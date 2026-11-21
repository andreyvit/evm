package evmcompiler

import (
	"fmt"
	"go/types"
	"math"
	"reflect"
	"unsafe"

	"github.com/andreyvit/evm/nativebind"
	"github.com/andreyvit/evm/values"
)

// Native fields need stable Go addresses even inside guest-only aggregates.
// Keep register snapshots as VM cells; only addressable storage uses this
// physical layout. Other fields remain cells inside the allocated Go object.
func (e *typeEnvironment) requiresNativeStorage(t types.Type) bool {
	t = types.Unalias(t)
	switch u := t.Underlying().(type) {
	case *types.Struct:
		if vt := e.typeOf(t); e.nativeLayout(vt) != nil {
			return true
		}
		for i := 0; i < u.NumFields(); i++ {
			if e.requiresNativeStorage(u.Field(i).Type()) {
				return true
			}
		}
	case *types.Array:
		if vt := e.typeOf(t); e.nativeLayout(vt) != nil {
			return true
		}
		return e.requiresNativeStorage(u.Elem())
	}
	return false
}

func (e *typeEnvironment) physicalType(s *values.Shape) reflect.Type {
	if s.Native != nil && s.Native.GoType != nil {
		return s.Native.GoType
	}
	return e.syntheticType(s)
}

type nativeStorageShape struct {
	typ   types.Type
	shape *values.Shape
}

func (e *typeEnvironment) addSyntheticStorage(t types.Type, s *values.Shape) error {
	required := e.requiresNativeStorage(t)
	if slice, ok := t.Underlying().(*types.Slice); ok {
		required = e.requiresNativeStorage(slice.Elem())
	}
	if s.Native == nil && required {
		e.pendingNativeStorage = append(e.pendingNativeStorage, nativeStorageShape{t, s})
	}
	// A recursive reference can discover a wrapper before its inline field has
	// finished shaping. Wait for that field, then derive both cell offsets and
	// physical storage. Scan only pending native-storage shapes.
	for i := 0; i < len(e.pendingNativeStorage); {
		p := e.pendingNativeStorage[i]
		ready, err := e.finishSyntheticStorage(p.typ, p.shape)
		if err != nil {
			return err
		}
		if !ready {
			i++
			continue
		}
		e.pendingNativeStorage = append(e.pendingNativeStorage[:i], e.pendingNativeStorage[i+1:]...)
		i = 0
	}
	return nil
}

func (e *typeEnvironment) finishSyntheticStorage(t types.Type, s *values.Shape) (bool, error) {
	if s.Kind == values.StructKind {
		source := t.Underlying().(*types.Struct)
		for i, f := range s.Fields {
			if e.shaping[f.Shape] || (e.requiresNativeStorage(source.Field(i).Type()) && f.Shape.Native == nil) {
				return false, nil
			}
		}
		fields := make([]reflect.StructField, len(s.Fields))
		cellFields := make([]bool, len(s.Fields))
		s.Cells = 0
		for i := range s.Fields {
			f := &s.Fields[i]
			f.Offset = s.Cells
			s.Cells += f.Shape.Cells
			if s.Cells > math.MaxUint32 {
				return false, fmt.Errorf("type %s exceeds storage limit", t)
			}
			typ := e.physicalType(f.Shape)
			if typ == nil || !f.Shape.NativeStorage() {
				typ = reflect.ArrayOf(int(f.Shape.Cells), reflect.TypeFor[values.Value]())
				cellFields[i] = true
			}
			fields[i] = reflect.StructField{Name: fmt.Sprintf("F%d", i), Type: typ}
		}
		typ := reflect.StructOf(fields)
		layout := nativebind.Layout(typ, nil, false)
		// Physical guest storage must not introduce a native-language identity.
		layout.GoType = nil
		layout.Compare = nil
		for i, f := range s.Fields {
			offset := typ.Field(i).Offset
			nf := values.NativeField{Offset: offset, Cells: cellFields[i]}
			if nf.Cells {
				nf.Get = func(p unsafe.Pointer) values.Value { return f.Shape.Stored(unsafe.Add(p, offset)) }
				nf.Set = func(p unsafe.Pointer, v values.Value) { f.Shape.Store(unsafe.Add(p, offset), v) }
			} else {
				nf.Get = func(p unsafe.Pointer) values.Value { return f.Shape.Native.Load(unsafe.Add(p, offset)) }
				nf.Set = func(p unsafe.Pointer, v values.Value) { f.Shape.Native.Store(unsafe.Add(p, offset), v) }
			}
			layout.Fields = append(layout.Fields, nf)
		}
		e.setSyntheticStorage(s, typ, layout)
		return true, nil
	}
	elem := s.Elem
	if e.shaping[elem] || !elem.NativeStorage() {
		return false, nil
	}
	typ := e.physicalType(elem)
	if typ == nil {
		return false, nil
	}
	if s.Kind == values.ArrayKind {
		if s.Len > math.MaxUint32 || (typ.Size() != 0 && s.Len > uint64(math.MaxInt)/uint64(typ.Size())) || (elem.Cells != 0 && s.Len > math.MaxUint32/elem.Cells) {
			return false, fmt.Errorf("native array %s exceeds VM capacity", s.Type.Name())
		}
		s.Cells = s.Len * elem.Cells
		typ = reflect.ArrayOf(int(s.Len), typ)
	} else {
		typ = reflect.SliceOf(typ)
	}
	layout := nativebind.Layout(typ, nil, false)
	layout.GoType = nil
	e.setSyntheticStorage(s, typ, layout)
	return true, nil
}

func (e *typeEnvironment) setSyntheticStorage(s *values.Shape, t reflect.Type, layout *values.NativeLayout) {
	if e.syntheticTypes == nil {
		e.syntheticTypes = map[*values.Shape]reflect.Type{}
	}
	e.syntheticTypes[s] = t
	s.Native = layout
	e.nativeLayouts[s.Type] = layout
}

// Addressable compact structs retain independent field slots. Value copies pack
// those slots; field pointers continue to point at ordinary stable storage.
func (e *typeEnvironment) cellStructStorage(t types.Type, s *values.Shape) error {
	s.Kind = values.StructKind
	_, err := e.finishSyntheticStorage(t, s)
	if err != nil {
		return err
	}
	cell := s.CellStruct
	layout := s.Native
	layout.Load = func(p unsafe.Pointer) values.Value {
		var fields [2]values.Value
		for i := range s.Fields {
			fields[i] = layout.Fields[i].Get(p)
		}
		return cell.Pack(fields)
	}
	layout.Store = func(p unsafe.Pointer, v values.Value) {
		for i := range s.Fields {
			layout.Fields[i].Set(p, cell.Get(v, i))
		}
	}
	layout.Codec = &values.NativeValueCodec{
		Decode: func(v values.Value) (values.Value, error) { return v, nil },
		Encode: func(v values.Value, _ func(uint64) error) (values.Value, error) { return v, nil },
	}
	s.Kind = values.TypedPairKind
	s.Cells = 1
	return nil
}
