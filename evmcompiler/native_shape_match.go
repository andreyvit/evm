package evmcompiler

import (
	"go/types"

	"github.com/andreyvit/evm/values"
)

// Resolve a declared anonymous identity before deriving storage from its type.
// Match structurally first: importing every candidate would pull unrelated
// host packages into a script merely because they expose a map or slice.
func (e *typeEnvironment) resolveDeclaredShape(t types.Type) error {
	if e.typeOf(t) != nil {
		return nil
	}
	if _, named := t.(*types.Named); named {
		return nil
	}
	for _, typ := range e.unnamedNativeTypes {
		decl := e.nativeTypes[typ]
		if decl.decl.Name != "" || decl.decl.SourceName != "" || e.resolving[typ] {
			continue
		}
		if !e.matchesDeclaredShape(t, decl.decl.Shape) {
			continue
		}
		return e.bindNative(typ, t)
	}
	return nil
}

func (e *typeEnvironment) matchesDeclaredShape(t types.Type, s *values.Shape) bool {
	t = types.Unalias(t)
	if known := e.goOf(s.Type); known != nil {
		return types.Identical(t, known)
	}
	if d, ok := e.nativeTypes[s.Type]; ok && d.decl.Name != "" {
		named, ok := t.(*types.Named)
		return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == d.pkg && named.Obj().Name() == d.decl.Name
	}
	switch t := t.(type) {
	case *types.Pointer:
		return s.Kind == values.PointerKind && e.matchesDeclaredShape(t.Elem(), s.Elem)
	case *types.Slice:
		return s.Kind == values.SliceKind && e.matchesDeclaredShape(t.Elem(), s.Elem)
	case *types.Array:
		return s.Kind == values.ArrayKind && t.Len() == int64(s.Len) && e.matchesDeclaredShape(t.Elem(), s.Elem)
	case *types.Map:
		return s.Kind == values.MapKind && e.matchesDeclaredShape(t.Key(), s.Key) && e.matchesDeclaredShape(t.Elem(), s.Elem)
	case *types.Signature:
		if s.Kind != values.FunctionKind || t.Variadic() != s.Variadic || t.Params().Len() != len(s.Params) || t.Results().Len() != len(s.Results) {
			return false
		}
		for i, f := range s.Params {
			if !e.matchesDeclaredShape(t.Params().At(i).Type(), f) {
				return false
			}
		}
		for i, f := range s.Results {
			if !e.matchesDeclaredShape(t.Results().At(i).Type(), f) {
				return false
			}
		}
		return true
	}
	return false
}
