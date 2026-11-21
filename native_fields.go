package evm

import (
	"fmt"
	"reflect"
	"slices"
	"unsafe"

	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/values"
)

// LoadNative copies a value from its declared native layout into VM registers.
// The caller keeps p alive for the call; reference fields retain their targets.
func (c *Call) LoadNative(s *values.Shape, p unsafe.Pointer) (values.Value, error) {
	return loadNative(c, s, p, &c.budget.allocated, c.budget.limits)
}

// StoreNative copies a VM value into caller-owned storage of the declared layout.
func (c *Call) StoreNative(s *values.Shape, p unsafe.Pointer, v values.Value) error {
	return storeNative(c, s, p, v, &c.budget.allocated, c.budget.limits)
}

func nativeLoadedValue(c *Call, s *values.Shape, v values.Value, used *allocationUsage, limits Limits) (values.Value, error) {
	if s.Native != nil && s.Native.Codec != nil {
		return s.Native.Codec.Decode(v)
	}
	if codec := c.program.nativeCodecs[s.Type]; codec != nil {
		return codec.Decode(c, v)
	}
	if s.Kind == values.FunctionKind && s.Native != nil && s.Native.GoType != nil && s.Native.GoType.Kind() == reflect.Func {
		if v.P == nil || reflect.NewAt(s.Native.GoType, v.P).Elem().IsNil() {
			return values.Value{}, nil
		}
		return values.Value{}, fmt.Errorf("native function %s requires a binding", s.Type.Name())
	}
	if s.Kind == values.ComplexKind {
		if s.Bits == 64 {
			return values.Uint(uint64(*(*uint64)(v.P))), nil
		}
		return makeComplex(s, *(*complex128)(v.P), used, limits)
	}
	if s.Kind == values.MapKind && s.Native != nil && s.Native.Map != nil {
		return s.Native.Map.Load(v.P), nil
	}
	if s.Kind == values.SliceKind && s.Native != nil && s.Native.Slice != nil {
		raw := s.Native.Slice.Load(v.P)
		if element := s.Native.Slice.Element; element != nil {
			return c.NativeSlice(s.Native, element, raw.P, raw.SliceLen(), raw.SliceCap())
		}
		return raw, nil
	}
	if s.Kind == values.StructKind || s.Kind == values.ArrayKind {
		return loadNative(c, s, v.P, used, limits)
	}
	if s.Kind == values.PointerKind && v.P != nil {
		v.R = values.NativeAddress
	}
	return v, nil
}

func nativeStoredValue(c *Call, s *values.Shape, v values.Value, used *allocationUsage, limits Limits) (values.Value, error) {
	if s.Native != nil && s.Native.Codec != nil {
		return s.Native.Codec.Encode(v, c.ChargeAllocation)
	}
	if codec := c.program.nativeCodecs[s.Type]; codec != nil {
		return codec.Encode(c, v)
	}
	if s.Kind == values.FunctionKind && s.Native != nil && s.Native.GoType != nil && s.Native.GoType.Kind() == reflect.Func {
		if v.R == 0 {
			return values.Value{}, nil
		}
		return values.Value{}, fmt.Errorf("native function %s requires a binding", s.Type.Name())
	}
	if s.Kind == values.ComplexKind {
		if err := charge(uint64(s.Bits/8), used, limits); err != nil {
			return values.Value{}, err
		}
		if s.Bits == 64 {
			bits := v.R
			return values.Value{P: unsafe.Pointer(&bits)}, nil
		}
		z := complexValue(s, v)
		return values.Value{P: unsafe.Pointer(&z)}, nil
	}
	if s.Kind == values.MapKind && s.Native != nil && s.Native.Map != nil {
		if err := charge(s.Native.Size, used, limits); err != nil {
			return values.Value{}, err
		}
		p := s.Native.New()
		s.Native.Map.Store(p, v)
		return values.Value{P: p}, nil
	}
	if s.Kind == values.SliceKind && s.Native != nil && s.Native.Slice != nil {
		if err := charge(s.Native.Size, used, limits); err != nil {
			return values.Value{}, err
		}
		if s.Native.Slice.Element != nil || v.NativeSlice() != nil {
			view := v.NativeSlice()
			if view == nil || view.Layout.GoType.Elem() != s.Native.GoType.Elem() {
				// Native slots retain their declared physical element type.
				if v.SliceData() != nil {
					return values.Value{}, fmt.Errorf("slice requires explicit conversion to %s", s.Native.GoType)
				}
				v = values.Value{}
			} else {
				v = values.SliceValue(view.Data, view.Length, view.Capacity)
			}
		}
		p := s.Native.New()
		s.Native.Slice.Store(p, v)
		return values.Value{P: p}, nil
	}
	if s.Kind == values.StructKind || s.Kind == values.ArrayKind {
		if s.Native == nil {
			return values.Value{}, ErrInvalidState
		}
		if err := charge(s.Native.Size, used, limits); err != nil {
			return values.Value{}, err
		}
		p := s.Native.New()
		if err := storeNative(c, s, p, v, used, limits); err != nil {
			return values.Value{}, err
		}
		return values.Value{P: p}, nil
	}
	if s.Kind == values.PointerKind && v.P != nil && v.R != values.NativeAddress {
		return values.Value{}, ErrInvalidState
	}
	return v, nil
}

func loadNative(c *Call, s *values.Shape, p unsafe.Pointer, used *allocationUsage, limits Limits) (values.Value, error) {
	if s.Native == nil {
		return values.Value{}, ErrInvalidState
	}
	if s.Kind == values.ArrayKind {
		dst, err := allocCells(s.Cells, used, limits)
		if err != nil {
			return values.Value{}, err
		}
		for i := uint64(0); i < s.Len; i++ {
			if i&255 == 0 {
				if err := c.Context.Err(); err != nil {
					return values.Value{}, err
				}
			}
			v, err := loadNative(c, s.Elem, unsafe.Add(p, i*s.Native.Array.ElementSize), used, limits)
			if err != nil {
				return values.Value{}, err
			}
			s.Elem.Store(values.CellAt(dst, i*s.Elem.Cells), v)
		}
		return values.Value{P: dst}, nil
	}
	if s.Kind != values.StructKind {
		v := s.Native.Load(p)
		return nativeLoadedValue(c, s, v, used, limits)
	}
	dst, err := allocCells(s.Cells, used, limits)
	if err != nil {
		return values.Value{}, err
	}
	if len(s.Native.Fields) != len(s.Fields) {
		return values.Value{}, ErrInvalidState
	}
	if slot := s.Native.OpaqueCell; slot != 0 {
		if err := charge(s.Native.Size, used, limits); err != nil {
			return values.Value{}, err
		}
		snapshot := s.Native.New()
		s.Native.Store(snapshot, values.Value{P: p})
		*(*values.Value)(values.CellAt(dst, slot-1)) = values.Value{P: snapshot}
	}
	for i, f := range s.Fields {
		v := s.Native.Fields[i].Get(p)
		if !s.Native.Fields[i].Cells {
			v, err = nativeLoadedValue(c, c.program.nativeFieldShape(s, i), v, used, limits)
		}
		if err != nil {
			return values.Value{}, err
		}
		f.Shape.Store(values.CellAt(dst, f.Offset), v)
	}
	return values.Value{P: dst}, nil
}
func storeNative(c *Call, s *values.Shape, p unsafe.Pointer, v values.Value, used *allocationUsage, limits Limits) error {
	if s.Native == nil {
		return ErrInvalidState
	}
	if s.Kind == values.ArrayKind {
		for i := uint64(0); i < s.Len; i++ {
			if i&255 == 0 {
				if err := c.Context.Err(); err != nil {
					return err
				}
			}
			if err := storeNative(c, s.Elem, unsafe.Add(p, i*s.Native.Array.ElementSize), s.Elem.Stored(values.CellAt(v.P, i*s.Elem.Cells)), used, limits); err != nil {
				return err
			}
		}
		return nil
	}
	if s.Kind != values.StructKind {
		v, err := nativeStoredValue(c, s, v, used, limits)
		if err != nil {
			return err
		}
		s.Native.Store(p, v)
		return nil
	}
	if len(s.Native.Fields) != len(s.Fields) {
		return ErrInvalidState
	}
	if slot := s.Native.OpaqueCell; slot != 0 {
		var snapshot unsafe.Pointer
		if v.P != nil {
			snapshot = (*values.Value)(values.CellAt(v.P, slot-1)).P
		}
		if snapshot == nil {
			if err := charge(s.Native.Size, used, limits); err != nil {
				return err
			}
			snapshot = s.Native.New()
		}
		s.Native.Store(p, values.Value{P: snapshot})
	}
	for i, f := range s.Fields {
		x := f.Shape.Stored(values.CellAt(v.P, f.Offset))
		if !s.Native.Fields[i].Cells {
			var err error
			x, err = nativeStoredValue(c, c.program.nativeFieldShape(s, i), x, used, limits)
			if err != nil {
				return err
			}
		}
		s.Native.Fields[i].Set(p, x)
	}
	return nil
}

func (p *Program) nativeFieldShape(s *values.Shape, index int) *values.Shape {
	return p.nativeSlotShape(s.Fields[index].Shape, s.Native.Fields[index].Layout)
}

func (p *Program) nativeSlotShape(logical *values.Shape, layout *values.NativeLayout) *values.Shape {
	if layout == nil {
		return logical
	}
	_, physical := p.physicalShape(logical, layout)
	return physical
}

// A native aggregate result still addresses Go storage. Materialize it before
// a pointer-only register discards the discriminator.
func (c *Call) importBorrow(t *values.Type, v values.Value) (values.Value, error) {
	s := c.program.Shape(t)
	if row, ok := c.program.borrowRecords[t]; ok {
		ptr := v.BorrowedNative()
		typ := ptr.Type().Elem()
		entity, ok := row.Model.Type().(*kvo.EntityType)
		if !ok || typ != row.NativeAllocation && !slices.Contains(entity.NativeTypes(), typ) {
			return values.Value{}, fmt.Errorf("unregistered %v backing for %s", typ, row.Name)
		}
		if t == row.Value && row.NativeAllocation != nil {
			if typ != row.NativeAllocation {
				return values.Value{}, ErrInvalidState
			}
			if err := c.ChargeAllocation(uint64(typ.Size())); err != nil {
				return values.Value{}, err
			}
			snapshot := reflect.New(typ)
			snapshot.Elem().Set(ptr.Elem())
			return c.borrowCell(values.Value{P: snapshot.UnsafePointer()})
		}
		r := kvo.Bind(typ, row.Model).Record(ptr)
		cell := r.Value()
		if row.Mutable {
			var err error
			cell, err = NativeMutableRecordValue(row.Model, r)
			if err != nil {
				return values.Value{}, err
			}
		}
		if t == row.Value {
			return c.borrowCell(cell)
		}
		return cell, nil
	}
	if s != nil && s.Aggregate() && s.CellStruct == nil {
		ptr := v.BorrowedNative()
		if s.Native == nil || ptr.Type().Elem() != s.Native.GoType {
			return values.Value{}, ErrInvalidState
		}
		return c.LoadNative(s, ptr.UnsafePointer())
	}
	if t.Repr() == values.Pointer {
		return values.Value{}, ErrInvalidState
	}
	return v, nil
}

func (c *Call) borrowCell(v values.Value) (values.Value, error) {
	p, err := c.AllocCells(1)
	if err != nil {
		return values.Value{}, err
	}
	*(*values.Value)(p) = v
	return values.Value{P: p}, nil
}
