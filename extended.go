package evm

import (
	"fmt"
	"slices"

	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/kvo/codec/row2"
	"github.com/andreyvit/evm/values"
)

// RecordListDirect fuses a single adjacent len/index consumer with its property
// read. No list value escapes the instruction, so no VM temporary is needed.
const RecordListDirect uint8 = 1

// RecordValueReceiver reads a property from an immutable row value snapshot.
const RecordValueReceiver uint8 = 2

// ExtendedOp is an immutable bytecode operand descriptor, not an executable
// callback. BC selects it for operations needing more than three operands.
type ExtendedOp struct {
	listAccess *kvo.ListAccess
	listSlot   int       // reusable VM iterator slot, assigned by Link
	step       row2.Step // model-specific policy resolved by Link
	childModel *kvo.Model
	Record     *RecordType
	Kind       ExtendedKind
	Shape      *values.Shape
	Source     *values.Shape
	In, Out    []Operand
	Index      uint64
	ChildKey   uint64 // leaf property for ExtendedGetChildKey
	Flags      uint8
	Packed     bool // Local slice/array backing uses the element's scalar bank.
}
type Operand struct {
	Slot uint16
	Repr values.Repr
}
type ExtendedKind uint8

const (
	ExtendedMakeInterface ExtendedKind = iota + 1
	ExtendedTypeAssert
	ExtendedFieldAddr
	ExtendedField
	ExtendedIndexAddr
	ExtendedIndex
	ExtendedSlice
	ExtendedMakeSlice
	ExtendedSliceToArray
	ExtendedMakeMap
	ExtendedLookup
	ExtendedMapUpdate
	ExtendedDelete
	ExtendedClear
	ExtendedLen
	ExtendedCap
	ExtendedAppend
	ExtendedCopy
	ExtendedRange
	ExtendedNext
	ExtendedConvert
	ExtendedEqual
	ExtendedComplex
	ExtendedReal
	ExtendedImag
	ExtendedComplexBinary
	ExtendedComplexNegate
	ExtendedMinMax
	ExtendedGetKey
	ExtendedSetKey
	ExtendedGetChildKey
	ExtendedPackedArray
	ExtendedPackedLoad
	ExtendedPackedStore
	ExtendedAddressLoad
	ExtendedAddressStore
	ExtendedFieldStore
	ExtendedNativeAlloc
	ExtendedOwn
	ExtendedRecordCopy
	ExtendedRecordList
	ExtendedRecordListLen
	ExtendedRecordListIndex
	ExtendedNativeAddress
)

func snapshotShape(s *values.Shape, seen map[*values.Shape]*values.Shape) (*values.Shape, error) {
	if s == nil {
		return nil, nil
	}
	if c := seen[s]; c != nil {
		return c, nil
	}
	if s.Type == nil || s.Kind < values.BoolKind || s.Kind > values.NativeValueKind || s.Cells > 1<<32 {
		return nil, fmt.Errorf("invalid storage shape")
	}
	if err := validateNativeAllocation(s); err != nil {
		return nil, err
	}
	c := *s
	if s.Native != nil {
		if s.Native.New == nil || s.Native.Load == nil || s.Native.Store == nil {
			return nil, fmt.Errorf("incomplete native layout")
		}
		if a := s.Native.Address; a != nil && (a.Encode == nil || a.Decode == nil) {
			return nil, fmt.Errorf("incomplete native address codec")
		}
		if s.Kind == values.StructKind && len(s.Native.Fields) != len(s.Fields) {
			return nil, fmt.Errorf("native field layout mismatch")
		}
		for _, f := range s.Native.Fields {
			if f.Get == nil || f.Set == nil || uint64(f.Offset) > s.Native.Size {
				return nil, fmt.Errorf("invalid native field layout")
			}
		}
		c.Native = snapshotNativeLayout(s.Native, make(map[*values.NativeLayout]*values.NativeLayout))
	}
	c.NativeAllocation = snapshotNativeLayout(s.NativeAllocation, make(map[*values.NativeLayout]*values.NativeLayout))
	seen[s] = &c
	c.Fields = slices.Clone(s.Fields)
	c.Methods = slices.Clone(s.Methods)
	for i := range c.Methods {
		m := &c.Methods[i]
		var err error
		m.Signature, err = snapshotShape(m.Signature, seen)
		if err != nil {
			return nil, err
		}
		if m.ID == "" || m.Signature == nil || m.Signature.Kind != values.FunctionKind {
			return nil, fmt.Errorf("invalid interface method")
		}
	}

	var err error
	c.Elem, err = snapshotShape(s.Elem, seen)
	if err != nil {
		return nil, err
	}
	c.Key, err = snapshotShape(s.Key, seen)
	if err != nil {
		return nil, err
	}
	c.Params = slices.Clone(s.Params)
	c.Results = slices.Clone(s.Results)
	for _, set := range [][]*values.Shape{c.Params, c.Results} {
		for i, s := range set {
			set[i], err = snapshotShape(s, seen)
			if err != nil {
				return nil, err
			}
			if set[i] == nil {
				return nil, fmt.Errorf("nil function shape")
			}
		}
	}
	for i := range c.Fields {
		c.Fields[i].Shape, err = snapshotShape(c.Fields[i].Shape, seen)
		if err != nil {
			return nil, err
		}
		if c.Fields[i].Shape == nil || (c.CellStruct == nil && (c.Fields[i].Offset > c.Cells || c.Fields[i].Shape.Cells > c.Cells-c.Fields[i].Offset)) {
			return nil, fmt.Errorf("invalid field shape")
		}
	}
	switch s.Kind {
	case values.PointerKind, values.SliceKind, values.ArrayKind, values.ComplexKind:
		if c.Elem == nil {
			return nil, fmt.Errorf("missing element shape")
		}
	case values.MapKind:
		if c.Elem == nil || c.Key == nil {
			return nil, fmt.Errorf("missing map shape")
		}
	}
	return &c, nil
}
func (p *Program) validateExtended(f *linkedFunction, op ExtendedOp) bool {
	for _, set := range [][]Operand{op.In, op.Out} {
		for _, r := range set {
			if r.Repr < values.Raw || r.Repr > values.Pair {
				return false
			}
			if r.Repr != values.Pointer && int(r.Slot) >= f.rSize {
				return false
			}
			if r.Repr != values.Raw && int(r.Slot) >= f.pSize {
				return false
			}
		}
	}
	if op.Shape == nil {
		return false
	}
	ni, no := len(op.In), len(op.Out)
	ins := func(reprs ...values.Repr) bool {
		if ni != len(reprs) {
			return false
		}
		for i, r := range reprs {
			if op.In[i].Repr != r {
				return false
			}
		}
		return true
	}
	outs := func(reprs ...values.Repr) bool {
		if no != len(reprs) {
			return false
		}
		for i, r := range reprs {
			if op.Out[i].Repr != r {
				return false
			}
		}
		return true
	}
	raw, ptr, pair := values.Raw, values.Pointer, values.Pair
	s := op.Shape
	if op.Packed {
		switch op.Kind {
		case ExtendedIndexAddr, ExtendedIndex, ExtendedMakeSlice, ExtendedSlice, ExtendedAppend, ExtendedCopy, ExtendedClear:
			if (s.Kind != values.ArrayKind && s.Kind != values.SliceKind) || !packable(s.Elem) {
				return false
			}
		default:
			return false
		}
	}
	switch op.Kind {
	case ExtendedRecordCopy:
		return s.Kind == values.StructKind && s.Cells == 1 && ins(pair) && outs(ptr)
	case ExtendedOwn:
		return (s.Type.CanOwn() || s.Kind == values.InterfaceKind || s.Kind == values.StringKind) && ins(s.Type.Repr()) && outs(s.Type.Repr())
	case ExtendedGetKey, ExtendedSetKey, ExtendedGetChildKey, ExtendedRecordList, ExtendedRecordListLen, ExtendedRecordListIndex:
		if op.Record == nil || op.Record.Model == nil || op.Index >= VMFieldBase {
			return false
		}
		prop := op.Record.Model.PropByCode(op.Index)
		if prop == nil {
			return false
		}
		if op.Kind == ExtendedRecordList || op.Kind == ExtendedRecordListLen || op.Kind == ExtendedRecordListIndex {
			list, ok := prop.AnyType().(*kvo.ListType)
			if !ok || op.Flags & ^RecordListDirect != 0 || op.Kind == ExtendedRecordList && op.Flags != 0 || op.Record.Mutable || s.Kind != values.SliceKind || RecordScalarType(list.ItemType()) == nil || !recordShapeMatches(prop.AnyType(), s) {
				return false
			}
			switch op.Kind {
			case ExtendedRecordList:
				return ins(pair) && outs(pair)
			case ExtendedRecordListLen:
				return ins(pair) && outs(raw)
			default:
				return ins(pair, raw) && outs(s.Elem.Type.Repr())
			}
		}
		if op.Kind == ExtendedGetChildKey {
			child := prop.TypeModel()
			if op.Record.Mutable || child == nil || op.ChildKey >= VMFieldBase || op.Source == nil || op.Source.Kind != values.TypedPairKind {
				return false
			}
			leaf := child.PropByCode(op.ChildKey)
			return leaf != nil && recordShapeMatches(leaf.AnyType(), s) && ins(pair) && outs(raw)
		}
		receiverRepr := op.Record.Pointer.Repr()
		if op.Flags&RecordValueReceiver != 0 {
			if op.Kind != ExtendedGetKey || op.Record.Mutable || op.Flags != RecordValueReceiver || op.Source == nil || op.Source.Kind != values.StructKind || op.Record.Value == nil || op.Source.Type != op.Record.Value {
				return false
			}
			if layout := op.Source.Native; layout != nil && (layout.Address == nil || layout.Compare == nil || layout.OpaqueCell == 0 || layout.OpaqueCell > op.Source.Cells) {
				return false
			}
			receiverRepr = ptr
		}
		if prop.TypeModel() != nil {
			return op.Kind == ExtendedGetKey && (s.Kind == values.PointerKind || s.Kind == values.TypedPairKind) && ins(receiverRepr) && outs(s.Type.Repr())
		}
		if !recordShapeMatches(prop.AnyType(), s) {
			return false
		}
		if op.Kind == ExtendedSetKey {
			return op.Record.Mutable && ins(receiverRepr, s.Type.Repr()) && outs()
		}
		return ins(receiverRepr) && outs(s.Type.Repr())
	case ExtendedPackedArray:
		return s.Kind == values.ArrayKind && packable(s.Elem) && ins() && outs(ptr)
	case ExtendedPackedLoad:
		return packable(s) && (ins(ptr) || ins(pair)) && outs(s.Type.Repr())
	case ExtendedPackedStore:
		return packable(s) && (ins(ptr, s.Type.Repr()) || ins(pair, s.Type.Repr())) && outs()
	case ExtendedMakeInterface:
		return s.Kind != values.InterfaceKind && op.Index != 0 && op.Index < uint64(len(p.types)) && p.types[op.Index].shape.Type == s.Type && ins(s.Type.Repr()) && outs(pair)
	case ExtendedTypeAssert:
		return ins(pair) && (outs(s.Type.Repr()) || outs(s.Type.Repr(), raw))
	case ExtendedAddressLoad:
		return (s.Native == nil || s.Native.Address == nil) && ins(pair) && outs(s.Type.Repr())
	case ExtendedNativeAddress:
		return s.Native != nil && s.Native.Address != nil && s.Native.Address.Encode != nil && s.Native.Address.Decode != nil && ins(pair) && outs(pair)
	case ExtendedNativeAlloc:
		return (s.NativeStorage() || s.Kind == values.ArrayKind && s.NativeAllocation != nil) && ins() && outs(pair)
	case ExtendedAddressStore:
		return ins(pair, s.Type.Repr()) && outs()
	case ExtendedFieldStore:
		if (s.Kind != values.StructKind && s.CellStruct == nil) || op.Index < VMFieldBase || op.Index&^VMFieldBase >= uint64(len(s.Fields)) {
			return false
		}
		repr := s.Fields[op.Index&^VMFieldBase].Shape.Type.Repr()
		if op.Flags&FieldValueCopy != 0 {
			return s.CellStruct != nil && ins(pair, repr) && outs(pair)
		}
		return (ins(ptr, repr) || ins(pair, repr)) && outs()
	case ExtendedFieldAddr, ExtendedField:
		if (s.Kind != values.StructKind && s.CellStruct == nil) || op.Index < VMFieldBase || op.Index&^VMFieldBase >= uint64(len(s.Fields)) || !(ins(ptr) || ins(pair)) {
			return false
		}
		if op.Kind == ExtendedFieldAddr {
			return outs(ptr) || outs(pair)
		}
		return outs(s.Fields[op.Index&^VMFieldBase].Shape.Type.Repr())
	case ExtendedIndexAddr, ExtendedIndex:
		if s.Kind == values.StringKind {
			return op.Kind == ExtendedIndex && ins(pair, raw) && outs(raw)
		}
		if s.Kind != values.ArrayKind && s.Kind != values.SliceKind {
			return false
		}
		if !ins(s.Type.Repr(), raw) && !(s.Kind == values.ArrayKind && ins(pair, raw)) {
			return false
		}
		if op.Kind == ExtendedIndexAddr {
			return outs(ptr) || outs(pair)
		}
		return outs(s.Elem.Type.Repr())
	case ExtendedSlice:
		return (s.Kind == values.StringKind || s.Kind == values.SliceKind || s.Kind == values.ArrayKind) && op.Flags <= 3 && (ins(s.Type.Repr(), raw, raw, raw) || s.Kind == values.ArrayKind && ins(pair, raw, raw, raw)) && outs(pair)
	case ExtendedMakeSlice:
		return s.Kind == values.SliceKind && ins(raw, raw) && outs(pair)
	case ExtendedSliceToArray:
		return s.Kind == values.PointerKind && s.Elem.Kind == values.ArrayKind && ins(pair) && outs(s.Type.Repr())
	case ExtendedMakeMap:
		return s.Kind == values.MapKind && (ins() || ins(raw)) && outs(ptr)
	case ExtendedLookup:
		return s.Kind == values.MapKind && ins(ptr, s.Key.Type.Repr()) && (outs(s.Elem.Type.Repr()) || outs(s.Elem.Type.Repr(), raw))
	case ExtendedMapUpdate:
		return s.Kind == values.MapKind && ins(ptr, s.Key.Type.Repr(), s.Elem.Type.Repr()) && outs()
	case ExtendedDelete:
		return s.Kind == values.MapKind && ins(ptr, s.Key.Type.Repr()) && outs()
	case ExtendedClear:
		return (s.Kind == values.SliceKind || s.Kind == values.MapKind) && ins(s.Type.Repr()) && outs()
	case ExtendedLen:
		return (s.Kind == values.StringKind || s.Kind == values.SliceKind || s.Kind == values.MapKind || s.Kind == values.ArrayKind) && ins(s.Type.Repr()) && outs(raw)
	case ExtendedCap:
		return (s.Kind == values.SliceKind || s.Kind == values.ArrayKind) && ins(s.Type.Repr()) && outs(raw)
	case ExtendedAppend, ExtendedCopy:
		if s.Kind != values.SliceKind || op.Flags > 1 || !ins(pair, pair) {
			return false
		}
		if op.Flags == 1 && (s.Elem.Kind != values.UintKind || s.Elem.Bits != 8) {
			return false
		}
		if op.Kind == ExtendedAppend {
			return outs(pair)
		}
		return outs(raw)
	case ExtendedRange:
		return (s.Kind == values.StringKind || s.Kind == values.MapKind) && ins(s.Type.Repr()) && outs(ptr)
	case ExtendedNext:
		if s.Kind == values.StringKind {
			return op.Flags == 1 && ins(ptr) && outs(raw, raw, raw)
		}
		return s.Kind == values.MapKind && op.Flags == 0 && ins(ptr) && outs(raw, s.Key.Type.Repr(), s.Elem.Type.Repr())
	case ExtendedConvert:
		src := op.Source
		if src == nil || !ins(src.Type.Repr()) || !outs(s.Type.Repr()) {
			return false
		}
		if (src.CellStruct != nil || s.CellStruct != nil) && src.LogicalKind() == values.StructKind && s.LogicalKind() == values.StructKind {
			if len(src.Fields) != 2 || len(s.Fields) != 2 {
				return false
			}
			for i := range s.Fields {
				if src.Fields[i].Shape.Type != s.Fields[i].Shape.Type {
					return false
				}
			}
			return true
		}
		switch s.Kind {
		case values.ComplexKind:
			return src.Kind == values.ComplexKind
		case values.StringKind:
			return src.Kind == values.IntKind || src.Kind == values.UintKind || src.Kind == values.SliceKind && byteOrRune(src.Elem)
		case values.SliceKind:
			return src.Kind == values.StringKind && byteOrRune(s.Elem)
		}
		return false
	case ExtendedEqual:
		return (s.Kind == values.StructKind || s.Kind == values.ArrayKind || s.Kind == values.ComplexKind || s.Kind == values.InterfaceKind || s.Kind == values.TypedPairKind || s.Kind == values.TimestampKind || s.Kind == values.SliceKind) && ins(s.Type.Repr(), s.Type.Repr()) && outs(raw)
	case ExtendedComplex:
		return s.Kind == values.ComplexKind && ins(raw, raw) && outs(s.Type.Repr())
	case ExtendedComplexBinary:
		return s.Kind == values.ComplexKind && (op.Flags == '+' || op.Flags == '-' || op.Flags == '*' || op.Flags == '/') && ins(s.Type.Repr(), s.Type.Repr()) && outs(s.Type.Repr())
	case ExtendedReal, ExtendedImag:
		return s.Kind == values.ComplexKind && ins(s.Type.Repr()) && outs(raw)
	case ExtendedComplexNegate:
		return s.Kind == values.ComplexKind && ins(s.Type.Repr()) && outs(s.Type.Repr())
	case ExtendedMinMax:
		if s.Kind != values.IntKind && s.Kind != values.UintKind && s.Kind != values.FloatKind && s.Kind != values.StringKind {
			return false
		}
		if ni < 1 || !outs(s.Type.Repr()) || op.Flags > 1 {
			return false
		}
		for _, r := range op.In {
			if r.Repr != s.Type.Repr() {
				return false
			}
		}
		return true
	}
	return false
}

type shapeValidationState uint8

const (
	shapeUnvisited shapeValidationState = iota
	shapeVisiting
	shapeValid
	shapeInvalid
)

func (s shapeValidationState) String() string {
	switch s {
	case shapeUnvisited:
		return "unvisited"
	case shapeVisiting:
		return "visiting"
	case shapeValid:
		return "valid"
	case shapeInvalid:
		return "invalid"
	default:
		return fmt.Sprintf("shapeValidationState(%d)", uint8(s))
	}
}

func validateStorageShape(s *values.Shape, visiting map[*values.Shape]shapeValidationState) (valid bool) {
	if state := visiting[s]; state != shapeUnvisited {
		return state == shapeValid
	}
	visiting[s] = shapeVisiting
	defer func() {
		if valid {
			visiting[s] = shapeValid
		} else {
			visiting[s] = shapeInvalid
		}
	}()
	repr := values.Raw
	switch s.Kind {
	case values.StringKind, values.FunctionKind, values.InterfaceKind, values.TypedPairKind, values.TimestampKind:
		repr = values.Pair
	case values.PointerKind:
		repr = s.Type.Repr()
		if repr != values.Pointer && repr != values.Pair {
			return false
		}
	case values.StructKind, values.ArrayKind, values.MapKind, values.NativeValueKind:
		repr = values.Pointer
	case values.SliceKind:
		repr = values.Pair
	case values.ComplexKind:
		if s.Bits == 128 {
			repr = values.Pointer
		} else if s.Bits != 64 {
			return false
		}
	}
	if s.Type.Repr() != repr {
		return false
	}
	switch s.Kind {
	case values.StructKind:
		cells := uint64(0)
		for _, f := range s.Fields {
			if f.Offset != cells || !validateStorageShape(f.Shape, visiting) {
				return false
			}
			cells += f.Shape.Cells
		}
		if s.Native != nil && s.Native.OpaqueCell != 0 {
			if s.Native.OpaqueCell != cells+1 {
				return false
			}
			if comparison := s.Native.Compare; comparison != nil {
				if comparison.Zero == nil {
					return false
				}
				field := 0
				for _, step := range comparison.Steps {
					if step.Field >= 0 {
						if step.Field != field || field >= len(s.Fields) {
							return false
						}
						field++
					} else if step.Equal == nil || step.Hash == nil || step.Work == nil {
						return false
					}
				}
				if field != len(s.Fields) {
					return false
				}
			}
			cells++
		}
		return cells == s.Cells
	case values.ArrayKind:
		if !validateStorageShape(s.Elem, visiting) {
			return false
		}
		return (s.Elem.Cells == 0 || s.Len <= uint64(1<<32)/s.Elem.Cells) && s.Cells == s.Len*s.Elem.Cells
	case values.ComplexKind:
		return s.Cells == uint64(s.Bits/64) && s.Elem.Kind == values.FloatKind && s.Elem.Bits == s.Bits/2
	case values.IntKind, values.UintKind:
		return s.Cells == 1 && (s.Bits == 8 || s.Bits == 16 || s.Bits == 32 || s.Bits == 64)
	case values.FloatKind:
		return s.Cells == 1 && (s.Bits == 32 || s.Bits == 64)
	default:
		return s.Cells == 1
	}
}

func byteOrRune(s *values.Shape) bool {
	return s.Kind == values.UintKind && s.Bits == 8 || s.Kind == values.IntKind && s.Bits == 32
}

// ValidateShape checks native declaration storage before the compiler builds Go
// types. It accepts recursive pointer graphs and rejects recursive value storage.
func ValidateShape(s *values.Shape) error {
	seen := make(map[*values.Shape]*values.Shape)
	if _, err := snapshotShape(s, seen); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("missing storage shape")
	}
	states := make(map[*values.Shape]shapeValidationState)
	for _, shape := range seen {
		if !validateStorageShape(shape, states) {
			return fmt.Errorf("invalid storage layout for %s", shape.Type.Name())
		}
	}
	return nil
}

// Physical slot metadata is part of the linked snapshot too. Preserve cycles
// and sharing within a layout without retaining mutable declaration objects.
func snapshotNativeLayout(layout *values.NativeLayout, seen map[*values.NativeLayout]*values.NativeLayout) *values.NativeLayout {
	if layout == nil {
		return nil
	}
	if copy := seen[layout]; copy != nil {
		return copy
	}
	copy := *layout
	seen[layout] = &copy
	if layout.Address != nil {
		address := *layout.Address
		copy.Address = &address
	}
	if layout.Codec != nil {
		codec := *layout.Codec
		copy.Codec = &codec
	}
	copy.Fields = slices.Clone(layout.Fields)
	for i := range copy.Fields {
		copy.Fields[i].Layout = snapshotNativeLayout(copy.Fields[i].Layout, seen)
	}
	cloneSlice := func(source *values.NativeSliceLayout) *values.NativeSliceLayout {
		if source == nil {
			return nil
		}
		result := *source
		result.Element = snapshotNativeLayout(source.Element, seen)
		return &result
	}
	copy.Slice = cloneSlice(layout.Slice)
	copy.Array = cloneSlice(layout.Array)
	if layout.Map != nil {
		m := *layout.Map
		m.Key = snapshotNativeLayout(m.Key, seen)
		m.Element = snapshotNativeLayout(m.Element, seen)
		copy.Map = &m
	}
	if layout.Compare != nil {
		comparison := *layout.Compare
		comparison.Steps = slices.Clone(comparison.Steps)
		copy.Compare = &comparison
	}
	return &copy
}

// FieldValueCopy distinguishes a value read from a fused address load.
const FieldValueCopy uint8 = 1
