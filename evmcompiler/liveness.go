package evmcompiler

import (
	"math"
	"slices"
	"sort"

	"golang.org/x/tools/go/ssa"
)

// liveness tells whether an SSA value is live after an instruction.
//
// The equations are the usual backward ones with two rules of their own. A phi
// operand is a use at the end of the predecessor it comes from, not in the
// block of the phi: in a Next loop a consumed row must not look live across
// the acquisition of the next one. And a block's successors, and the phis of
// each, apply in order: a phi ends the value it defines for the successors
// before it and for the phis above it.
//
// No equation mixes two values, so each value is solved alone, the first time
// a caller asks about it, by walking back from its uses. A function costs one
// scan and each value the blocks it is live in, where sets of all live values
// per block cost the square of the function.
type liveness struct {
	fn     *ssa.Function
	index  map[ssa.Instruction]int32 // position of an instruction in its block
	ids    map[ssa.Value]int32       // operands other than constants
	first  []int32                   // uses[first[id]:first[id+1]] are the uses of a value
	uses   []liveUse
	ranges map[ssa.Value]*liveRange
	in     []int32 // per block: the serial of the last value found live into it
	out    []int32
	serial int32
	queue  []int32
}

// liveUse is an instruction that has the value as an operand.
type liveUse struct {
	block, index int32
	pred         int32 // for a phi, the predecessor the operand comes from; else -1
}

// liveRange is where one value is live.
type liveRange struct {
	out     []int32     // blocks the value is live out of, sorted
	touches []liveTouch // its definition and its uses outside phis, sorted
}

type liveTouch struct {
	block, index int32
	use          bool
}

type liveEdge struct{ pred, succ int32 }

func newLiveness(fn *ssa.Function) *liveness { return &liveness{fn: fn} }

func (l *liveness) prepare() {
	if l.index != nil {
		return
	}
	fn := l.fn
	instructions := 0
	for _, b := range fn.Blocks {
		instructions += len(b.Instrs)
	}
	l.index = make(map[ssa.Instruction]int32, instructions)
	l.ids = make(map[ssa.Value]int32, instructions)
	var counts []int32
	var operands []*ssa.Value
	each := func(visit func(v ssa.Value, use liveUse)) {
		for _, b := range fn.Blocks {
			for j, ins := range b.Instrs {
				operands = ins.Operands(operands[:0])
				phi, _ := ins.(*ssa.Phi)
				for n, p := range operands {
					if p == nil || *p == nil {
						continue
					}
					if _, constant := (*p).(*ssa.Const); constant {
						continue
					}
					use := liveUse{int32(b.Index), int32(j), -1}
					if phi != nil {
						use.pred = int32(b.Preds[n].Index)
					}
					visit(*p, use)
				}
			}
		}
	}
	for _, b := range fn.Blocks {
		for j, ins := range b.Instrs {
			l.index[ins] = int32(j)
		}
	}
	each(func(v ssa.Value, _ liveUse) {
		id, ok := l.ids[v]
		if !ok {
			id = int32(len(counts))
			l.ids[v] = id
			counts = append(counts, 0)
		}
		counts[id]++
	})
	l.first = make([]int32, len(counts)+1)
	for id, n := range counts {
		l.first[id+1] = l.first[id] + n
	}
	l.uses = make([]liveUse, l.first[len(counts)])
	next := slices.Clone(l.first[:len(counts)])
	each(func(v ssa.Value, use liveUse) {
		id := l.ids[v]
		l.uses[next[id]] = use
		next[id]++
	})
	l.ranges = make(map[ssa.Value]*liveRange)
	l.in = make([]int32, len(fn.Blocks))
	l.out = make([]int32, len(fn.Blocks))
}

// usesOf returns the instructions that have v as an operand, in block order.
func (l *liveness) usesOf(v ssa.Value) []liveUse {
	if id, ok := l.ids[v]; ok {
		return l.uses[l.first[id]:l.first[id+1]]
	}
	if _, constant := v.(*ssa.Const); !constant {
		return nil
	}
	// Each constant operand is a value of its own, with one use.
	var uses []liveUse
	var operands []*ssa.Value
	for _, b := range l.fn.Blocks {
		for j, ins := range b.Instrs {
			operands = ins.Operands(operands[:0])
			for n, p := range operands {
				if p == nil || *p != v {
					continue
				}
				use := liveUse{int32(b.Index), int32(j), -1}
				if _, phi := ins.(*ssa.Phi); phi {
					use.pred = int32(b.Preds[n].Index)
				}
				uses = append(uses, use)
			}
		}
	}
	return uses
}

// rangeOf solves the equations for one value.
func (l *liveness) rangeOf(v ssa.Value) *liveRange {
	l.prepare()
	if r := l.ranges[v]; r != nil {
		return r
	}
	r := &liveRange{}
	l.ranges[v] = r
	blocks := l.fn.Blocks

	// A phi ends its value on every edge into its block, after the phis above it.
	phiBlock, phiIndex := int32(-1), int32(-1)
	if ins, ok := v.(ssa.Instruction); ok {
		if j, ok := l.index[ins]; ok {
			b := int32(ins.Block().Index)
			r.touches = append(r.touches, liveTouch{b, j, false})
			if _, phi := ins.(*ssa.Phi); phi {
				phiBlock, phiIndex = b, j
			}
		}
	}
	// An edge starts the value at the last phi that reads it from that edge.
	var edges map[liveEdge]int32
	for _, use := range l.usesOf(v) {
		if use.pred < 0 {
			r.touches = append(r.touches, liveTouch{use.block, use.index, true})
			continue
		}
		if edges == nil {
			edges = make(map[liveEdge]int32)
		}
		edge := liveEdge{use.pred, use.block}
		if last, ok := edges[edge]; !ok || use.index > last {
			edges[edge] = use.index
		}
	}
	slices.SortFunc(r.touches, func(a, b liveTouch) int {
		if a.block != b.block {
			return int(a.block - b.block)
		}
		if a.index != b.index {
			return int(a.index - b.index)
		}
		// Were an instruction to read its own result, the read would count.
		if a.use != b.use {
			if a.use {
				return -1
			}
			return 1
		}
		return 0
	})

	l.serial++
	serial := l.serial
	l.queue = l.queue[:0]
	liveIn := func(b int32) {
		if l.in[b] != serial {
			l.in[b] = serial
			l.queue = append(l.queue, b)
		}
	}
	liveOut := func(b int32) {
		if l.out[b] == serial {
			return
		}
		l.out[b] = serial
		r.out = append(r.out, b)
		// The first instruction of the block to touch the value decides.
		if touch, ok := r.touchAfter(b, -1); !ok || touch.use {
			liveIn(b)
		}
	}
	end := func(b *ssa.BasicBlock) bool {
		live := false
		for _, succ := range b.Succs {
			s := int32(succ.Index)
			if l.in[s] == serial {
				live = true
			}
			read, reads := edges[liveEdge{int32(b.Index), s}]
			switch {
			case reads && (s != phiBlock || read >= phiIndex):
				live = true
			case s == phiBlock:
				live = false
			}
		}
		return live
	}
	for i := 0; i < len(r.touches); i++ {
		if touch := r.touches[i]; i == 0 || r.touches[i-1].block != touch.block {
			if touch.use {
				liveIn(touch.block)
			}
		}
	}
	// Map order does not matter: the solution is the least one, whatever the order.
	for edge := range edges {
		if b := blocks[edge.pred]; end(b) {
			liveOut(edge.pred)
		}
	}
	for len(l.queue) > 0 {
		s := l.queue[len(l.queue)-1]
		l.queue = l.queue[:len(l.queue)-1]
		for _, pred := range blocks[s].Preds {
			if p := int32(pred.Index); l.out[p] != serial && end(pred) {
				liveOut(p)
			}
		}
	}
	slices.Sort(r.out)
	return r
}

// touchAfter returns the first touch in a block after the given position.
func (r *liveRange) touchAfter(block, index int32) (liveTouch, bool) {
	i := sort.Search(len(r.touches), func(i int) bool {
		t := r.touches[i]
		return t.block > block || t.block == block && t.index > index
	})
	if i < len(r.touches) && r.touches[i].block == block {
		return r.touches[i], true
	}
	return liveTouch{}, false
}

func (r *liveRange) liveOut(block int32) bool {
	_, found := slices.BinarySearch(r.out, block)
	return found
}

// eachBlock visits, in order, the blocks that may have an instruction after
// which the value is live.
func (r *liveRange) eachBlock(visit func(block int32)) {
	i, j, last := 0, 0, int32(-1)
	for i < len(r.out) || j < len(r.touches) {
		var b int32
		if j == len(r.touches) || i < len(r.out) && r.out[i] <= r.touches[j].block {
			b = r.out[i]
			i++
		} else {
			b = r.touches[j].block
			j++
		}
		if b != last {
			last = b
			visit(b)
		}
	}
}

// eachSpan visits, in order, the runs of positions lo..hi of a block after
// which the value is live, until visit returns false.
func (r *liveRange) eachSpan(block int32, visit func(lo, hi int32) bool) {
	i := sort.Search(len(r.touches), func(i int) bool { return r.touches[i].block >= block })
	lo := int32(0)
	for ; i < len(r.touches) && r.touches[i].block == block; i++ {
		touch := r.touches[i]
		if touch.use && lo < touch.index && !visit(lo, touch.index-1) {
			return
		}
		lo = touch.index
	}
	if r.liveOut(block) {
		visit(lo, math.MaxInt32)
	}
}

// liveAfterAt reports whether the value is live after the instruction at a
// position of a block.
func (r *liveRange) liveAfterAt(block, index int32) bool {
	if touch, ok := r.touchAfter(block, index); ok {
		return touch.use
	}
	return r.liveOut(block)
}

// liveAfter reports whether v is live after ins, an instruction of the function.
func (l *liveness) liveAfter(ins ssa.Instruction, v ssa.Value) bool {
	r := l.rangeOf(v)
	index, ok := l.index[ins]
	if !ok {
		return false
	}
	return r.liveAfterAt(int32(ins.Block().Index), index)
}
