package values

import "unsafe"

// CellStruct preserves a two-field Go struct while storing value copies in one
// cell. Addressable values use their native layout; Get reads immutable cells.
// Descriptors are immutable setup metadata, not per-value objects.
type CellStruct struct {
	// Untagged cells carry their logical type externally and box at any boundaries.
	Untagged    bool
	Type        *Type
	Get         func(Value, int) Value
	Pack        func([2]Value) Value
	LoadNative  func(unsafe.Pointer) Value
	StoreNative func(unsafe.Pointer, Value)
}

// LogicalKind hides a compact physical representation from reflection consumers.
func (s *Shape) LogicalKind() Kind {
	if s.CellStruct != nil {
		return StructKind
	}
	return s.Kind
}

// FieldValue reads a field of a value copy, including compact structs.
func (s *Shape) FieldValue(v Value, i int) Value {
	if s.CellStruct != nil {
		return s.CellStruct.Get(v, i)
	}
	f := s.Fields[i]
	return f.Shape.Stored(CellAt(v.P, f.Offset))
}

// CompactOptional stores a Value/Present struct in one cell for numeric, boolean,
// string, and registered compact-struct payloads. Other payloads keep their
// ordinary struct representation.
// Markers belong to the descriptor, so no permanent representation IDs are used.
func CompactOptional(name string, elem *Shape) *CellStruct {
	tagged := elem.Kind == TypedPairKind && elem.CellStruct != nil && !elem.CellStruct.Untagged
	if !tagged && elem.Kind != StringKind && elem.Kind != BoolKind && elem.Kind != IntKind && elem.Kind != UintKind && elem.Kind != FloatKind {
		return nil
	}
	markers := new([2]byte)
	presentZero, absent := unsafe.Pointer(&markers[0]), unsafe.Pointer(&markers[1])
	get := func(v Value, i int) Value {
		present := v != (Value{})
		if elem.Kind == StringKind || tagged {
			present = present && v.R>>63 == 0
			v.R &^= uint64(1) << 63
		} else {
			present = present && v.P != absent
			v.P = nil
		}
		if i == 1 {
			return Bool(present)
		}
		if i != 0 {
			panic("invalid optional field")
		}
		if v.P == presentZero {
			return Value{}
		}
		return v
	}
	pack := func(fields [2]Value) Value {
		v, present := fields[0], fields[1].R != 0
		if v == (Value{}) || elem.Kind == StringKind && v.R == 0 {
			if present {
				return Value{P: presentZero}
			}
			return Value{}
		}
		if !present {
			if elem.Kind == StringKind || tagged {
				v.R |= uint64(1) << 63
			} else {
				v.P = absent
			}
		}
		return v
	}
	typ := NewType(name, Pair)
	if elem.Type.CanOwn() {
		typ = NewOwnedType(name, Pair, func(v Value, b OwnershipBudget) (Value, error) {
			value, err := elem.Type.Own(get(v, 0), b)
			if err != nil {
				return Value{}, err
			}
			return pack([2]Value{value, get(v, 1)}), nil
		})
	}
	return &CellStruct{Type: typ, Get: get, Pack: pack, Untagged: true}
}
