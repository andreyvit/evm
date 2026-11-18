package nativebind

import (
	"math"
	"reflect"
	"unsafe"

	"github.com/andreyvit/evm/values"
)

func sliceLayout(t reflect.Type) *values.NativeSliceLayout {
	elem := t.Elem()
	slice := func(p unsafe.Pointer, count uint32) reflect.Value {
		return reflect.SliceAt(elem, p, int(count))
	}
	return &values.NativeSliceLayout{
		Array:       func(n uint64) *values.NativeLayout { return Layout(reflect.ArrayOf(int(n), elem), nil, false) },
		ElementSize: uint64(elem.Size()),
		Load: func(p unsafe.Pointer) values.Value {
			v := reflect.NewAt(t, p).Elem()
			if uint64(v.Cap()) > math.MaxUint32 {
				panic("nativebind: slice exceeds VM capacity")
			}
			return values.SliceValue(v.UnsafePointer(), uint32(v.Len()), uint32(v.Cap()))
		},
		Store: func(p unsafe.Pointer, v values.Value) {
			if v.SliceLen() > v.SliceCap() {
				panic("nativebind: invalid slice length")
			}
			if view := v.NativeSlice(); view != nil && (view.Layout == nil || view.Layout.GoType == nil || view.Layout.GoType.Elem() != elem) {
				panic("nativebind: slice requires matching native storage; explicit conversion required")
			}
			data := v.SliceData()
			dst := reflect.NewAt(t, p).Elem()
			if data == nil {
				if v.SliceCap() != 0 {
					panic("nativebind: nil slice with capacity")
				}
				dst.SetZero()
				return
			}
			dst.Set(slice(data, v.SliceCap()).Slice(0, int(v.SliceLen())))
		},
		Make: func(capacity uint32) unsafe.Pointer {
			return reflect.MakeSlice(t, int(capacity), int(capacity)).UnsafePointer()
		},
		Copy:  func(dst, src unsafe.Pointer, count uint32) { reflect.Copy(slice(dst, count), slice(src, count)) },
		Clear: func(p unsafe.Pointer, count uint32) { slice(p, count).Clear() },
	}
}
