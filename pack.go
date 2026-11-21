package evm

import (
	"fmt"
	"math"
	"unsafe"

	"github.com/andreyvit/evm/values"
)

// PackAggregate builds a guest struct, array, or slice from values in declaration
// or index order. Each input must match its declared field or element type.
func (vm *VM) PackAggregate(t *values.Type, members ...values.Value) (values.Value, error) {
	s := vm.program.Shape(t)
	if s == nil {
		return values.Value{}, fmt.Errorf("unlinked aggregate type")
	}
	if s.Native != nil || s.NativeAllocation != nil {
		return values.Value{}, fmt.Errorf("%s has native storage; use its binding", t.Name())
	}
	n := uint64(len(members))
	var size uint64
	switch s.LogicalKind() {
	case values.StructKind:
		if len(s.Fields) != len(members) {
			return values.Value{}, fmt.Errorf("%s needs %d fields", t.Name(), len(s.Fields))
		}
		if s.CellStruct != nil {
			var fields [2]values.Value
			for i, v := range members {
				var err error
				fields[i], err = vm.member(s.Fields[i].Shape, v)
				if err != nil {
					return values.Value{}, err
				}
			}
			return s.CellStruct.Pack(fields), nil
		}
		size = s.Cells
	case values.ArrayKind, values.SliceKind:
		if s.Kind == values.ArrayKind && n != s.Len || n > math.MaxUint32 || s.Elem.Cells != 0 && n > math.MaxUint64/s.Elem.Cells {
			return values.Value{}, ErrBounds
		}
		size = n * s.Elem.Cells
	default:
		return values.Value{}, fmt.Errorf("%s is not a struct, array or slice", t.Name())
	}
	ptr, err := vm.AllocCells(size)
	if err != nil {
		return values.Value{}, err
	}
	for i, v := range members {
		field := s.Elem
		var offset uint64
		if s.Kind == values.StructKind {
			f := s.Fields[i]
			field, offset = f.Shape, f.Offset
		} else {
			offset = uint64(i) * field.Cells
		}
		v, err = vm.member(field, v)
		if err != nil {
			return values.Value{}, err
		}
		field.Store(values.CellAt(ptr, offset), v)
	}
	if s.Kind == values.SliceKind {
		return values.SliceValue(ptr, uint32(n), uint32(n)), nil
	}
	return values.Value{P: ptr}, nil
}

func (vm *VM) member(s *values.Shape, v values.Value) (values.Value, error) {
	if err := vm.budget.contextErr(); err != nil {
		return values.Value{}, err
	}
	if v.R == values.NativeBorrow && v.P != nil {
		owned, err := vm.importBorrow(s.Type, v)
		if err != nil {
			return values.Value{}, err
		}
		v = owned
	}
	if s.Type.CanOwn() || s.Kind == values.InterfaceKind || s.Kind == values.StringKind || byteSliceShape(s) {
		return vm.program.ownValue(s, v, vm.budget)
	}
	return v, nil
}

// PackMap builds a guest map from alternating keys and values.
func (vm *VM) PackMap(t *values.Type, items ...values.Value) (values.Value, error) {
	s := vm.program.Shape(t)
	if s == nil || s.Kind != values.MapKind || s.Native != nil || len(items)%2 != 0 {
		return values.Value{}, fmt.Errorf("PackMap requires a guest map type and key/value pairs")
	}
	if err := vm.ChargeAllocation(128); err != nil {
		return values.Value{}, err
	}
	m := values.NewMap(s.Key, vm.program.typeShapes)
	for i := 0; i < len(items); i += 2 {
		k, err := vm.member(s.Key, items[i])
		if err != nil {
			return values.Value{}, err
		}
		v, err := vm.member(s.Elem, items[i+1])
		if err != nil {
			return values.Value{}, err
		}
		k, err = snapshot(s.Key, k, &vm.budget.allocated, vm.budget.limits)
		if err != nil {
			return values.Value{}, err
		}
		v, err = snapshot(s.Elem, v, &vm.budget.allocated, vm.budget.limits)
		if err != nil {
			return values.Value{}, err
		}
		h, err := s.Key.HashContext(vm.Context, k, vm.program.typeShapes)
		if err != nil {
			return values.Value{}, err
		}
		if _, ok, err := m.FindContext(vm.Context, k, h); err != nil {
			return values.Value{}, err
		} else if !ok {
			if err := vm.ChargeAllocation(128); err != nil {
				return values.Value{}, err
			}
		}
		if err := m.PutContext(vm.Context, k, v, h); err != nil {
			return values.Value{}, err
		}
	}
	return values.Value{P: unsafe.Pointer(m)}, nil
}

// UnpackAggregate returns guest struct fields in declaration order, or array or
// slice elements in index order. Nested values keep their EVM representation.
func (vm *VM) UnpackAggregate(t *values.Type, v values.Value) ([]values.Value, error) {
	s := vm.program.Shape(t)
	if s == nil || s.Native != nil || s.NativeAllocation != nil {
		return nil, fmt.Errorf("UnpackAggregate requires a linked guest type")
	}
	var count uint64
	switch s.LogicalKind() {
	case values.StructKind:
		count = uint64(len(s.Fields))
	case values.ArrayKind:
		count = s.Len
	case values.SliceKind:
		count = uint64(v.SliceLen())
	default:
		return nil, fmt.Errorf("%s is not a struct, array or slice", t.Name())
	}
	out, err := vm.makeValues(count)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if i%256 == 0 {
			if err := vm.budget.contextErr(); err != nil {
				return nil, err
			}
		}
		if s.LogicalKind() == values.StructKind {
			out[i] = s.FieldValue(v, i)
		} else {
			out[i], err = vm.collectionElement(s, v, uint64(i))
			if err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// UnpackMap returns alternating keys and values from a guest map.
func (vm *VM) UnpackMap(t *values.Type, v values.Value) ([]values.Value, error) {
	s := vm.program.Shape(t)
	if s == nil || s.Kind != values.MapKind || s.Native != nil {
		return nil, fmt.Errorf("UnpackMap requires a guest map type")
	}
	m := (*values.Map)(v.P)
	out, err := vm.makeValues(2 * uint64(m.Len()))
	if err != nil {
		return nil, err
	}
	n := 0
	for i, entry := range m.Entries() {
		if i%256 == 0 {
			if err := vm.budget.contextErr(); err != nil {
				return nil, err
			}
		}
		if entry.Live {
			out[n], out[n+1] = entry.Key, entry.Value
			n += 2
		}
	}
	return out, nil
}

func (vm *VM) makeValues(count uint64) ([]values.Value, error) {
	const size = uint64(unsafe.Sizeof(values.Value{}))
	if count > math.MaxInt/size {
		return nil, ErrAllocation
	}
	if err := vm.budget.contextErr(); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}
	if err := vm.ChargeAllocation(count * size); err != nil {
		return nil, err
	}
	return make([]values.Value, int(count)), nil
}
