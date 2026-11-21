package evmcompiler

import (
	"container/heap"
	"go/token"
	"go/types"
	"slices"
	"strings"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/values"
	"golang.org/x/tools/go/ssa"
)

// Analyze typed SSA, before virtual registers are reused. Lowering emits explicit
// Own bytecode; runtime property access needs no taint bit or scope lookup.
// Stored leaves are owned at insertion, so aggregate aliases cannot conceal a
// borrowed leaf. Opaque borrowed aggregates use the declared acquisition clone.
// borrowFacts are the domains a value may borrow storage from.
type borrowFacts map[string]bool
type ownershipClone struct {
	value  ssa.Value
	reason string
}
type ownershipPlan struct {
	before map[ssa.Instruction][]ownershipClone
	after  map[ssa.Instruction][]ownershipClone
}

func (f *lowerer) ownershipBinding(call *ssa.CallCommon) *evm.ExternalFunction {
	fn := call.StaticCallee()
	if fn == nil {
		return nil
	}
	if forwarded := f.forwardedHost(fn); forwarded != nil {
		fn = forwarded
	}
	binding, _, _ := f.c.nativeBindingFor(fn)
	return binding.function
}
func (f *lowerer) ownershipType(v ssa.Value) *values.Type {
	if v == nil {
		return nil
	}
	if f.c.deferValues[v] {
		return nil
	}
	if call, ok := v.(*ssa.Call); ok {
		if builtin, ok := call.Call.Value.(*ssa.Builtin); ok && builtin.Name() == "ssa:deferstack" {
			return nil
		}
	}
	if _, tuple := v.Type().(*types.Tuple); tuple {
		return nil
	}
	t, _ := f.c.env.valueType(v.Type())
	return t
}
func (f *lowerer) ownershipIntrinsic(call *ssa.CallCommon) bool {
	fn := call.StaticCallee()
	if fn == nil {
		return false
	}
	obj, _ := fn.Object().(*types.Func)
	_, ok := f.c.env.recordMethods[obj]
	return ok
}

// This summary proves ordinary read-only helpers safe without cloning their
// arguments. Recursive/indirect calls and possible reference returns are kept
// conservative. Stores in a helper are handled by owning arguments at its caller.
func (f *lowerer) ownershipSafeCall(call *ssa.CallCommon, retaining bool) bool {
	return f.ownershipSafeCallIn(call, retaining, make(map[*ssa.Function]bool))
}

type safeCall struct {
	fn        *ssa.Function
	retaining bool
}

// walking holds the helpers on the path from the call to the one being walked.
func (f *lowerer) ownershipSafeCallIn(call *ssa.CallCommon, retaining bool, walking map[*ssa.Function]bool) bool {
	if f.ownershipIntrinsic(call) {
		return true
	}
	if builtin, ok := call.Value.(*ssa.Builtin); ok {
		switch builtin.Name() {
		case "len", "cap", "real", "imag", "complex", "min", "max", "delete", "clear", "ssa:wrapnilchk":
			return true
		}
		return !retaining && (builtin.Name() == "append" || builtin.Name() == "copy")
	}
	if b := f.ownershipBinding(call); b != nil {
		if b.Ownership == nil {
			return false
		}
		if retaining {
			return b.Ownership.NoRetain
		}
		return b.Ownership.NoInvalidate
	}
	fn := call.StaticCallee()
	if fn == nil || len(fn.Blocks) == 0 || walking[fn] {
		return false
	}
	// A helper is walked once. Without that a chain of helpers is walked once
	// per call site, and helpers that each call the previous one twice are
	// walked 2^depth times.
	//
	// The answer does not depend on the path. A safe helper reaches no cycle,
	// so no path can cut its walk short. An unsafe one stays unsafe: the first
	// unsafe thing found ends the whole walk, and a walk that ran into a
	// helper further up its path had found a cycle through this one.
	//
	// Walking also builds the shapes of the types it meets, and their order
	// is the order of the module's shape table. A second walk builds none: it
	// repeats the first until one of them stops, and where the first stopped
	// at a helper on its path, that helper had by then been walked up to the
	// call that led back here, which is as far as any walk of it gets.
	key := safeCall{fn, retaining}
	if safe, ok := f.c.safeCalls[key]; ok {
		return safe
	}
	errors := len(f.c.errors)
	walking[fn] = true
	safe := f.ownershipSafeBody(fn, retaining, walking)
	delete(walking, fn)
	if len(f.c.errors) == errors { // A walk that reports an error reports it each time.
		if f.c.safeCalls == nil {
			f.c.safeCalls = make(map[safeCall]bool)
		}
		f.c.safeCalls[key] = safe
	}
	return safe
}

func (f *lowerer) ownershipSafeBody(fn *ssa.Function, retaining bool, walking map[*ssa.Function]bool) bool {
	for _, block := range fn.Blocks {
		for _, ins := range block.Instrs {
			switch i := ins.(type) {
			case *ssa.Store, *ssa.MapUpdate, *ssa.MakeClosure:
				if retaining {
					return false
				}
			case *ssa.Return:
				if retaining {
					for _, v := range i.Results {
						if t := f.ownershipType(v); t == nil || t.Repr() != values.Raw {
							return false
						}
					}
				}
			case *ssa.Call:
				if !f.ownershipSafeCallIn(&i.Call, retaining, walking) {
					return false
				}
			case *ssa.Defer, *ssa.RunDefers, *ssa.Panic, *ssa.Go:
				return false
			}
		}
	}
	return true
}

// ownershipCopyWeight is what one planned copy takes from MaxBranchWeight:
// the instruction, its site and its bookkeeping, next to the eight bytes of
// a unit.
const ownershipCopyWeight = 256

func (f *lowerer) planOwnership() *ownershipPlan {
	plan := &ownershipPlan{before: make(map[ssa.Instruction][]ownershipClone), after: make(map[ssa.Instruction][]ownershipClone)}
	facts := make(map[ssa.Value]borrowFacts)
	localContainers := f.localBorrowContainers()
	add := func(v ssa.Value, domain string) {
		if facts[v] == nil {
			facts[v] = make(borrowFacts)
		}
		facts[v][domain] = true
	}
	type plannedClone struct {
		ins   ssa.Instruction
		value ssa.Value
		after bool
	}
	planned := make(map[plannedClone]bool)
	sites := make(map[ssa.Value][]ssa.Instruction) // where a value is cloned before an instruction
	addClone := func(ins ssa.Instruction, v ssa.Value, after bool, reason string) {
		key := plannedClone{ins, v, after}
		if planned[key] || f.c.overBudget {
			return
		}
		planned[key] = true
		// Borrowed values times the branches that end their scope is as many
		// copies; they come out of what the functions left of the budget.
		if f.c.budget -= ownershipCopyWeight; f.c.budget < 0 {
			f.c.overBudget = true
			f.c.fail(f.fn.Pos(), "function is too large: it copies borrowed values in more places than the compile budget allows")
			return
		}
		if after {
			plan.after[ins] = append(plan.after[ins], ownershipClone{v, reason})
		} else {
			plan.before[ins] = append(plan.before[ins], ownershipClone{v, reason})
			sites[v] = append(sites[v], ins)
		}
	}
	for _, param := range f.fn.Params {
		if t := f.ownershipType(param); t != nil && t.CanOwn() && f.isReadRecord(t) {
			add(param, "evm.record")
		}
	}
	for _, block := range f.fn.Blocks {
		for _, ins := range block.Instrs {
			if v, ok := ins.(ssa.Value); ok {
				if _, alloc := v.(*ssa.Alloc); alloc && f.isReadRecord(f.ownershipType(v)) {
					add(v, "evm.record")
				}
			}
			call, ok := ins.(*ssa.Call)
			if !ok || f.ownershipIntrinsic(&call.Call) {
				continue
			}
			binding := f.ownershipBinding(&call.Call)
			var sources []evm.BorrowSource
			if binding != nil && binding.Ownership != nil {
				if err := binding.Ownership.Validate(binding.Signature); err != nil {
					f.c.fail(ins.Pos(), "%v", err)
					continue
				}
				sources = binding.Ownership.Borrowed
				for _, cb := range binding.Ownership.Callbacks {
					if cb.Argument >= len(call.Call.Args) {
						continue
					}
					if _, ok := call.Call.Args[cb.Argument].Type().Underlying().(*types.Signature); !ok {
						f.c.fail(ins.Pos(), "scoped callback argument is not a function")
					}
				}
			}
			// Immutable KVO result types have an ownership operation even for old
			// bindings that have not yet supplied finer scope/effect declarations.
			if results, ok := call.Type().(*types.Tuple); ok {
				for i := 0; i < results.Len(); i++ {
					t, _ := f.c.env.valueType(results.At(i).Type())
					if t.CanOwn() && f.isReadRecord(t) && !slices.ContainsFunc(sources, func(s evm.BorrowSource) bool { return s.Result == i }) {
						sources = append(slices.Clone(sources), evm.BorrowSource{Result: i, Domain: "evm.record"})
					}
				}
			} else if t := f.ownershipType(call); t != nil && t.CanOwn() && f.isReadRecord(t) && len(sources) == 0 {
				sources = []evm.BorrowSource{{Domain: "evm.record"}}
			}
			for _, source := range sources {
				// Scopes of one domain are not told apart yet: a scope that ends
				// ends every borrow of its domain. The argument must still exist.
				if source.ScopeParameter > 0 {
					_ = call.Call.Args[source.ScopeParameter-1]
				}
				var results []ssa.Value
				if _, tuple := call.Type().(*types.Tuple); tuple {
					for _, use := range *call.Referrers() {
						if x, ok := use.(*ssa.Extract); ok && x.Index == source.Result {
							results = append(results, x)
						}
					}
				} else if source.Result == 0 {
					results = []ssa.Value{call}
				}
				for _, v := range results {
					t := f.ownershipType(v)
					if t == nil || !t.CanOwn() {
						f.c.fail(ins.Pos(), "borrowed result has no ownership operation")
						continue
					}
					if t == values.StringType || f.isReadRecord(t) {
						add(v, source.Domain)
					} else {
						addClone(v.(ssa.Instruction), v, true, "acquisition fallback: opaque borrowed value")
					}
				}
			}
		}
	}
	if len(facts) == 0 {
		return plan
	}
	f.spreadBorrows(facts, localContainers)

	// Escapes own a borrowed value where it leaves the function's sight.
	live := newLiveness(f.fn)
	live.prepare()
	var boundaries scopeBoundaries
	for _, block := range f.fn.Blocks {
		for j, ins := range block.Instrs {
			own := func(v ssa.Value, reason string) {
				if len(facts[v]) != 0 {
					addClone(ins, v, false, reason)
				}
			}
			var invalidates bool
			var ends []evm.ScopeEnd
			switch i := ins.(type) {
			case *ssa.UnOp:
				if i.Op == token.MUL && f.isReadRecord(f.ownershipType(i.X)) {
					own(i.X, "escape: explicit record struct copy")
				}
			case *ssa.Return:
				for _, v := range i.Results {
					own(v, "escape: return")
				}
			case *ssa.Store:
				if localContainers[i.Addr] == nil {
					own(i.Val, "escape: store borrowed leaf")
				}
			case *ssa.MapUpdate:
				if localContainers[i.Map] == nil {
					own(i.Key, "escape: map key")
					own(i.Value, "escape: map value")
				}
			case *ssa.MakeClosure:
				for _, v := range i.Bindings {
					own(v, "escape: closure capture")
				}
			case *ssa.Call:
				if !f.ownershipSafeCall(&i.Call, true) {
					for _, v := range i.Call.Args {
						own(v, "escape: retaining call")
					}
				}
				invalidates = !f.ownershipSafeCall(&i.Call, false)
				if b := f.ownershipBinding(&i.Call); b != nil && b.Ownership != nil {
					ends = b.Ownership.Ends
				}
			case *ssa.Defer:
				for _, v := range i.Call.Args {
					own(v, "escape: deferred call")
				}
				own(i.Call.Value, "escape: deferred closure")
			case *ssa.Panic:
				own(i.X, "escape: panic")
			case *ssa.RunDefers:
				invalidates = true
			}
			if invalidates {
				boundaries.add(len(f.fn.Blocks), block.Index, j, ends)
			}
		}
	}

	// A scope boundary owns each borrowed value that is live across it and
	// that it may end. Of the boundaries of one block only the first counts
	// for a value: the clone there discharges the later ones. Each value
	// looks only at the blocks it is live in; asking every boundary about
	// every value is the product of the two.
	if len(boundaries.blocks) > 0 {
		each := func(v ssa.Value) {
			if len(facts[v]) == 0 || f.c.overBudget {
				return
			}
			definition, _ := v.(ssa.Instruction)
			matches := boundaries.matching(facts[v])
			r := live.rangeOf(v)
			r.eachBlock(func(b int32) {
				groups := boundaries.blocks[b]
				if len(groups) == 0 {
					return
				}
				block := f.fn.Blocks[b]
				after := int32(-1)
				if definition != nil {
					if definition.Block() == block {
						after = live.index[definition]
					} else if !definition.Block().Dominates(block) {
						return
					}
				}
				r.eachSpan(b, func(lo, hi int32) bool {
					first := int32(-1)
					for _, group := range groups {
						if !matches[group.class] {
							continue
						}
						i, _ := slices.BinarySearch(group.positions, max(lo, after+1))
						if i < len(group.positions) && group.positions[i] <= hi && (first < 0 || group.positions[i] < first) {
							first = group.positions[i]
						}
					}
					if first < 0 {
						return true
					}
					addClone(block.Instrs[first], v, false, "scope boundary: live borrowed value")
					return false
				})
			})
		}
		for _, param := range f.fn.Params {
			each(param)
		}
		for _, block := range f.fn.Blocks {
			for _, ins := range block.Instrs {
				if v, ok := ins.(ssa.Value); ok {
					each(v)
				}
			}
		}
	}

	// A dominating clone discharges subsequent copies of the same SSA value.
	var order *dominatorOrder
	for v, at := range sites {
		if len(at) < 2 {
			continue
		}
		if order == nil {
			order = newDominatorOrder(f.fn)
		}
		slices.SortFunc(at, func(a, b ssa.Instruction) int {
			if x, y := a.Block(), b.Block(); x != y {
				return int(order.enter[x.Index] - order.enter[y.Index])
			}
			return int(live.index[a] - live.index[b])
		})
		// open holds the blocks with a surviving clone that dominate the
		// current one; a block dominates itself, and its later clones.
		var open []*ssa.BasicBlock
		for _, ins := range at {
			block := ins.Block()
			for len(open) > 0 && !order.dominates(open[len(open)-1], block) {
				open = open[:len(open)-1]
			}
			if len(open) == 0 {
				open = append(open, block)
				continue
			}
			plan.before[ins] = slices.DeleteFunc(plan.before[ins], func(clone ownershipClone) bool { return clone.value == v })
		}
	}
	return plan
}

// checkOwnership and checkRecordLists, when set, receive each function once
// its plan or its lists are made. Tests set them to compare the results with
// those of the implementations these replaced.
var checkOwnership, checkRecordLists func(f *lowerer)

// spreadBorrows carries the domains of each borrowed value to the values
// derived from it, until nothing changes.
//
// Sweeping the function until a sweep changes nothing takes one sweep per
// link of the longest chain of flows that runs against block order. Here a
// value passes its domains on when it gains one. Asking whether a value can
// hold a borrow builds the shape of its type, and the order in which shapes
// are first built is the order of the module's shape table. So the values are
// asked in the order the sweeps would ask them: a flow is first followed in
// the sweep in which its source gains a domain if it comes later in the
// function, and in the next sweep otherwise.
func (f *lowerer) spreadBorrows(facts map[ssa.Value]borrowFacts, localContainers map[ssa.Value][]ssa.Value) {
	nodes := make(map[ssa.Value]*borrowNode)
	containers := make(map[*ssa.Value]*borrowNode)
	nodeOf := func(v ssa.Value) *borrowNode {
		n := nodes[v]
		if n == nil {
			n = &borrowNode{value: v, facts: facts[v]}
			nodes[v] = n
		}
		return n
	}
	position := int32(0) // of the instruction being read
	flow := func(dst, src ssa.Value) {
		from := nodeOf(src)
		from.flows = append(from.flows, borrowFlow{nodeOf(dst), position})
	}
	// A store into a local container reaches each of its aliases. They share
	// one node, or n stores into n elements of an array would be n*n flows.
	store := func(container ssa.Value, src ssa.Value) {
		aliases := localContainers[container]
		if len(aliases) == 0 {
			return
		}
		shared := containers[&aliases[0]]
		if shared == nil {
			shared = &borrowNode{aliases: aliases}
			containers[&aliases[0]] = shared
		}
		from := nodeOf(src)
		from.flows = append(from.flows, borrowFlow{shared, position})
	}
	for _, block := range f.fn.Blocks {
		for _, ins := range block.Instrs {
			position++
			switch i := ins.(type) {
			case *ssa.Store:
				store(i.Addr, i.Val)
			case *ssa.MapUpdate:
				store(i.Map, i.Key)
				store(i.Map, i.Value)
			case *ssa.Index:
				flow(i, i.X)
			case *ssa.Field:
				flow(i, i.X)
			case *ssa.Lookup:
				if !i.CommaOk {
					flow(i, i.X)
				}
			case *ssa.Phi:
				for _, v := range i.Edges {
					flow(i, v)
				}
			case *ssa.ChangeType:
				flow(i, i.X)
			case *ssa.ChangeInterface:
				flow(i, i.X)
			case *ssa.MakeInterface:
				flow(i, i.X)
			case *ssa.TypeAssert:
				if !i.CommaOk {
					flow(i, i.X)
				}
			case *ssa.Extract:
				if lookup, ok := i.Tuple.(*ssa.Lookup); ok && i.Index == 0 {
					flow(i, lookup.X)
				}
				if next, ok := i.Tuple.(*ssa.Next); ok && i.Index > 0 {
					if rng, ok := next.Iter.(*ssa.Range); ok {
						flow(i, rng.X)
					}
				}
				if assertion, ok := i.Tuple.(*ssa.TypeAssert); ok && i.Index == 0 {
					flow(i, assertion.X)
				}
			case *ssa.Slice:
				flow(i, i.X)
			case *ssa.Convert:
				if a, ok := i.X.Type().Underlying().(*types.Basic); ok && a.Kind() == types.String {
					if b, ok := i.Type().Underlying().(*types.Basic); ok && b.Kind() == types.String {
						flow(i, i.X)
					}
				}
			case *ssa.UnOp:
				if i.Op == token.MUL {
					flow(i, i.X)
					if ref, _, ok := f.recordRef(i.X); ok {
						flow(i, ref.Call.Args[0])
					}
				}
			}
		}
	}

	// First, which values can hold a borrow, asked in sweep order.
	accepts := func(n *borrowNode) bool {
		if n.asked {
			return n.accepts
		}
		n.asked = true
		t := f.ownershipType(n.value)
		if t == nil {
			return false
		}
		if !t.CanOwn() && localContainers[n.value] == nil {
			s, err := f.c.env.shape(n.value.Type())
			if err != nil || s.Kind != values.InterfaceKind && s.Kind != values.StringKind {
				return false
			}
		}
		n.accepts = true
		return true
	}
	var pending borrowFlowHeap
	reach := func(n *borrowNode, sweep, position int32) {
		if n.reached {
			return
		}
		n.reached = true
		for _, flow := range n.flows {
			at := sweep
			if flow.position <= position {
				at++
			}
			heap.Push(&pending, pendingBorrowFlow{at, n, flow})
		}
	}
	var borrowed []*borrowNode
	for v := range facts {
		if n := nodes[v]; n != nil {
			reach(n, 0, 0)
			borrowed = append(borrowed, n)
		}
	}
	for pending.Len() > 0 {
		next := heap.Pop(&pending).(pendingBorrowFlow)
		dst := next.flow.to
		if dst.aliases == nil {
			if accepts(dst) {
				next.from.to = append(next.from.to, dst)
				reach(dst, next.sweep, next.flow.position)
			}
			continue
		}
		if !dst.asked {
			dst.asked = true
			for _, alias := range dst.aliases {
				if n := nodeOf(alias); accepts(n) {
					dst.to = append(dst.to, n)
					reach(n, next.sweep, next.flow.position)
				}
			}
		}
		next.from.to = append(next.from.to, dst)
	}

	// Then the domains, along the flows that carry any.
	for len(borrowed) > 0 {
		n := borrowed[len(borrowed)-1]
		borrowed = borrowed[:len(borrowed)-1]
		n.queued = false
		for _, dst := range n.to {
			changed := false
			for domain := range n.facts {
				if !dst.facts[domain] {
					if dst.facts == nil {
						dst.facts = make(borrowFacts)
						if dst.value != nil {
							facts[dst.value] = dst.facts
						}
					}
					dst.facts[domain] = true
					changed = true
				}
			}
			if changed && !dst.queued {
				dst.queued = true
				borrowed = append(borrowed, dst)
			}
		}
	}
}

// borrowNode is a value in the graph of borrow flows, or the aliases of one
// local container taken together.
type borrowNode struct {
	value   ssa.Value
	aliases []ssa.Value
	facts   borrowFacts
	flows   []borrowFlow  // every flow out of the value, in function order
	to      []*borrowNode // the targets that can hold a borrow
	asked   bool
	accepts bool
	reached bool // its flows are pending or followed
	queued  bool
}

type borrowFlow struct {
	to       *borrowNode
	position int32 // of the instruction that makes the flow
}

type pendingBorrowFlow struct {
	sweep int32
	from  *borrowNode
	flow  borrowFlow
}

type borrowFlowHeap []pendingBorrowFlow

func (h borrowFlowHeap) Len() int { return len(h) }
func (h borrowFlowHeap) Less(i, j int) bool {
	if h[i].sweep != h[j].sweep {
		return h[i].sweep < h[j].sweep
	}
	return h[i].flow.position < h[j].flow.position
}
func (h borrowFlowHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *borrowFlowHeap) Push(x any)   { *h = append(*h, x.(pendingBorrowFlow)) }
func (h *borrowFlowHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// scopeBoundaries are the instructions that may end a scope, by block. Those
// of a block are grouped by the domains they end, so that a value finds the
// first boundary that concerns it without passing over the others.
type scopeBoundaries struct {
	blocks  [][]boundaryGroup
	classes []([]evm.ScopeEnd)
	byKey   map[string]int
}

type boundaryGroup struct {
	class     int
	positions []int32 // in the block, ascending
}

func (s *scopeBoundaries) add(blocks, block, index int, ends []evm.ScopeEnd) {
	if s.blocks == nil {
		s.blocks = make([][]boundaryGroup, blocks)
		s.byKey = make(map[string]int)
	}
	var key strings.Builder
	for _, end := range ends {
		key.WriteString(end.Domain)
		key.WriteByte(0)
	}
	class, ok := s.byKey[key.String()]
	if !ok {
		class = len(s.classes)
		s.classes = append(s.classes, ends)
		s.byKey[key.String()] = class
	}
	groups := s.blocks[block]
	for i := range groups {
		if groups[i].class == class {
			groups[i].positions = append(groups[i].positions, int32(index))
			return
		}
	}
	s.blocks[block] = append(groups, boundaryGroup{class, []int32{int32(index)}})
}

// matching tells, for each class of boundary, whether it may end a borrow of
// one of the domains. A boundary that names no domain may end any; a record
// borrow ends at every boundary.
func (s *scopeBoundaries) matching(facts borrowFacts) []bool {
	matches := make([]bool, len(s.classes))
	for class, ends := range s.classes {
		matches[class] = len(ends) == 0
		for domain := range facts {
			for _, end := range ends {
				matches[class] = matches[class] || domain == "evm.record" || domain == end.Domain
			}
		}
	}
	return matches
}

// dominatorOrder numbers the dominator tree so that sorting blocks by entry
// puts each block before the ones it dominates.
type dominatorOrder struct{ enter, leave []int32 }

func newDominatorOrder(fn *ssa.Function) *dominatorOrder {
	o := &dominatorOrder{make([]int32, len(fn.Blocks)), make([]int32, len(fn.Blocks))}
	type frame struct {
		block *ssa.BasicBlock
		next  int
	}
	var stack []frame
	clock := int32(0)
	for _, root := range fn.Blocks {
		if root.Idom() != nil {
			continue
		}
		clock++
		o.enter[root.Index] = clock
		stack = append(stack[:0], frame{root, 0})
		for len(stack) > 0 {
			top := &stack[len(stack)-1]
			if children := top.block.Dominees(); top.next < len(children) {
				child := children[top.next]
				top.next++
				clock++
				o.enter[child.Index] = clock
				stack = append(stack, frame{child, 0})
				continue
			}
			clock++
			o.leave[top.block.Index] = clock
			stack = stack[:len(stack)-1]
		}
	}
	return o
}

// dominates is BasicBlock.Dominates: a block dominates itself.
func (o *dominatorOrder) dominates(a, b *ssa.BasicBlock) bool {
	return o.enter[a.Index] <= o.enter[b.Index] && o.leave[b.Index] <= o.leave[a.Index]
}

func (f *lowerer) emitOwnership(clones []ownershipClone) {
	for _, c := range clones {
		r := f.regs[c.value][0]
		f.code.OwnershipSites = append(f.code.OwnershipSites, evm.OwnershipSite{PC: len(f.code.Code), Reason: c.reason})
		f.extended(evm.ExtendedOwn, f.shape(c.value.Type()), []vreg{r}, []vreg{r}, 0, 0, nil)
	}
}
