package evmcompiler

import (
	"go/token"
	"go/types"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/values"

	"golang.org/x/tools/go/ssa"
)

const concurrencyPath = "evmruntime/concurrency"

func concurrencyFunction(f *ssa.Function) *types.Func {
	if f == nil {
		return nil
	}
	obj, _ := f.Object().(*types.Func)
	if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != concurrencyPath {
		return nil
	}
	return obj
}

// The registration parameter chooses the roots. All ordinary parameters keep
// their usual mutation and aliasing rules.
func (c *typedCompiler) checkConcurrentCallbacks(functions []*ssa.Function, verified map[*ssa.Function]bool) {
	borrows := publicationCheck{c: c, functions: functions}
	boxed := map[types.Type]bool{}
	for _, f := range functions {
		for _, block := range f.Blocks {
			for _, ins := range block.Instrs {
				if box, ok := ins.(*ssa.MakeInterface); ok && !boxed[box.X.Type()] && c.guestType(box.X.Type()) {
					boxed[box.X.Type()] = true
					c.guestBoxed = append(c.guestBoxed, box.X.Type())
				}
			}
		}
	}
	for _, f := range functions {
		for _, block := range f.Blocks {
			for _, ins := range block.Instrs {
				site, ok := ins.(ssa.CallInstruction)
				if !ok {
					continue
				}
				call := site.Common()
				pkg, key, offset := c.concurrentCallbackTarget(call)
				if pkg == nil {
					continue
				}
				if len(pkg.ConcurrentInterfaceParams[key])+len(pkg.ConcurrentCallbackParams[key]) > 0 {
					if c.concurrentSites == nil {
						c.concurrentSites = map[ssa.Instruction][]int{}
					}
					c.concurrentSites[ins] = append(append([]int(nil), pkg.ConcurrentInterfaceParams[key]...), pkg.ConcurrentCallbackParams[key]...)
				}
				// A callback runs after registration ends, so it cannot use the registrar.
				var registrar types.Type
				if call.IsInvoke() {
					registrar = call.Value.Type()
				} else if offset == 1 && len(call.Args) > 0 {
					registrar = call.Args[0].Type()
				}
				for _, i := range pkg.ConcurrentInterfaceParams[key] {
					if i < 0 || i >= len(call.Args)-offset || i >= call.Signature().Params().Len() {
						c.fail(ins.Pos(), "invalid concurrent interface parameter for %s", key)
						return
					}
					iface, ok := call.Signature().Params().At(i).Type().Underlying().(*types.Interface)
					if !ok || iface.NumMethods() == 0 {
						c.fail(ins.Pos(), "concurrent interface parameter requires a method interface")
						return
					}
					c.verifyConcurrentInterface(call.Args[offset+i], iface, ins, registrar, verified, &borrows, map[ssa.Value]bool{})
					if len(c.errors) > 0 {
						return
					}
				}
				for _, i := range pkg.ConcurrentCallbackParams[key] {
					if i < 0 || i >= len(call.Args)-offset {
						c.fail(ins.Pos(), "invalid concurrent callback parameter for %s", key)
						return
					}
					c.verifyConcurrentCallbackArgument(call.Args[offset+i], ins.Pos(), registrar, verified, &borrows, map[ssa.Value]bool{})
					if len(c.errors) > 0 {
						return
					}
				}
			}
		}
	}
	borrows.run()
}

func (c *typedCompiler) concurrentCallbackTarget(call *ssa.CallCommon) (*evm.Package, string, int) {
	obj := call.Method
	var receiver types.Type
	offset := 0
	if call.IsInvoke() {
		receiver = call.Value.Type()
	} else {
		fn := call.StaticCallee()
		if fn == nil {
			return nil, "", 0
		}
		obj, _ = fn.Object().(*types.Func)
		if obj != nil {
			if recv := obj.Type().(*types.Signature).Recv(); recv != nil {
				receiver = recv.Type()
				offset = 1
			}
		}
	}
	if obj == nil || obj.Pkg() == nil {
		return nil, "", 0
	}
	owner := obj.Pkg()
	key := obj.Name()
	if receiver != nil {
		t := types.Unalias(receiver)
		if p, ok := t.(*types.Pointer); ok {
			t = types.Unalias(p.Elem())
		}
		n, ok := t.(*types.Named)
		if !ok || n.Obj().Pkg() == nil {
			return nil, "", 0
		}
		owner = n.Obj().Pkg()
		key = n.Obj().Name() + "." + key
	}
	return c.env.packages[owner.Path()], key, offset
}

func (c *typedCompiler) verifyConcurrentCallbackArgument(v ssa.Value, pos token.Pos, registrar types.Type, verified map[*ssa.Function]bool, borrows *publicationCheck, seen map[ssa.Value]bool) {
	if seen[v] {
		return
	}
	seen[v] = true
	switch v := v.(type) {
	case *ssa.Const:
		if v.IsNil() {
			return
		}
	case *ssa.Function:
		return
	case *ssa.MakeInterface:
		c.verifyConcurrentCallbackArgument(v.X, pos, registrar, verified, borrows, seen)
		return
	case *ssa.ChangeType:
		c.verifyConcurrentCallbackArgument(v.X, pos, registrar, verified, borrows, seen)
		return
	case *ssa.Phi:
		for _, edge := range v.Edges {
			c.verifyConcurrentCallbackArgument(edge, pos, registrar, verified, borrows, seen)
		}
		return
	case *ssa.MakeClosure:
		for _, root := range v.Bindings {
			borrows.add(v, root)
		}
		target := v.Fn.(*ssa.Function)
		if verified[target] {
			return
		}
		check := sharedCheck{c: c, pos: pos, registrar: registrar, seen: map[ssa.Value]bool{}, aliases: map[types.Type]bool{}}
		for _, root := range target.FreeVars {
			check.add(root)
		}
		check.run()
		verified[target] = len(c.errors) == 0
		return
	}
	c.fail(pos, "concurrent callback parameter requires a statically known function, closure, or bound method")
}

type sharedCheck struct {
	initializing bool // setup helpers may write through the root, but may not publish an alias
	c            *typedCompiler
	pos          token.Pos
	registrar    types.Type // receiver type of the registration call, if it has one
	seen         map[ssa.Value]bool
	pending      []ssa.Value
	aliases      map[types.Type]bool
	returned     map[*ssa.Function]bool        // helpers that return a tracked value
	callers      map[*ssa.Function][]*ssa.Call // calls that passed a tracked value to a helper
}

// Native packages have no guest source. The host declares their functions safe
// to call with shared state, so only guest code needs verification.
func (c *typedCompiler) nativePackage(p *types.Package) bool {
	if p == nil || p == c.pkg.Pkg {
		return false
	}
	std := c.env.standard[p.Path()]
	return std == nil || std.declarationsOnly
}

func (c *typedCompiler) guestType(t types.Type) bool {
	t = types.Unalias(t)
	if p, ok := t.(*types.Pointer); ok {
		t = types.Unalias(p.Elem())
	}
	n, ok := t.(*types.Named)
	return ok && n.Obj().Pkg() != nil && !c.nativePackage(n.Obj().Pkg())
}

// nativeCall reports a call that runs no guest code. The host declares such
// calls safe to make with shared state.
func (c *typedCompiler) nativeCall(call *ssa.CallCommon) bool {
	if call.IsInvoke() {
		iface, ok := call.Value.Type().Underlying().(*types.Interface)
		if !ok || call.Method.Pkg() == c.pkg.Pkg {
			return false
		}
		// A guest type behind the interface still runs guest code.
		for _, t := range c.guestBoxed {
			if types.Implements(t, iface) {
				return false
			}
		}
		return true
	}
	f := call.StaticCallee()
	if f == nil || len(f.Blocks) > 0 {
		return false
	}
	obj := f.Object()
	if obj == nil || obj.Pkg() == nil || obj.Pkg() == c.pkg.Pkg {
		return false
	}
	// A source package declares its primitives without bodies. An instance of
	// a generic source function can have no body yet, so do not trust it.
	return c.nativePackage(obj.Pkg()) || f.Origin() == nil
}

// Go code passes a pointer to a plain variable or field so that the callee can
// write through it. Pointers to structs name objects, and the host owns the
// types that it declares.
func (c *typedCompiler) outParameter(v ssa.Value) bool {
	if box, ok := v.(*ssa.MakeInterface); ok {
		v = box.X
	}
	p, ok := types.Unalias(v.Type()).(*types.Pointer)
	if !ok {
		return false
	}
	elem := types.Unalias(p.Elem())
	if n, ok := elem.(*types.Named); ok && c.nativePackage(n.Obj().Pkg()) {
		return false
	}
	_, object := elem.Underlying().(*types.Struct)
	return !object
}

// A helper that returns a tracked value hands the alias to its guest caller.
func (s *sharedCheck) returnsTo(f *ssa.Function, call *ssa.Call) {
	if s.returned[f] {
		s.add(call)
		return
	}
	if s.callers == nil {
		s.callers = map[*ssa.Function][]*ssa.Call{}
	}
	s.callers[f] = append(s.callers[f], call)
}

// Cache data can contain independent guest values, including flattened structs.
func (e *typeEnvironment) cacheDataType(t types.Type, key bool, memo map[types.Type]bool) bool {
	if valid, ok := memo[t]; ok {
		return valid
	}
	vt := e.typeOf(t)
	if vt == values.TimeType {
		return false
	}
	if decl, ok := e.nativeTypes[vt]; ok {
		s := decl.decl.Shape
		if s.CellStruct != nil || s.Native != nil && s.Native.OpaqueCell != 0 || s.Kind == values.TypedPairKind || s.Kind == values.NativeValueKind {
			return false
		}
	}
	valid := false
	switch u := t.Underlying().(type) {
	case *types.Basic:
		valid = u.Info()&(types.IsBoolean|types.IsInteger|types.IsString) != 0 || !key && u.Info()&types.IsFloat != 0
	case *types.Struct:
		valid = true
		for i := range u.NumFields() {
			if !e.cacheDataType(u.Field(i).Type(), key, memo) {
				valid = false
				break
			}
		}
	case *types.Array:
		valid = e.cacheDataType(u.Elem(), key, memo)
	}
	memo[t] = valid
	return valid
}

// compactStruct reports whether the host keeps t in one cell.
func (e *typeEnvironment) compactStruct(t types.Type) bool {
	d, ok := e.nativeTypes[e.typeOf(t)]
	return ok && d.decl.Shape.CellStruct != nil
}

// Copying a scalar or a struct/array of scalars does not copy a writable alias.
func (s *sharedCheck) sharedAlias(t types.Type) (alias bool) {
	if a, ok := s.aliases[t]; ok {
		return a
	}
	defer func() { s.aliases[t] = alias }()
	if n, ok := types.Unalias(t).(*types.Named); ok && n.Obj().Pkg() != nil && n.Obj().Pkg() != s.c.pkg.Pkg {
		// Native layouts can omit private fields from the visible struct.
		// A compact struct cannot: its two fields are all it has, and they
		// decide below whether a copy can alias anything.
		if _, ok := n.Underlying().(*types.Struct); ok && !s.c.env.compactStruct(n) {
			return true
		}
	}

	switch t := t.Underlying().(type) {
	case *types.Basic:
		return t.Kind() == types.UnsafePointer
	case *types.Array:
		return s.sharedAlias(t.Elem())
	case *types.Tuple:
		for i := range t.Len() {
			if s.sharedAlias(t.At(i).Type()) {
				return true
			}
		}
		return false
	case *types.Struct:
		for i := range t.NumFields() {
			if s.sharedAlias(t.Field(i).Type()) {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func (s *sharedCheck) add(v ssa.Value) {
	if !s.seen[v] && s.sharedAlias(v.Type()) {
		s.seen[v] = true
		s.pending = append(s.pending, v)
	}
}

func (s *sharedCheck) run() {
	for len(s.pending) > 0 {
		i := len(s.pending) - 1
		v := s.pending[i]
		s.pending = s.pending[:i]
		if s.registrar != nil && types.Identical(v.Type(), s.registrar) {
			pos := s.pos
			if refs := v.Referrers(); refs != nil {
				for _, use := range *refs {
					if use.Pos().IsValid() {
						pos = use.Pos()
						break
					}
				}
			}
			s.c.fail(pos, "concurrent callback uses the %s it is registered on; use the callback's own argument", types.TypeString(s.registrar, func(p *types.Package) string { return p.Name() }))
			return
		}
		if refs := v.Referrers(); refs != nil {
			if !s.c.chargeConcurrentCheck(s.pos, len(*refs)+1) {
				return
			}
			for _, use := range *refs {
				if !s.safeUse(v, use) {
					pos := use.Pos()
					if !pos.IsValid() {
						pos = s.pos
					}
					if s.initializing {
						s.c.fail(pos, "cannot verify concurrent borrow: setup helper may retain an alias in %s", use)
						return
					}
					message := "concurrent callback: cannot prove read-only use of receiver or capture in %s"
					switch op := use.(type) {
					case *ssa.Store:
						if op.Addr == v {
							message = "concurrent callback writes to shared receiver or captured state in %s"
						}
					case *ssa.MapUpdate:
						if op.Map == v {
							message = "concurrent callback writes to shared map in %s"
						}
					}
					s.c.fail(pos, message, use)
					return
				}
			}
		}
	}
}

func (c *typedCompiler) chargeConcurrentCheck(pos token.Pos, work int) bool {
	if c.overBudget {
		return false
	}
	c.budget -= work
	if c.budget < 0 {
		c.overBudget = true
		c.fail(pos, "concurrent borrow verification exceeds the compile budget")
		return false
	}
	return true
}

func (s *sharedCheck) safeUse(v ssa.Value, use ssa.Instruction) bool {
	switch op := use.(type) {
	case *ssa.DebugRef, *ssa.BinOp:
		return true
	case *ssa.UnOp:
		if op.Op != token.MUL {
			return false
		}
		s.add(op)
		return true
	case *ssa.FieldAddr, *ssa.IndexAddr, *ssa.Index, *ssa.Field, *ssa.Lookup, *ssa.Slice, *ssa.Range, *ssa.Next, *ssa.Phi, *ssa.Extract, *ssa.MakeInterface, *ssa.ChangeInterface, *ssa.ChangeType, *ssa.TypeAssert:
		s.add(op.(ssa.Value))
		return true
	case *ssa.Convert:
		// Conversions can preserve slice backing storage.
		s.add(op)
		return true
	case ssa.CallInstruction:
		call := op.Common()
		if builtin, ok := call.Value.(*ssa.Builtin); ok {
			switch builtin.Name() {
			case "len", "cap":
				return true
			case "delete", "clear":
				return s.initializing && call.Args[0] == v
			}
			return false
		}
		if s.c.nativeCall(call) {
			return !s.c.outParameter(v)
		}
		target := call.StaticCallee()
		if target == nil || call.Value == v {
			return false
		}
		for i, arg := range call.Args {
			if arg != v {
				continue
			}
			if i == 0 && cacheMethod(target) {
				continue
			}
			if len(target.Blocks) == 0 || i >= len(target.Params) {
				return false
			}
			s.add(target.Params[i])
			if result, ok := op.(*ssa.Call); ok {
				if s.initializing {
					s.add(result)
				} else {
					s.returnsTo(target, result)
				}
			}
		}
		return true
	case *ssa.MapUpdate:
		return s.initializing && op.Map == v && op.Value != v && op.Key != v
	case *ssa.Return:
		// The callback itself returns to the host. A helper returns to a guest
		// call, which then holds the alias.
		if f := op.Parent(); !s.initializing && !s.returned[f] {
			if s.returned == nil {
				s.returned = map[*ssa.Function]bool{}
			}
			s.returned[f] = true
			for _, call := range s.callers[f] {
				s.add(call)
			}
		}
		return true
	case *ssa.Store:
		if s.initializing && op.Addr == v && op.Val != v {
			return true
		}
		box, ok := op.Addr.(*ssa.Alloc)
		return ok && v == op.Val && v != box && s.localAlias(box)
	case *ssa.MakeClosure:
		// Analyze the body only if every use calls it synchronously or hands it
		// to native code. A closure that escapes to guest storage could publish
		// another alias.
		for _, ref := range *op.Referrers() {
			switch r := ref.(type) {
			case *ssa.DebugRef:
				continue
			case *ssa.Call:
				if r.Call.Value == op {
					if !s.initializing {
						s.returnsTo(op.Fn.(*ssa.Function), r)
					}
					continue
				}
				if s.c.nativeCall(&r.Call) {
					continue
				}
				if f := r.Call.StaticCallee(); cacheMethod(f) && concurrencyFunction(f).Name() == "GetOrLoad" && r.Call.Args[len(r.Call.Args)-1] == op {
					continue
				}
			}
			return false
		}
		for i, b := range op.Bindings {
			if b == v {
				s.add(op.Fn.(*ssa.Function).FreeVars[i])
			}
		}
		return true
	default:
		return false
	}
}

// SSA boxes a receiver parameter when a loader closes over it. The box
// belongs to this invocation. Follow reads of its contents, and require every
// closure holding the box itself to finish synchronously.
func (s *sharedCheck) localAlias(box *ssa.Alloc) bool {
	for _, use := range *box.Referrers() {
		switch r := use.(type) {
		case *ssa.DebugRef:
			continue
		case *ssa.Store:
			if r.Addr != box || r.Val == box {
				return false
			}
		case *ssa.UnOp:
			if r.Op != token.MUL {
				return false
			}
			s.add(r)
		case *ssa.MakeClosure:
			if !s.safeUse(box, r) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func cacheMethod(f *ssa.Function) bool {
	obj := concurrencyFunction(f)
	if obj == nil {
		return false
	}
	sig := obj.Type().(*types.Signature)
	if sig.Recv() == nil {
		return false
	}
	ptr, ok := sig.Recv().Type().(*types.Pointer)
	if !ok {
		return false
	}
	named, ok := ptr.Elem().(*types.Named)
	if !ok || named.Obj().Name() != "Cache" {
		return false
	}
	switch obj.Name() {
	case "Get", "Set", "GetOrLoad", "InvalidateIfCurrent":
		return true
	}
	return false
}

// Check the concrete methods visible through the declared interface. A method
// outside the interface can still be used during ordinary initialization.
func (c *typedCompiler) verifyConcurrentInterface(v ssa.Value, iface *types.Interface, site ssa.Instruction, registrar types.Type, verified map[*ssa.Function]bool, borrows *publicationCheck, seen map[ssa.Value]bool) {
	if seen[v] {
		return
	}
	seen[v] = true
	switch x := v.(type) {
	case *ssa.Const:
		if x.IsNil() {
			return
		}
	case *ssa.Phi:
		for _, edge := range x.Edges {
			c.verifyConcurrentInterface(edge, iface, site, registrar, verified, borrows, seen)
		}
		return
	case *ssa.ChangeInterface:
		c.verifyConcurrentInterface(x.X, iface, site, registrar, verified, borrows, seen)
		return
	case *ssa.MakeInterface:
		borrows.add(site, x.X)
		set := c.pkg.Prog.MethodSets.MethodSet(x.X.Type())
		for i := 0; i < iface.NumMethods(); i++ {
			method := iface.Method(i)
			sel := set.Lookup(method.Pkg(), method.Name())
			if sel == nil {
				c.fail(site.Pos(), "missing concurrent interface method %s", method.Name())
				return
			}
			fn := c.methodValue(c.pkg.Prog, sel)
			if fn == nil || len(fn.Blocks) == 0 || len(fn.Params) == 0 || method.Signature().Variadic() {
				c.fail(site.Pos(), "cannot verify concurrent interface method %s", method.Name())
				return
			}
			if !verified[fn] {
				check := sharedCheck{c: c, pos: site.Pos(), registrar: registrar, seen: map[ssa.Value]bool{}, aliases: map[types.Type]bool{}}
				check.add(fn.Params[0])
				check.run()
				if len(c.errors) > 0 {
					return
				}
				verified[fn] = true
			}
		}
		return
	}
	c.fail(site.Pos(), "concurrent interface requires a statically known concrete implementation")
}
