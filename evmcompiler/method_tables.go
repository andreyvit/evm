package evmcompiler

import (
	"cmp"
	"go/types"
	"slices"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/values"
)

// A frozen environment numbers the native methods of the host once and
// builds the method table of each host type whose methods are all native.
// Its modules share both: every module has the same MethodImports, and a
// module that lists one of the types has the same Methods slice. A link
// environment prepared with them binds each once for all of its programs;
// see evm.MethodTables.
//
// A compile still derives the method table of each type it lists and takes
// the shared one only if the two are equal, so the tables here decide what
// is shared and never what a module means.

// prepareMethodTables runs while the environment is frozen. It only reads
// what importing produced.
func (e *typeEnvironment) prepareMethodTables() {
	var bindings []nativeBinding
	for _, b := range e.natives {
		bindings = append(bindings, b)
	}
	for _, b := range e.nativeReceiverMethods {
		bindings = append(bindings, b)
	}
	bindings = slices.DeleteFunc(bindings, func(b nativeBinding) bool {
		return b.key.receiver == nil || b.function == nil || b.function.Instantiate != nil
	})
	slices.SortStableFunc(bindings, func(a, b nativeBinding) int {
		return cmp.Or(cmp.Compare(a.key.pkg, b.key.pkg), cmp.Compare(a.key.receiver.Name(), b.key.receiver.Name()), cmp.Compare(a.key.name, b.key.name))
	})
	e.methodNumbers = make(map[importKey]uint32, len(bindings))
	for _, b := range bindings {
		if _, ok := e.methodNumbers[b.key]; ok || len(e.methodImports) >= evm.MethodImportBase-1 {
			continue
		}
		e.methodNumbers[b.key] = uint32(len(e.methodImports))
		e.methodBindings = append(e.methodBindings, b.function)
		e.methodImports = append(e.methodImports, evm.Import{Package: b.key.pkg, Name: b.key.name, Receiver: b.key.receiver, Signature: evm.Signature{Params: slices.Clone(b.function.Signature.Params), Results: slices.Clone(b.function.Signature.Results)}, Ownership: b.function.Ownership.Clone()})
	}
	// Modules share the list: an append must never write into it.
	e.methodImports = slices.Clip(e.methodImports)

	e.hostMethods = make(map[*values.Shape][]evm.DynamicMethod)
	e.shapes.Iterate(func(t types.Type, shape any) {
		if methods := e.nativeMethodTable(t); len(methods) != 0 {
			e.hostMethods[shape.(*values.Shape)] = slices.Clip(methods)
		}
	})
}

// nativeMethodTable returns the method table a compile derives for a host
// type, or nil if a method needs the module's own code: a wrapper that
// promotes a method or loads its receiver, a source function, or a generic
// binding.
func (e *typeEnvironment) nativeMethodTable(t types.Type) []evm.DynamicMethod {
	if _, ok := t.Underlying().(*types.Interface); ok {
		return nil
	}
	if _, ok := t.(*types.TypeParam); ok {
		return nil
	}
	_, pointer := t.Underlying().(*types.Pointer)
	set := types.NewMethodSet(t)
	var methods []evm.DynamicMethod
	for i := 0; i < set.Len(); i++ {
		sel := set.At(i)
		obj, ok := sel.Obj().(*types.Func)
		if !ok {
			return nil
		}
		// A compile leaves these out of the table.
		if obj.Signature().TypeParams().Len() != 0 {
			continue
		}
		if _, intrinsic := e.recordMethods[obj]; intrinsic {
			continue
		}
		recv := obj.Signature().Recv()
		if recv == nil || len(sel.Index()) != 1 {
			return nil
		}
		if _, pointerMethod := recv.Type().Underlying().(*types.Pointer); pointer && !pointerMethod {
			return nil
		}
		binding, ok := e.natives[obj.Origin()]
		if e.genericNativeDecls[obj.Origin()] {
			receiver := e.shapeOf(types.Default(types.Unalias(recv.Type())))
			if receiver == nil {
				return nil
			}
			binding, ok = e.nativeReceiverMethods[nativeReceiverMethod{obj.Origin(), receiver.Type}]
		}
		if !ok || binding.function == nil || binding.function.Instantiate != nil || binding.key.receiver == nil {
			return nil
		}
		n, ok := e.methodNumbers[binding.key]
		if !ok || e.methodBindings[n] != binding.function {
			return nil
		}
		sig := obj.Signature()
		var s evm.Signature
		for v := range sig.Params().Variables() {
			shape := e.shapeOf(types.Default(types.Unalias(v.Type())))
			if shape == nil {
				return nil
			}
			s.Params = append(s.Params, shape.Type)
		}
		for v := range sig.Results().Variables() {
			shape := e.shapeOf(types.Default(types.Unalias(v.Type())))
			if shape == nil {
				return nil
			}
			s.Results = append(s.Results, shape.Type)
		}
		methods = append(methods, evm.DynamicMethod{ID: obj.Id(), Signature: s, Variadic: sig.Variadic(), Function: evm.MethodImportRef(int(n))})
	}
	return methods
}

func sameMethodTable(a, b []evm.DynamicMethod) bool {
	return slices.EqualFunc(a, b, func(x, y evm.DynamicMethod) bool {
		return x.ID == y.ID && x.Variadic == y.Variadic && x.Function == y.Function && slices.Equal(x.Signature.Params, y.Signature.Params) && slices.Equal(x.Signature.Results, y.Signature.Results)
	})
}

// MethodTables returns the method tables the environment's modules share,
// for evm.LinkOptions. They never change.
func (env *Environment) MethodTables() evm.MethodTables {
	if env.real == nil {
		return evm.MethodTables{}
	}
	tables := evm.MethodTables{Imports: env.real.methodImports}
	for shape, methods := range env.real.hostMethods {
		tables.Types = append(tables.Types, evm.DynamicType{Shape: shape, Methods: methods})
	}
	return tables
}

// shapeNativeSignatures shapes the types in the signatures of the natives
// that host source declares. A compile compares each such signature it
// reaches with the binding, which needs those shapes; shaped while the
// environment is built, they belong to it, and the modules and programs of
// its compiles share them. Otherwise every compile builds its own for the
// types nothing else had shaped, and every module and program keeps them.
//
// A signature that cannot be shaped is left alone: the compile that reaches
// it reports that, as before.
func (e *typeEnvironment) shapeNativeSignatures() {
	var declared []*types.Func
	for obj, binding := range e.natives {
		if obj.Pkg() == nil || binding.function == nil || binding.function.Instantiate != nil {
			continue
		}
		if host := e.packages[obj.Pkg().Path()]; host == nil || !host.HasSource() {
			continue
		}
		if sig := obj.Signature(); sig.TypeParams().Len() != 0 || sig.RecvTypeParams().Len() != 0 {
			continue // Shaped for the type arguments of each use.
		}
		declared = append(declared, obj)
	}
	slices.SortFunc(declared, func(a, b *types.Func) int {
		return cmp.Or(cmp.Compare(a.Pkg().Path(), b.Pkg().Path()), cmp.Compare(a.FullName(), b.FullName()), cmp.Compare(a.Pos(), b.Pos()))
	})
	for _, obj := range declared {
		shaped := len(e.shapeOrder)
		_, err := e.signature(obj.Signature())
		if err == nil && len(e.pendingNativeStorage) == 0 && len(e.pendingSourceChecks) == 0 && len(e.pendingMapKeys) == 0 {
			continue
		}
		// Forget what the failed attempt shaped, finished or not.
		failed := make(map[*values.Shape]bool)
		for _, s := range e.shapeOrder[shaped:] {
			failed[s] = true
		}
		var keys []types.Type
		e.shapes.Iterate(func(t types.Type, s any) {
			if failed[s.(*values.Shape)] {
				keys = append(keys, t)
			}
		})
		for _, t := range keys {
			e.shapes.Delete(t)
		}
		e.shapeOrder = e.shapeOrder[:shaped]
		e.pendingNativeStorage, e.pendingSourceChecks, e.pendingMapKeys = nil, nil, nil
	}
}
