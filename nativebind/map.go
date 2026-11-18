package nativebind

import (
	"reflect"
	"unsafe"

	"github.com/andreyvit/evm/values"
)

func mapLayout(t reflect.Type) *values.NativeMapLayout {
	// A map value is one GC-visible pointer. Copy its identity into a typed
	// reflect.Value before operating, so assignments use Go's write barriers.
	value := func(p unsafe.Pointer) reflect.Value { return reflect.NewAt(t, unsafe.Pointer(&p)).Elem() }
	key := func(p unsafe.Pointer) reflect.Value { return reflect.NewAt(t.Key(), p).Elem() }
	elem := func(p unsafe.Pointer) reflect.Value { return reflect.NewAt(t.Elem(), p).Elem() }
	return &values.NativeMapLayout{
		Load: func(p unsafe.Pointer) values.Value {
			return values.Value{P: reflect.NewAt(t, p).Elem().UnsafePointer()}
		},
		Store: func(p unsafe.Pointer, v values.Value) { reflect.NewAt(t, p).Elem().Set(value(v.P)) },
		Make:  func() unsafe.Pointer { return reflect.MakeMap(t).UnsafePointer() },
		Len:   func(p unsafe.Pointer) int { return value(p).Len() },
		Lookup: func(p, k, out unsafe.Pointer) bool {
			v := value(p).MapIndex(key(k))
			if !v.IsValid() {
				return false
			}
			elem(out).Set(v)
			return true
		},
		Update: func(p, k, v unsafe.Pointer) { value(p).SetMapIndex(key(k), elem(v)) },
		Delete: func(p, k unsafe.Pointer) { value(p).SetMapIndex(key(k), reflect.Value{}) },
		Clear:  func(p unsafe.Pointer) { value(p).Clear() },
		Iter: func(p unsafe.Pointer) values.NativeMapIterator {
			return &nativeMapIterator{iter: value(p).MapRange(), key: t.Key(), elem: t.Elem()}
		},
	}
}

type nativeMapIterator struct {
	iter      *reflect.MapIter
	key, elem reflect.Type
}

func (it *nativeMapIterator) Next(k, v unsafe.Pointer) bool {
	if !it.iter.Next() {
		return false
	}
	reflect.NewAt(it.key, k).Elem().SetIterKey(it.iter)
	reflect.NewAt(it.elem, v).Elem().SetIterValue(it.iter)
	return true
}
