package evmbind

import "go/types"

func functionHandleAlias(t types.Type) bool {
	a, ok := t.(*types.Alias)
	return ok && a.Obj().Pkg() != nil && a.Obj().Pkg().Path() == "github.com/andreyvit/evm" && (a.Obj().Name() == "FunctionOf" || a.Obj().Name() == "NamedFunctionOf")
}

func aliasesFunctionHandle(t types.Type) bool {
	for {
		switch x := t.(type) {
		case *types.Alias:
			if functionHandleAlias(x) {
				return true
			}
			t = x.Rhs()
		case *types.Pointer:
			t = x.Elem()
		default:
			return false
		}
	}
}

// Binding identities must not depend on which alias appeared first, including
// inside pointer and function signatures. Keep EVM's signature-carrying handle
// aliases intact: their Go representation deliberately erases that signature.
func canonicalAliasType(t types.Type) types.Type {
	if types.Identical(t, types.Universe.Lookup("any").Type()) {
		return types.Universe.Lookup("any").Type()
	}
	switch x := t.(type) {
	case *types.Alias:
		if functionHandleAlias(x) {
			return x
		}
		if aliasesFunctionHandle(x.Rhs()) {
			fail("alias %s of an EVM function handle is not supported", x.Obj().Name())
		}
		return canonicalAliasType(types.Unalias(x))
	case *types.Pointer:
		elem := canonicalAliasType(x.Elem())
		if elem != x.Elem() {
			return types.NewPointer(elem)
		}
	case *types.Slice:
		elem := canonicalAliasType(x.Elem())
		if elem != x.Elem() {
			return types.NewSlice(elem)
		}
	case *types.Array:
		elem := canonicalAliasType(x.Elem())
		if elem != x.Elem() {
			return types.NewArray(elem, x.Len())
		}
	case *types.Map:
		key, elem := canonicalAliasType(x.Key()), canonicalAliasType(x.Elem())
		if key != x.Key() || elem != x.Elem() {
			return types.NewMap(key, elem)
		}
	case *types.Signature:
		params, results := canonicalAliasTuple(x.Params()), canonicalAliasTuple(x.Results())
		if params != x.Params() || results != x.Results() {
			var recvParams, paramsTypes []*types.TypeParam
			for i := 0; i < x.RecvTypeParams().Len(); i++ {
				recvParams = append(recvParams, x.RecvTypeParams().At(i))
			}
			for i := 0; i < x.TypeParams().Len(); i++ {
				paramsTypes = append(paramsTypes, x.TypeParams().At(i))
			}
			return types.NewSignatureType(x.Recv(), recvParams, paramsTypes, params, results, x.Variadic())
		}
	case *types.Named:
		if x.TypeArgs().Len() == 0 {
			return x
		}
		args := make([]types.Type, x.TypeArgs().Len())
		changed := false
		for i := range args {
			a := x.TypeArgs().At(i)
			args[i] = canonicalAliasType(a)
			changed = changed || args[i] != a
		}
		if changed {
			result, err := types.Instantiate(nil, x.Origin(), args, true)
			if err != nil {
				fail("invalid native alias instantiation: %v", err)
			}
			return result
		}
	}
	return t
}

func canonicalAliasTuple(t *types.Tuple) *types.Tuple {
	if t == nil {
		return nil
	}
	var vars []*types.Var
	for i := 0; i < t.Len(); i++ {
		v := t.At(i)
		typ := canonicalAliasType(v.Type())
		if typ != v.Type() && vars == nil {
			vars = make([]*types.Var, t.Len())
			for j := 0; j < i; j++ {
				vars[j] = t.At(j)
			}
		}
		if vars != nil {
			vars[i] = types.NewVar(v.Pos(), v.Pkg(), v.Name(), typ)
		}
	}
	if vars == nil {
		return t
	}
	return types.NewTuple(vars...)
}
