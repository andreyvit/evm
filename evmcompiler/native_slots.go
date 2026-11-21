package evmcompiler

import (
	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/nativebind"
	"github.com/andreyvit/evm/values"
	"reflect"
)

// Slice and map conversions preserve their backing storage. Their underlying
// type selects the same layout even when only a defined type is registered.
type nativeCollectionKey struct{ key, elem *values.Type }

func (e *typeEnvironment) collectNativeSlots(packages []evm.Package) {
	// Hand-written bindings may declare the owner layout only. Recover each
	// ordinary slot's type before assigning pointer register banks.
	e.collectionLayouts = make(map[nativeCollectionKey]*values.NativeLayout)
	seenSlots := make(map[*values.Shape]bool)
	var slots func(*values.Shape, reflect.Type)
	slots = func(s *values.Shape, typ reflect.Type) {
		if s == nil || seenSlots[s] {
			return
		}
		layout := e.nativeLayouts[s.Type]
		if layout == nil && typ != nil {
			var fields *nativebind.Struct
			if typ.Kind() == reflect.Struct {
				if s.Kind != values.StructKind {
					return
				}
				names := make([]string, len(s.Fields))
				for i, f := range s.Fields {
					names[i] = f.Name
				}
				fields = nativebind.BindSelectedFields(typ, names, nil, nativebind.Automatic)
			}
			layout = nativebind.Layout(typ, fields, false)
			e.nativeLayouts[s.Type] = layout
		}
		if layout == nil || layout.GoType == nil {
			return
		}
		seenSlots[s] = true
		typ = layout.GoType
		switch s.Kind {
		case values.PointerKind:
			if s.Type.Repr() == values.Pair && typ.Kind() == reflect.Pointer {
				slots(s.Elem, typ.Elem())
			}
		case values.StructKind:
			for i, f := range s.Fields {
				if i >= len(layout.Fields) || layout.Fields[i].Cells || layout.Fields[i].Layout != nil {
					continue
				}
				if sf, ok := typ.FieldByName(f.Name); ok {
					slots(f.Shape, sf.Type)
				}
			}
		case values.SliceKind, values.ArrayKind:
			if typ.Kind() != reflect.Slice && typ.Kind() != reflect.Array {
				return
			}
			key := nativeCollectionKey{elem: s.Elem.Type}
			if e.collectionLayouts[key] == nil && (layout.Slice == nil || layout.Slice.Element == nil) {
				e.collectionLayouts[key] = nativebind.Layout(reflect.SliceOf(typ.Elem()), nil, false)
			}
			slots(s.Elem, typ.Elem())
		case values.MapKind:
			key := nativeCollectionKey{s.Key.Type, s.Elem.Type}
			if e.collectionLayouts[key] == nil && layout.Map != nil {
				plain := nativebind.Layout(reflect.MapOf(typ.Key(), typ.Elem()), nil, false)
				plain.Map.Key, plain.Map.Element = layout.Map.Key, layout.Map.Element
				e.collectionLayouts[key] = plain
			}
			slots(s.Key, typ.Key())
			slots(s.Elem, typ.Elem())
		}
	}
	for _, pkg := range packages {
		for _, d := range pkg.Types {
			slots(d.Shape, nil)
		}
	}
}
