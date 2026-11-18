package evm

import (
	"unsafe"

	"github.com/andreyvit/evm/values"
)

func (vm *VM) nativeMapOperation(op *ExtendedOp, b *executionBudget, get func(int) values.Value, put func(int, values.Value)) (bool, error) {
	s := op.Shape
	m := s.Native.Map
	keyShape := vm.program.nativeSlotShape(s.Key, m.Key)
	elemShape := vm.program.nativeSlotShape(s.Elem, m.Element)
	allocate := func(shape *values.Shape) (unsafe.Pointer, error) {
		if shape.Native == nil {
			return nil, ErrInvalidState
		}
		if err := charge(shape.Native.Size, &b.allocated, b.limits); err != nil {
			return nil, err
		}
		return shape.Native.New(), nil
	}
	key := func() (unsafe.Pointer, error) {
		p, err := allocate(keyShape)
		if err != nil {
			return nil, err
		}
		err = storeNative(&vm.call, keyShape, p, get(1), &b.allocated, b.limits)
		return p, err
	}
	switch op.Kind {
	case ExtendedMakeMap:
		if err := charge(128, &b.allocated, b.limits); err != nil {
			return true, err
		}
		put(0, values.Value{P: m.Make()})
	case ExtendedLen:
		put(0, values.Uint(uint64(m.Len(get(0).P))))
	case ExtendedClear:
		m.Clear(get(0).P)
	case ExtendedLookup:
		k, err := key()
		if err != nil {
			return true, err
		}
		v, err := allocate(elemShape)
		if err != nil {
			return true, err
		}
		ok := m.Lookup(get(0).P, k, v)
		result := values.Value{}
		if ok {
			result, err = loadNative(&vm.call, elemShape, v, &b.allocated, b.limits)
			if err != nil {
				return true, err
			}
		}
		put(0, result)
		if len(op.Out) == 2 {
			put(1, values.Bool(ok))
		}
	case ExtendedMapUpdate:
		if get(0).P == nil {
			return true, ErrNilMap
		}
		k, err := key()
		if err != nil {
			return true, err
		}
		v, err := allocate(elemShape)
		if err != nil {
			return true, err
		}
		// Reserve the entry before growing Go's map, including overwritten values.
		if err := charge(128, &b.allocated, b.limits); err != nil {
			return true, err
		}
		if err := storeNative(&vm.call, elemShape, v, get(2), &b.allocated, b.limits); err != nil {
			return true, err
		}
		m.Update(get(0).P, k, v)
	case ExtendedDelete:
		k, err := key()
		if err != nil {
			return true, err
		}
		m.Delete(get(0).P, k)
	case ExtendedRange:
		if err := charge(128, &b.allocated, b.limits); err != nil {
			return true, err
		}
		k, err := allocate(keyShape)
		if err != nil {
			return true, err
		}
		v, err := allocate(elemShape)
		if err != nil {
			return true, err
		}
		put(0, values.Value{P: unsafe.Pointer(&iterator{native: m.Iter(get(0).P), nativeKey: k, nativeValue: v})})
	case ExtendedNext:
		it := (*iterator)(get(0).P)
		ok := it.native.Next(it.nativeKey, it.nativeValue)
		put(0, values.Bool(ok))
		if ok {
			k, err := loadNative(&vm.call, keyShape, it.nativeKey, &b.allocated, b.limits)
			if err != nil {
				return true, err
			}
			v, err := loadNative(&vm.call, elemShape, it.nativeValue, &b.allocated, b.limits)
			if err != nil {
				return true, err
			}
			put(1, k)
			put(2, v)
		}
	default:
		return false, nil
	}
	return true, nil
}
