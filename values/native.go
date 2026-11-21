package values

import (
	"bytes"
	"fmt"
	"math"
	"reflect"
	"unsafe"
)

// NativeAddress marks a pointer into live Go memory. A pointer with R=0 refers
// to VM cells. The pointer's static type determines whether it carries this
// discriminator; opaque interface receivers continue to use their own ABI.
const NativeAddress uint64 = 1

// NativeBorrow marks an aggregate borrowed from a host. A typed VM boundary
// materializes it before assigning it to an aggregate register.
const NativeBorrow uint64 = ^uint64(0)

// BorrowNative retains the Go type with its address until a host value is imported.
func BorrowNative(typ reflect.Type, p unsafe.Pointer) Value {
	ptr := reflect.NewAt(typ, p)
	return Value{R: NativeBorrow, P: unsafe.Pointer(&ptr)}
}

// BorrowedNative returns the typed pointer created by BorrowNative.
func (v Value) BorrowedNative() reflect.Value { return *(*reflect.Value)(v.P) }

// NativeValueCodec converts a native slot to its logical VM value. The encode
// callback charges any new native storage before allocating it.
type NativeValueCodec struct {
	Decode func(Value) (Value, error)
	Encode func(Value, func(uint64) error) (Value, error)
}

// NativeAddressCodec preserves a logical pointer representation for native storage.
type NativeAddressCodec struct {
	Encode func(unsafe.Pointer) Value
	Decode func(Value) (unsafe.Pointer, error)
}

// NativeLayout supplies operations on Go memory, independently of VM cells.
// Load returns decoded scalars/strings, native pointer targets, or a borrowed
// native aggregate address. VM aggregate conversion remains an explicit step.
type NativeLayout struct {
	Address *NativeAddressCodec
	Codec   *NativeValueCodec
	// OpaqueCell is the one-based cell holding an immutable native snapshot
	// for fields omitted from the script type. Zero means no snapshot.
	OpaqueCell uint64
	Compare    *NativeCompare
	GoType     reflect.Type
	Array      *NativeSliceLayout
	Map        *NativeMapLayout
	Slice      *NativeSliceLayout
	Size       uint64
	New        func() unsafe.Pointer
	Load       func(unsafe.Pointer) Value
	Store      func(unsafe.Pointer, Value)
	Fields     []NativeField
}
type NativeField struct {
	// Layout records physical slot storage when it differs from the logical shape.
	Layout *NativeLayout
	// Cells marks VM cells embedded in synthetic aggregate storage.
	// Their addresses and values keep the ordinary guest representation.
	Cells  bool
	Offset uintptr
	Get    func(unsafe.Pointer) Value
	Set    func(unsafe.Pointer, Value)
}

// NativeStorage reports whether this shape supports live native allocation.
// VM collection and interface layouts remain distinct from Go storage.
func (s *Shape) NativeStorage() bool {
	if s.Native == nil {
		return false
	}
	switch s.Kind {
	case TypedPairKind:
		return s.Native.Codec != nil
	case SliceKind:
		return s.Native.Slice != nil && s.Native.Slice.Element != nil
	case BoolKind, IntKind, UintKind, FloatKind, StringKind, StructKind, PointerKind, ArrayKind, TimestampKind:
		return true
	}
	return false
}

// NativeSliceLayout operates on Go slice storage. VM values retain the backing
// array pointer and Go length/capacity; element addresses use native byte strides.
// Copy and Clear must retain Go write barriers for pointer-bearing elements.
type NativeSliceLayout struct {
	Element     *NativeLayout
	Array       func(uint64) *NativeLayout
	ElementSize uint64
	Load        func(unsafe.Pointer) Value
	Store       func(unsafe.Pointer, Value)
	Make        func(uint32) unsafe.Pointer
	Copy        func(dst, src unsafe.Pointer, count uint32)
	Clear       func(unsafe.Pointer, uint32)
}

// NativeMapLayout operates on a Go map identity, not a field holding that map.
// Reassigning the source field therefore does not retarget existing aliases.
type NativeMapLayout struct {
	Key, Element *NativeLayout
	Load         func(unsafe.Pointer) Value
	Store        func(unsafe.Pointer, Value)
	Make         func() unsafe.Pointer
	Len          func(unsafe.Pointer) int
	Lookup       func(mapPtr, key, result unsafe.Pointer) bool
	Update       func(mapPtr, key, value unsafe.Pointer)
	Delete       func(mapPtr, key unsafe.Pointer)
	Clear        func(unsafe.Pointer)
	Iter         func(unsafe.Pointer) NativeMapIterator
}
type NativeMapIterator interface {
	Next(key, value unsafe.Pointer) bool
}

// NativeCompare preserves Go field order when an opaque snapshot supplies
// fields omitted from the script layout. A nonnegative Field selects VM cells.
type NativeCompare struct {
	Comparable bool
	Zero       unsafe.Pointer
	Steps      []NativeCompareStep
}
type NativeCompareStep struct {
	Field int
	Equal func(unsafe.Pointer, unsafe.Pointer) bool
	Hash  func(unsafe.Pointer) uint64
	Work  func(unsafe.Pointer, uint64) (uint64, bool)
}

// NativeSliceView carries physical storage per value. Linking a host slice
// declaration must not change the backing layout of ordinary guest slices.
// Length and Capacity retain the full uint32 range of ordinary slice headers.
type NativeSliceView struct {
	Data             unsafe.Pointer
	Layout           *NativeLayout
	Element          *NativeLayout
	Length, Capacity uint32
}

// A second impossible slice header (len 2, cap 0) carries only the byte
// offset and bounds. Ordinary native views keep their original size.
const borrowedByteSliceTag = 2

type borrowedByteView struct {
	backing                  *borrowedByteBacking
	offset, length, capacity uint32
}
type borrowedByteBacking struct {
	data   []byte
	layout *NativeLayout
	budget OwnershipBudget // cleared on detachment
}

// BorrowByteSlice exposes exactly len(data) bytes. Binding declarations must
// describe the result's borrow domain so the compiler can own escaping values.
func BorrowByteSlice(data []byte, layout *NativeLayout, budget OwnershipBudget) (Value, error) {
	if data == nil {
		return Value{}, nil
	}
	if uint64(len(data)) > math.MaxUint32 {
		return Value{}, fmt.Errorf("borrowed byte slice exceeds VM capacity")
	}
	type header struct {
		view    borrowedByteView
		backing borrowedByteBacking
	}
	if err := budget.ChargeAllocation(uint64(unsafe.Sizeof(header{}))); err != nil {
		return Value{}, err
	}
	h := &header{backing: borrowedByteBacking{data[:len(data):len(data)], layout, budget}}
	n := uint32(len(data))
	h.view = borrowedByteView{&h.backing, 0, n, n}
	return Value{R: borrowedByteSliceTag, P: unsafe.Pointer(&h.view)}, nil
}

func (v Value) byteView() *borrowedByteView { return (*borrowedByteView)(v.P) }

// HasNativeSlice reports whether the value carries per-slice physical metadata.
func (v Value) HasNativeSlice() bool     { return (v.R == 1 || v.R == borrowedByteSliceTag) && v.P != nil }
func (v Value) IsBorrowedByteView() bool { return v.R == borrowedByteSliceTag && v.P != nil }

// OwnBytes switches all aliases to one writable array. A failed reservation
// leaves every view attached to the original storage.
func (v Value) OwnBytes() error {
	if !v.IsBorrowedByteView() {
		return nil
	}
	b := v.byteView().backing
	if b.budget == nil {
		return nil
	}
	if err := b.budget.ChargeAllocation(uint64(len(b.data))); err != nil {
		return err
	}
	b.data = bytes.Clone(b.data)
	b.budget = nil
	return nil
}

// SliceView creates a subview with the original physical
// layout. Borrowed offsets resolve against the shared backing on every access.
func (v Value) SliceView(lo, length, capacity uint32, b OwnershipBudget) (Value, error) {
	if !v.HasNativeSlice() || length > capacity || uint64(lo)+uint64(capacity) > uint64(v.SliceCap()) {
		return Value{}, fmt.Errorf("invalid native slice bounds")
	}
	if v.IsBorrowedByteView() {
		if err := b.ChargeAllocation(uint64(unsafe.Sizeof(borrowedByteView{}))); err != nil {
			return Value{}, err
		}
		sub := *v.byteView()
		sub.length, sub.capacity = length, capacity
		if capacity != 0 {
			sub.offset += lo
		}
		return Value{R: borrowedByteSliceTag, P: unsafe.Pointer(&sub)}, nil
	}
	if err := b.ChargeAllocation(uint64(unsafe.Sizeof(NativeSliceView{}))); err != nil {
		return Value{}, err
	}
	sub := *v.NativeSlice()
	sub.Length, sub.Capacity = length, capacity
	if capacity != 0 {
		sub.Data = unsafe.Add(sub.Data, uint64(lo)*sub.Layout.Slice.ElementSize)
	}
	return NativeSliceValue(&sub), nil
}

// NativeSliceValue retains a native view. Callers account for its allocation.
func NativeSliceValue(view *NativeSliceView) Value {
	if view == nil {
		return Value{}
	}
	return Value{R: 1, P: unsafe.Pointer(view)}
}

// NativeSlice returns a view suitable for exposing writable native memory.
func (v Value) NativeSlice() *NativeSliceView {
	if v.IsBorrowedByteView() {
		if err := v.OwnBytes(); err != nil {
			panic(err)
		}
		return v.byteView().nativeView()
	}
	if v.R == 1 && v.P != nil {
		return (*NativeSliceView)(v.P)
	}
	return nil
}

// ReadNativeSlice resolves physical metadata for an immediate read. Its Data
// pointer is valid until an alias detaches.
func (v Value) ReadNativeSlice() *NativeSliceView {
	if v.R == 1 && v.P != nil {
		return (*NativeSliceView)(v.P)
	}
	if v.IsBorrowedByteView() {
		return v.byteView().nativeView()
	}
	return nil
}
func (b *borrowedByteView) nativeView() *NativeSliceView {
	layout := b.backing.layout
	return &NativeSliceView{Data: b.data(), Layout: layout, Element: layout.Slice.Element, Length: b.length, Capacity: b.capacity}
}
func (b *borrowedByteView) data() unsafe.Pointer {
	return unsafe.Add(unsafe.Pointer(unsafe.SliceData(b.backing.data)), b.offset)
}

// SliceData returns the writable element backing address.
func (v Value) SliceData() unsafe.Pointer {
	if err := v.OwnBytes(); err != nil {
		panic(err)
	}
	return v.SliceReadData()
}

// SliceReadData returns a transient address for reads or nil tests.
func (v Value) SliceReadData() unsafe.Pointer {
	if v.R == 1 && v.P != nil {
		return (*NativeSliceView)(v.P).Data
	}
	if v.IsBorrowedByteView() {
		return v.byteView().data()
	}
	return v.P
}
