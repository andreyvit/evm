package evm

import (
	"unsafe"

	"github.com/andreyvit/evm/values"
)

func mapLength(s *values.Shape, v values.Value) int {
	if s.Native != nil && s.Native.Map != nil {
		return s.Native.Map.Len(v.P)
	}
	return (*values.Map)(v.P).Len()
}
func (c *Call) mapEntries(s *values.Shape, v values.Value) ([]*values.MapEntry, error) {
	n := mapLength(s, v)
	entrySize := uint64(unsafe.Sizeof(uintptr(0)))
	if s.Native != nil && s.Native.Map != nil {
		entrySize += uint64(unsafe.Sizeof(values.MapEntry{}))
	}
	if err := c.ChargeAllocation(uint64(n) * entrySize); err != nil {
		return nil, err
	}
	out := make([]*values.MapEntry, 0, n)
	if s.Native == nil || s.Native.Map == nil {
		for _, entry := range (*values.Map)(v.P).Entries() {
			if entry.Live {
				out = append(out, entry)
			}
		}
		return out, nil
	}
	keyShape := c.program.nativeSlotShape(s.Key, s.Native.Map.Key)
	elemShape := c.program.nativeSlotShape(s.Elem, s.Native.Map.Element)
	if err := c.ChargeAllocation(keyShape.Native.Size + elemShape.Native.Size + 128); err != nil {
		return nil, err
	}
	key, elem := keyShape.Native.New(), elemShape.Native.New()
	it := s.Native.Map.Iter(v.P)
	for it.Next(key, elem) {
		if err := c.Context.Err(); err != nil {
			return nil, err
		}
		k, err := loadNative(c, keyShape, key, &c.budget.allocated, c.budget.limits)
		if err != nil {
			return nil, err
		}
		v, err := loadNative(c, elemShape, elem, &c.budget.allocated, c.budget.limits)
		if err != nil {
			return nil, err
		}
		out = append(out, &values.MapEntry{Key: k, Value: v, Live: true})
	}
	return out, nil
}
func (c *Call) collectionElement(s *values.Shape, v values.Value, i uint64) (values.Value, error) {
	if s.Kind == values.SliceKind {
		if view := v.NativeSlice(); view != nil {
			return c.nativeViewElement(s, view, i)
		}
	}
	if s.Kind == values.SliceKind && s.Native != nil && s.Native.Slice != nil {
		return loadNative(c, s.Elem, unsafe.Add(v.P, i*s.Native.Slice.ElementSize), &c.budget.allocated, c.budget.limits)
	}
	return s.Elem.Stored(values.CellAt(v.P, i*s.Elem.Cells)), nil
}

// SliceElement reads an element using the linked native or VM storage layout.
// Host callbacks must use their parameter shape rather than assume VM cells.
func (c *Call) SliceElement(s *values.Shape, v values.Value, index uint64) (values.Value, error) {
	if s == nil || s.Kind != values.SliceKind {
		return values.Value{}, ErrInvalidState
	}
	if index >= uint64(v.SliceLen()) {
		return values.Value{}, ErrBounds
	}
	return c.collectionElement(s, v, index)
}
