package evm

import (
	"math"
	"unsafe"

	"github.com/andreyvit/evm/values"
)

// Packed backing is private to an alias group verified by the compiler.
// Ordinary ABI values and addressable aggregates use Value cells.
type sliceStorage uint8

const (
	cellStorage sliceStorage = iota
	byteStorage
	wordStorage
	pointerStorage
)

func storageFor(elem *values.Shape, packed bool) sliceStorage {
	if !packed {
		return cellStorage
	}
	if elem.Kind == values.PointerKind {
		return pointerStorage
	}
	if elem.Kind == values.BoolKind || elem.Kind == values.UintKind && elem.Bits == 8 {
		return byteStorage
	}
	return wordStorage
}
func packable(s *values.Shape) bool {
	return s != nil && (s.Kind == values.PointerKind && s.Type.Repr() == values.Pointer || s.Type.Repr() == values.Raw && (s.Kind == values.IntKind || s.Kind == values.UintKind || s.Kind == values.BoolKind || s.Kind == values.FloatKind))
}
func allocateElements[T any](n uint64, used *allocationUsage, l Limits) (unsafe.Pointer, error) {
	var v T
	size := uint64(unsafe.Sizeof(v))
	n = max(n, 1) // A non-nil empty slice still has a distinct addressable backing.
	if n > math.MaxInt/size {
		return nil, ErrAllocation
	}
	if err := charge(n*size, used, l); err != nil {
		return nil, err
	}
	data := make([]T, int(n))
	return unsafe.Pointer(&data[0]), nil
}
func (st sliceStorage) allocate(n, cells uint64, used *allocationUsage, l Limits) (unsafe.Pointer, error) {
	switch st {
	case byteStorage:
		return allocateElements[byte](n, used, l)
	case wordStorage:
		return allocateElements[uint64](n, used, l)
	case pointerStorage:
		return allocateElements[unsafe.Pointer](n, used, l)
	default:
		if cells != 0 && n > math.MaxUint64/cells {
			return nil, ErrAllocation
		}
		return allocCells(n*cells, used, l)
	}
}
func (st sliceStorage) at(p unsafe.Pointer, i, cells uint64) unsafe.Pointer {
	switch st {
	case byteStorage:
		return unsafe.Add(p, i)
	case wordStorage:
		return unsafe.Add(p, i*8)
	case pointerStorage:
		return unsafe.Add(p, uintptr(i)*unsafe.Sizeof(p))
	default:
		return values.CellAt(p, i*cells)
	}
}
func (st sliceStorage) copy(dst, src unsafe.Pointer, n, cells uint64) {
	switch st {
	case byteStorage:
		copy(unsafe.Slice((*byte)(dst), n), unsafe.Slice((*byte)(src), n))
	case wordStorage:
		copy(unsafe.Slice((*uint64)(dst), n), unsafe.Slice((*uint64)(src), n))
	case pointerStorage:
		copy(unsafe.Slice((*unsafe.Pointer)(dst), n), unsafe.Slice((*unsafe.Pointer)(src), n))
	default:
		copy(values.CellSlice(dst, n*cells), values.CellSlice(src, n*cells))
	}
}
func (st sliceStorage) clear(p unsafe.Pointer, n, cells uint64) {
	switch st {
	case byteStorage:
		clear(unsafe.Slice((*byte)(p), n))
	case wordStorage:
		clear(unsafe.Slice((*uint64)(p), n))
	case pointerStorage:
		clear(unsafe.Slice((*unsafe.Pointer)(p), n))
	default:
		clear(values.CellSlice(p, n*cells))
	}
}
func (st sliceStorage) load(p unsafe.Pointer) values.Value {
	switch st {
	case byteStorage:
		return values.Uint(uint64(*(*byte)(p)))
	case pointerStorage:
		return values.Value{P: *(*unsafe.Pointer)(p)}
	default:
		return values.Uint(*(*uint64)(p))
	}
}
func (st sliceStorage) store(p unsafe.Pointer, v values.Value) {
	switch st {
	case byteStorage:
		*(*byte)(p) = byte(v.R)
	case pointerStorage:
		*(*unsafe.Pointer)(p) = v.P
	default:
		*(*uint64)(p) = v.R
	}
}

// collectionStorage selects the backing layout of a declared slice. Native
// slices retain Go element storage, while ordinary guest slices retain cells.
type collectionStorage struct {
	elem *values.Shape
	sliceStorage
	native *values.NativeSliceLayout
}

func collectionStorageFor(s *values.Shape, packed bool) collectionStorage {
	st := collectionStorage{sliceStorage: storageFor(s.Elem, packed), elem: s.Elem}
	if s.Native != nil {
		if s.Kind == values.SliceKind {
			st.native = s.Native.Slice
		}
		if s.Kind == values.ArrayKind {
			st.native = s.Native.Array
		}
	}
	return st
}
func (st collectionStorage) allocate(n, cells uint64, used *allocationUsage, l Limits) (unsafe.Pointer, error) {
	if st.native == nil {
		return st.sliceStorage.allocate(n, cells, used, l)
	}
	if n > math.MaxUint32 || st.native.ElementSize != 0 && n > math.MaxInt/st.native.ElementSize {
		return nil, ErrAllocation
	}
	if err := charge(max(n, 1)*st.native.ElementSize, used, l); err != nil {
		return nil, err
	}
	return st.native.Make(uint32(n)), nil
}
func (st collectionStorage) at(p unsafe.Pointer, i, cells uint64) unsafe.Pointer {
	if st.native != nil {
		return unsafe.Add(p, i*st.native.ElementSize)
	}
	return st.sliceStorage.at(p, i, cells)
}
func (st collectionStorage) copy(dst, src unsafe.Pointer, n, cells uint64) {
	if st.native != nil {
		st.native.Copy(dst, src, uint32(n))
		return
	}
	st.sliceStorage.copy(dst, src, n, cells)
}
func (st collectionStorage) clear(p unsafe.Pointer, n, cells uint64) {
	if st.native != nil {
		st.native.Clear(p, uint32(n))
		return
	}
	st.sliceStorage.clear(p, n, cells)
}

// Scalar conversions use the declared native width, never the cell stride.
func (st collectionStorage) scalarStore(p unsafe.Pointer, v values.Value) {
	if st.native != nil {
		st.elem.Native.Store(p, v)
	} else {
		st.elem.Store(p, v)
	}
}

// scalarSliceReader resolves the backing pointer, element width and decoder
// together. A view carries its own physical element type.
type scalarSliceReader struct {
	data   unsafe.Pointer
	stride uint64
	load   func(unsafe.Pointer) values.Value
}

func readScalarSlice(s *values.Shape, v values.Value) scalarSliceReader {
	if view := v.NativeSlice(); view != nil {
		return scalarSliceReader{view.Data, view.Layout.Slice.ElementSize, view.Element.Load}
	}
	if s.Native != nil && s.Native.Slice != nil {
		return scalarSliceReader{v.P, s.Native.Slice.ElementSize, s.Elem.Native.Load}
	}
	return scalarSliceReader{v.P, s.Elem.Cells * uint64(unsafe.Sizeof(values.Value{})), s.Elem.Stored}
}
func (r scalarSliceReader) at(i uint64) values.Value {
	return r.load(unsafe.Add(r.data, i*r.stride))
}
