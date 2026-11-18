package evm

import (
	"fmt"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/nativebind"
	"github.com/andreyvit/evm/values"
	"reflect"
	"unsafe"
)

// NativeModelPointerLayout exposes native pointer slots through the model's
// row API. Stores require matching native backing; decoding is explicit.
func NativeModelPointerLayout(model *kvo.Model, typ reflect.Type) *values.NativeLayout {
	binding := kvo.Bind(typ, model)
	pointer := reflect.PointerTo(typ)
	layout := nativebind.Layout(pointer, nil, true)
	layout.Codec = &values.NativeValueCodec{
		Decode: func(v values.Value) (values.Value, error) {
			ptr := reflect.Zero(pointer)
			if v.P != nil {
				ptr = reflect.NewAt(typ, v.P)
			}
			return binding.Record(ptr).Value(), nil
		},
		Encode: func(v values.Value, _ func(uint64) error) (values.Value, error) {
			record, err := RecordValue(v, model)
			if err != nil {
				return values.Value{}, err
			}
			if record.Missing() {
				return values.Value{}, nil
			}
			if ptr := record.NativePointer(); ptr != nil && reflect.TypeOf(ptr) == pointer {
				return values.Value{P: reflect.ValueOf(ptr).UnsafePointer()}, nil
			}
			return values.Value{}, fmt.Errorf("model row requires explicit conversion to %s", pointer)
		},
	}
	return layout
}

// NativeModelValueLayout uses ordinary native struct snapshots for row values.
func NativeModelValueLayout(model *kvo.Model, typ reflect.Type) *values.NativeLayout {
	binding := kvo.Bind(typ, model)
	layout := nativebind.Layout(typ, nil, false)
	layout.OpaqueCell = 1
	layout.Compare.Steps = append(layout.Compare.Steps, values.NativeCompareStep{Field: 0})
	layout.Fields = []values.NativeField{{Cells: true, Get: func(unsafe.Pointer) values.Value { return values.Value{} }, Set: func(unsafe.Pointer, values.Value) {}}}
	layout.Address = &values.NativeAddressCodec{
		Encode: func(p unsafe.Pointer) values.Value { return binding.Record(reflect.NewAt(typ, p)).Value() },
		Decode: func(v values.Value) (unsafe.Pointer, error) {
			r, err := RecordValue(v, model)
			if err != nil {
				return nil, err
			}
			ptr := reflect.ValueOf(r.NativePointer())
			if !ptr.IsValid() || ptr.Type() != reflect.PointerTo(typ) {
				return nil, fmt.Errorf("model row requires explicit conversion to %s", typ)
			}
			return ptr.UnsafePointer(), nil
		},
	}
	return layout
}

// ModelValueShape describes the opaque value cells used by native bindings.
// The compiler resolves the row identity to its own model property layout.
func ModelValueShape(row *RecordType) *values.Shape {
	s := &values.Shape{Type: row.Value, Kind: values.StructKind, Cells: 1}
	if row.NativeAllocation != nil {
		s.Native = nativebind.Layout(row.NativeAllocation, nil, false)
		s.Native.OpaqueCell = 1
	}
	return s
}
