package evm

import (
	"fmt"
	"reflect"
	"slices"

	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/nativebind"
	"github.com/andreyvit/evm/values"
)

type mutableFieldKey struct {
	shape  *values.Shape
	native reflect.Type
}

// Physical field layouts are immutable, program-local metadata. Actual row
// pointers and collection values never enter this cache.
func (p *Program) mutableFieldShape(s *values.Shape, t reflect.Type, k kvo.AnyType) (*values.Shape, error) {
	key := mutableFieldKey{s, t}
	if found, ok := p.mutableFields.Load(key); ok {
		return found.(*values.Shape), nil
	}
	layout, err := mutableFieldLayout(s, t, k)
	if err != nil {
		return nil, err
	}
	result := *s
	result.Native = layout
	actual, _ := p.mutableFields.LoadOrStore(key, &result)
	return actual.(*values.Shape), nil
}
func mutableFieldLayout(s *values.Shape, t reflect.Type, k kvo.AnyType) (*values.NativeLayout, error) {
	if model := k.Model(); model != nil {
		entity, ok := model.Type().(*kvo.EntityType)
		if !ok || t.Kind() != reflect.Pointer || !slices.Contains(entity.NativeTypes(), t.Elem()) {
			return nil, fmt.Errorf("native row collection requires modeled pointer elements, got %s", t)
		}
		layout := nativebind.Layout(t, nil, false)
		binding := kvo.Bind(t.Elem(), model)
		layout.Codec = &values.NativeValueCodec{
			Decode: func(v values.Value) (values.Value, error) {
				return NativeMutableRecordValue(model, binding.Record(reflect.NewAt(t.Elem(), v.P)))
			},
			Encode: func(v values.Value, _ func(uint64) error) (values.Value, error) {
				r, err := NativeMutableRecord(v, model)
				if err != nil {
					return values.Value{}, err
				}
				if r.Missing() {
					return values.Value{}, nil
				}
				ptr := r.NativePointer()
				if reflect.TypeOf(ptr) != t {
					return values.Value{}, fmt.Errorf("row requires native backing %s", t)
				}
				return values.Value{P: reflect.ValueOf(ptr).UnsafePointer()}, nil
			},
		}
		return layout, nil
	}
	if s.Kind == values.SliceKind && t.Kind() == reflect.Slice {
		element := k.ItemType()
		if _, ok := k.(*kvo.ScalarDataType); ok {
			element = kvo.TUint8
		}
		if element == nil {
			return nil, fmt.Errorf("unsupported native slice property %s", k.Name())
		}
		layout := nativebind.Layout(t, nil, false)
		child, err := mutableFieldLayout(s.Elem, t.Elem(), element)
		if err != nil {
			return nil, err
		}
		layout.Slice.Element = child
		return layout, nil
	}
	if s.Kind == values.MapKind {
		if s.Native != nil && s.Native.GoType == t && s.Native.Map != nil {
			return s.Native, nil
		}
		return nil, fmt.Errorf("native model map %s needs an explicitly bound native map layout", t)
	}
	// Match the real field kind and width; do not reinterpret narrower Go cells.
	valid := s.Kind == values.StringKind && t.Kind() == reflect.String || s.Kind == values.BoolKind && t.Kind() == reflect.Bool
	valid = valid || s.Kind == values.IntKind && t.Kind() >= reflect.Int && t.Kind() <= reflect.Int64 && s.Bits == uint8(t.Size()*8)
	valid = valid || s.Kind == values.UintKind && t.Kind() >= reflect.Uint && t.Kind() <= reflect.Uint64 && s.Bits == uint8(t.Size()*8)
	valid = valid || s.Kind == values.FloatKind && (t.Kind() == reflect.Float32 || t.Kind() == reflect.Float64) && s.Bits == uint8(t.Size()*8)
	if !valid {
		return nil, fmt.Errorf("native property layout %s does not match %s", t, s.Type.Name())
	}
	return nativebind.Layout(t, nil, false), nil
}
func (vm *VM) nativeMutableField(f frame, op *ExtendedOp, r kvo.Record, b *executionBudget) error {
	if r.Model() != op.Record.Model || r.Missing() {
		return fmt.Errorf("invalid native %s record", op.Record.Name)
	}
	if op.childModel != nil {
		child := r.Child(op.step)
		v, err := NativeMutableRecordValue(op.childModel, child)
		if err == nil {
			vm.write(f, op.Out[0], v)
		}
		return err
	}
	k := op.Record.Model.MustPropByCode(op.Index).AnyType()
	if op.Shape.Kind != values.SliceKind && op.Shape.Kind != values.MapKind && op.Shape.Kind != values.InterfaceKind {
		if op.Kind == ExtendedSetKey {
			return r.TrySetNativeValue(op.step, vm.read(f, op.In[1]))
		}
		vm.write(f, op.Out[0], r.GetValue(op.Index))
		return nil
	}
	typ, ptr, err := r.NativeField(op.Index)
	if err != nil {
		return err
	}
	shape, err := vm.program.mutableFieldShape(op.Shape, typ, k)
	if err != nil {
		return err
	}
	if op.Kind == ExtendedSetKey {
		return storeNative(&vm.call, shape, ptr, vm.read(f, op.In[1]), &b.allocated, b.limits)
	}
	value, err := loadNative(&vm.call, shape, ptr, &b.allocated, b.limits)
	if err == nil {
		vm.write(f, op.Out[0], value)
	}
	return err
}
