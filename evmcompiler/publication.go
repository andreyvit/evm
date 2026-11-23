package evmcompiler

import (
	"go/token"
	"go/types"
	"slices"

	"golang.org/x/tools/go/ssa"
)

// A publication starts at the method value or closure passed to a concurrent
// parameter. Its effects flow forward over control flow, including back edges.
type publication struct {
	site      ssa.Instruction
	root      ssa.Value
	setupOnly bool
}
type publicationCheck struct {
	c         *typedCompiler
	functions []*ssa.Function
	pending   []publication
	seen      map[publication]bool
	callers   map[*ssa.Function][]ssa.CallInstruction
	closures  map[*ssa.Function][]*ssa.MakeClosure
	indirect  []ssa.CallInstruction
	returns   map[*ssa.Function][]*ssa.Return
	size      map[*ssa.Function]int
}

func (p *publicationCheck) add(site ssa.Instruction, root ssa.Value) {
	p.enqueue(publication{site: site, root: root})
}

func (p *publicationCheck) enqueue(k publication) {
	if p.seen == nil {
		p.seen = map[publication]bool{}
	}
	if !p.seen[k] {
		if !p.c.chargeConcurrentCheck(k.site.Pos(), 32) {
			return
		}
		p.seen[k] = true
		p.pending = append(p.pending, k)
	}
}
func (p *publicationCheck) run() {
	if len(p.pending) == 0 {
		return
	}
	p.callers = map[*ssa.Function][]ssa.CallInstruction{}
	p.closures = map[*ssa.Function][]*ssa.MakeClosure{}
	p.returns = map[*ssa.Function][]*ssa.Return{}
	p.size = map[*ssa.Function]int{}
	for _, f := range p.functions {
		for _, b := range f.Blocks {
			p.size[f] += len(b.Instrs)
			for _, ins := range b.Instrs {
				if ret, ok := ins.(*ssa.Return); ok {
					p.returns[f] = append(p.returns[f], ret)
				}
				if cl, ok := ins.(*ssa.MakeClosure); ok {
					fn := cl.Fn.(*ssa.Function)
					p.closures[fn] = append(p.closures[fn], cl)
				}
				if call, ok := ins.(ssa.CallInstruction); ok {
					if target := call.Common().StaticCallee(); target != nil {
						p.callers[target] = append(p.callers[target], call)
					} else if _, builtin := call.Common().Value.(*ssa.Builtin); !builtin {
						p.indirect = append(p.indirect, call)
					}
				}
			}
		}
	}
	for len(p.pending) > 0 && len(p.c.errors) == 0 {
		work := p.pending[0]
		p.pending = p.pending[1:]
		p.check(work)
	}
}

// Follow aliases in either direction: for example, a map can have been stored
// into a receiver field before the borrow, or loaded from it afterwards.
func (p *publicationCheck) check(work publication) {
	f := work.site.Parent()
	check := sharedCheck{c: p.c, pos: work.site.Pos(), aliases: map[types.Type]bool{}, seen: map[ssa.Value]bool{}}
	if !check.sharedAlias(work.root.Type()) {
		return
	}
	locked := p.publicationAliases(f, work.root, &check, work.site)
	if p.c.overBudget {
		return
	}
	for v := range locked {
		if _, ok := v.(*ssa.Global); ok {
			p.c.fail(work.site.Pos(), "cannot verify concurrent borrow of storage reachable through a package global")
			return
		}
	}
	if work.setupOnly {
		p.checkEarlierCalls(work, locked, check.aliases)
		return
	}
	// If a helper borrows a caller's object, continue the check at its callers.
	for i, param := range f.Params {
		if locked[param] {
			for _, call := range p.possibleCallers(f) {
				args := call.Common().Args
				if call.Common().IsInvoke() {
					args = append([]ssa.Value{call.Common().Value}, args...)
				}
				if i >= len(args) {
					continue
				}
				actual := args[i]
				p.add(call, actual)
				if len(f.Params) > 1 {
					callerAliases := p.publicationAliases(call.Parent(), actual, &check, call)
					if p.c.overBudget {
						return
					}
					for j, other := range args {
						if j < len(f.Params) && callerAliases[other] && !locked[f.Params[j]] {
							p.add(work.site, f.Params[j])
						}
					}
				}
			}
		}
	}
	for i, free := range f.FreeVars {
		if !locked[free] {
			continue
		}
		for _, closure := range p.closures[f] {
			for _, use := range *closure.Referrers() {
				if _, ok := use.(*ssa.DebugRef); ok {
					continue
				}
				if call, ok := use.(*ssa.Call); ok && call.Call.Value == closure {
					p.add(call, closure.Bindings[i])
				} else {
					p.add(closure, closure.Bindings[i])
				}
			}
		}
	}
	after := instructionsAfter(work.site)
	p.checkEarlierCalls(work, locked, check.aliases)
	if len(p.c.errors) > 0 {
		return
	}
	// A second closure can carry a writable alias out of the builder. Check
	// its use of that alias unless all its uses finish before the borrow.
	for _, b := range f.Blocks {
		for _, ins := range b.Instrs {
			closure, ok := ins.(*ssa.MakeClosure)
			if !ok {
				continue
			}
			for _, ref := range *closure.Referrers() {
				if _, ok := ref.(*ssa.DebugRef); ok {
					continue
				}
				if call, ok := ref.(*ssa.Call); ok && call.Call.Value == closure && !after[call] {
					continue
				}
				for i, bind := range closure.Bindings {
					if locked[bind] {
						check.add(closure.Fn.(*ssa.Function).FreeVars[i])
					}
				}
				break
			}
		}
	}
	// Check in source/SSA order for stable diagnostics.
	var operands [8]*ssa.Value
	for _, b := range f.Blocks {
		for _, ins := range b.Instrs {
			_, deferred := ins.(*ssa.Defer)
			if !after[ins] && !deferred {
				continue
			}
			switch x := ins.(type) {
			case *ssa.Store:
				if locked[x.Addr] {
					p.fail(work, ins)
					return
				}
			case *ssa.MapUpdate:
				if locked[x.Map] {
					p.fail(work, ins)
					return
				}
			case *ssa.Panic:
				if locked[x.X] {
					p.fail(work, ins)
					return
				}
			case *ssa.Return:
				for _, v := range x.Results {
					if locked[v] {
						for _, call := range p.possibleCallers(f) {
							if value, ok := call.(*ssa.Call); ok {
								p.add(value, value)
							}
						}
					}
				}
			case ssa.CallInstruction:
				call := x.Common()
				if builtin, ok := call.Value.(*ssa.Builtin); ok {
					switch builtin.Name() {
					case "copy", "append", "clear", "delete":
						if len(call.Args) > 0 && locked[call.Args[0]] {
							p.fail(work, ins)
							return
						}
					}
					continue
				}
				for idx, arg := range ins.Operands(operands[:0]) {
					if arg == nil || *arg == nil {
						continue
					}
					if idx > 0 && p.checkedParam(call, idx-1) {
						continue
					}
					if idx == 0 {
						if _, known := call.Value.(*ssa.MakeClosure); known {
							// Its captured bindings are checked above.
							continue
						}
					}
					if locked[*arg] && !check.safeUse(*arg, ins) {
						p.fail(work, ins)
						return
					}
				}
			}
		}
	}
	check.run()
}

func (p *publicationCheck) checkedParam(call *ssa.CallCommon, i int) bool {
	pkg, key, off := p.c.concurrentCallbackTarget(call)
	return pkg != nil && (slices.Contains(pkg.ConcurrentInterfaceParams[key], i-off) ||
		slices.Contains(pkg.ConcurrentCallbackParams[key], i-off))
}

func (p *publicationCheck) fail(w publication, use ssa.Instruction) {
	pos := use.Pos()
	if !pos.IsValid() {
		pos = w.site.Pos()
	}
	p.c.fail(pos, "cannot mutate or pass a mutable alias after a concurrent borrow: %s", use)
}

func (p *publicationCheck) publicationAliases(f *ssa.Function, root ssa.Value, check *sharedCheck, site ssa.Instruction) map[ssa.Value]bool {
	// Each root needs another pass over this function's SSA.
	if !p.c.chargeConcurrentCheck(site.Pos(), p.size[f]+1) {
		return nil
	}
	links := map[ssa.Value][]ssa.Value{}
	link := func(a, b ssa.Value) {
		if a == nil || b == nil || !check.sharedAlias(a.Type()) || !check.sharedAlias(b.Type()) {
			return
		}
		if _, ok := a.(*ssa.Const); ok {
			return
		}
		if _, ok := b.(*ssa.Const); ok {
			return
		}
		links[a] = append(links[a], b)
		links[b] = append(links[b], a)
	}
	for _, b := range f.Blocks {
		for _, ins := range b.Instrs {
			switch x := ins.(type) {
			case *ssa.FieldAddr:
				link(x, x.X)
			case *ssa.IndexAddr:
				link(x, x.X)
			case *ssa.Field:
				link(x, x.X)
			case *ssa.Index:
				link(x, x.X)
			case *ssa.Lookup:
				link(x, x.X)
			case *ssa.Slice:
				link(x, x.X)
			case *ssa.Range:
				link(x, x.X)
			case *ssa.Next:
				link(x, x.Iter)
			case *ssa.Extract:
				link(x, x.Tuple)
			case *ssa.TypeAssert:
				link(x, x.X)
			case *ssa.ChangeType:
				link(x, x.X)
			case *ssa.Convert:
				link(x, x.X)
			case *ssa.ChangeInterface:
				link(x, x.X)
			case *ssa.MakeInterface:
				link(x, x.X)
			case *ssa.SliceToArrayPointer:
				link(x, x.X)
			case *ssa.UnOp:
				if x.Op == token.MUL {
					link(x, x.X)
				}
			case *ssa.Phi:
				for _, v := range x.Edges {
					link(x, v)
				}
			case *ssa.Store:
				link(x.Addr, x.Val)
			case *ssa.MapUpdate:
				link(x.Map, x.Value)
				link(x.Map, x.Key)
			case *ssa.MakeClosure:
				for _, binding := range x.Bindings {
					link(x, binding)
				}
			case ssa.CallInstruction:
				if ins == site {
					continue
				}
				var first ssa.Value
				call := x.Common()
				args := call.Args
				if builtin, ok := call.Value.(*ssa.Builtin); ok && builtin.Name() == "copy" {
					// The containers have distinct storage, but both can now
					// reach the same objects through their elements.
					if dst, ok := args[0].Type().Underlying().(*types.Slice); ok && check.sharedAlias(dst.Elem()) {
						link(args[0], args[1])
					}
				}
				if closure, ok := call.Value.(*ssa.MakeClosure); ok {
					args = append(slices.Clone(args), closure.Bindings...)
				}
				for _, arg := range args {
					if result := x.Value(); result != nil {
						link(result, arg)
					}
					// A guest helper can store one argument inside another.
					if fn := call.StaticCallee(); fn != nil && len(fn.Blocks) > 0 && check.sharedAlias(arg.Type()) {
						if first == nil {
							first = arg
						} else {
							link(first, arg)
						}
					}
				}
			}
		}
	}
	locked := map[ssa.Value]bool{root: true}
	queue := []ssa.Value{root}
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		for _, w := range links[v] {
			if !locked[w] {
				locked[w] = true
				queue = append(queue, w)
			}
		}
	}
	return locked
}
func instructionsAfter(site ssa.Instruction) map[ssa.Instruction]bool {
	after := map[ssa.Instruction]bool{}
	start := site.Block()
	past := false
	for _, ins := range start.Instrs {
		if past {
			after[ins] = true
		}
		if ins == site {
			past = true
		}
	}
	blocks := append([]*ssa.BasicBlock(nil), start.Succs...)
	visited := map[*ssa.BasicBlock]bool{}
	for len(blocks) > 0 {
		b := blocks[0]
		blocks = blocks[1:]
		if visited[b] {
			continue
		}
		visited[b] = true
		for _, ins := range b.Instrs {
			after[ins] = true
		}
		blocks = append(blocks, b.Succs...)
	}
	return after
}
func (p *publicationCheck) checkEarlierCalls(work publication, locked map[ssa.Value]bool, aliases map[types.Type]bool) {
	f := work.site.Parent()
	start := work.site.Block()
	// Inspect earlier calls on paths to the borrow. Arbitrary Go code can
	// retain a mutable argument even when it returns no value.
	initCheck := sharedCheck{c: p.c, pos: work.site.Pos(), aliases: aliases, seen: map[ssa.Value]bool{}, initializing: true}
	beforeBlocks := map[*ssa.BasicBlock]bool{start: true}
	predecessors := append([]*ssa.BasicBlock(nil), start.Preds...)
	for len(predecessors) > 0 {
		b := predecessors[0]
		predecessors = predecessors[1:]
		if beforeBlocks[b] {
			continue
		}
		beforeBlocks[b] = true
		predecessors = append(predecessors, b.Preds...)
	}
	for _, b := range f.Blocks {
		if !beforeBlocks[b] {
			continue
		}
		for _, ins := range b.Instrs {
			if ins == work.site {
				break
			}
			call, ok := ins.(ssa.CallInstruction)
			if !ok {
				continue
			}
			common := call.Common()
			if _, builtin := common.Value.(*ssa.Builtin); builtin {
				continue
			}
			target := common.StaticCallee()
			if result := call.Value(); result != nil && locked[result] {
				if target == nil {
					p.c.fail(ins.Pos(), "cannot verify concurrent borrow of an indirect call result")
					return
				}
				if concurrencyFunction(target) == nil {
					p.checkSetupOrigins(target, nil)
				}
			}
			if closure, ok := common.Value.(*ssa.MakeClosure); ok {
				for i, binding := range closure.Bindings {
					if locked[binding] {
						initCheck.add(target.FreeVars[i])
						p.checkSetupOrigins(target, target.FreeVars[i])
					}
				}
			} else if locked[common.Value] && target == nil {
				p.c.fail(ins.Pos(), "cannot verify concurrent borrow: earlier indirect call may retain a mutable alias")
				return
			}
			for i, arg := range common.Args {
				if !locked[arg] {
					continue
				}
				if i == 0 && cacheMethod(target) {
					continue
				}
				if target == nil || len(target.Blocks) == 0 || i >= len(target.Params) {
					if p.checkedParam(common, i) {
						continue
					}
					p.c.fail(ins.Pos(), "cannot verify concurrent borrow: earlier call may retain a mutable alias")
					return
				}
				initCheck.add(target.Params[i])
				p.checkSetupOrigins(target, target.Params[i])
			}
		}
	}
	initCheck.run()
}

// Inspect the storage a setup helper hands back to the builder. A factory can
// keep an alias without receiving any arguments. Setup may still initialize
// the object until the builder publishes it.
func (p *publicationCheck) checkSetupOrigins(f *ssa.Function, root ssa.Value) {
	for _, ret := range p.returns[f] {
		if root != nil {
			p.enqueue(publication{site: ret, root: root, setupOnly: true})
		} else {
			for _, v := range ret.Results {
				p.enqueue(publication{site: ret, root: v, setupOnly: true})
			}
		}
	}
}

// An indirect guest call can publish an argument too. Where dispatch is
// unknown, conservatively include matching targets at that call site.
func (p *publicationCheck) possibleCallers(f *ssa.Function) []ssa.CallInstruction {
	calls := slices.Clone(p.callers[f])
	for _, call := range p.indirect {
		common := call.Common()
		if common.IsInvoke() {
			if f.Signature.Recv() == nil || f.Name() != common.Method.Name() {
				continue
			}
		} else if f.Signature.Recv() != nil {
			continue
		}
		if types.Identical(withoutReceiver(common.Signature()), withoutReceiver(f.Signature)) {
			calls = append(calls, call)
		}
	}
	return calls
}
