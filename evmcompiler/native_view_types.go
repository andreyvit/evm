package evmcompiler

import (
	"go/types"
	"reflect"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/values"
)

// Native model lists retain their backing storage. Their element addresses
// need a tag distinguishing native storage from VM cells. Collect possible
// element types before importing packages or building any cached shapes.
func (e *typeEnvironment) collectNativeViewLeaves() {
	e.nativeViewLeaves = make(map[*values.Type]bool)
	var walk func(kvo.AnyType, bool)
	walk = func(k kvo.AnyType, element bool) {
		if k.Model() != nil { // Model pointers already carry representation tags.
			return
		}
		switch k := k.(type) {
		case *kvo.ListType:
			walk(k.ItemType(), true)
			return
		case *kvo.MapType:
			walk(k.ItemType(), element)
			return
		case *kvo.ScalarDataType:
			if k.NativeType().Kind() == reflect.Slice {
				e.nativeViewLeaves[values.Uint8] = true
				return
			}
		}
		if !element {
			return
		}
		if scalar := evm.RecordScalarType(k); scalar != nil {
			e.nativeViewLeaves[scalar] = true
		}
		if native, ok := k.(interface{ NativeType() reflect.Type }); ok {
			for typ, decl := range e.nativeTypes {
				s := decl.decl.Shape
				if decl.decl.ScalarBase != nil && s.Native != nil && s.Native.GoType == native.NativeType() {
					e.nativeViewLeaves[typ] = true
				}
			}
			for _, pkg := range e.packages {
				for _, shared := range pkg.SharedTypes {
					if shared.Native == native.NativeType() {
						e.nativeViewLeaves[shared.Type] = true
					}
				}
			}
		}
	}
	for _, pkg := range e.packages {
		// Generic instantiations can introduce a borrow source at compile time.
		borrowBytes := func(sig evm.Signature, effects *evm.OwnershipEffects) bool {
			if effects != nil {
				for _, src := range effects.Borrowed {
					if src.Result >= 0 && src.Result < len(sig.Results) && sig.Results[src.Result] == evm.ByteSliceType {
						return true
					}
				}
			}
			return false
		}
		for _, f := range pkg.Functions {
			if borrowBytes(f.Signature, f.Ownership) || f.Instantiate != nil && e.nativeTypes[evm.ByteSliceType].decl.Shape != nil {
				e.nativeViewLeaves[values.Uint8] = true
			}
		}
		for _, m := range pkg.Methods {
			if borrowBytes(m.Signature, m.Ownership) || m.Instantiate != nil && e.nativeTypes[evm.ByteSliceType].decl.Shape != nil {
				e.nativeViewLeaves[values.Uint8] = true
			}
		}
		for _, row := range pkg.Records {
			if row.Mutable {
				for _, prop := range row.Model.AllProps() {
					walk(prop.AnyType(), false)
				}
			}
		}
	}
}

func (e *typeEnvironment) mayViewNative(t types.Type) bool {
	var seen elementChain
	for t = types.Unalias(t); seen.enter(t); t = types.Unalias(t) {
		if v := e.typeOf(t); e.nativeViewLeaves[v] {
			return true
		}
		switch a := t.Underlying().(type) {
		case *types.Slice:
			t = a.Elem()
		case *types.Array:
			t = a.Elem()
		default:
			return false
		}
	}
	return false
}
