package nativebind

import (
	"reflect"
	"runtime"
	"testing"
	"unsafe"

	"github.com/andreyvit/evm/values"
)

func TestNativeSliceStoragePreservesAliasing(t *testing.T) {
	type item struct{ Name string }
	type items []*item
	a, b := &item{"a"}, &item{"b"}
	input := items{a, b, nil}
	layout := Layout(reflect.TypeFor[items](), nil, false).Slice
	value := layout.Load(unsafe.Pointer(&input))
	if value.SliceLen() != 3 || value.SliceCap() != 3 {
		t.Fatal(value)
	}
	var alias items
	layout.Store(unsafe.Pointer(&alias), values.SliceValue(value.P, 2, 3))
	alias[0] = b
	if input[0] != b {
		t.Fatal("slice store copied the backing array")
	}
	// Copy overlapping pointer-bearing regions through Go's write barriers.
	layout.Copy(unsafe.Add(value.P, layout.ElementSize), value.P, 2)
	if input[1] != b || input[2] != b {
		t.Fatal(input)
	}
	layout.Clear(value.P, 2)
	if input[0] != nil || input[1] != nil || input[2] != b {
		t.Fatal(input)
	}
	allocated := layout.Make(2)
	var result items
	layout.Store(unsafe.Pointer(&result), values.SliceValue(allocated, 2, 2))
	result[0] = a
	runtime.GC()
	if result[0].Name != "a" {
		t.Fatal("lost pointer-bearing storage")
	}
	var empty items
	layout.Store(unsafe.Pointer(&empty), values.SliceValue(layout.Make(0), 0, 0))
	if empty == nil {
		t.Fatal("non-nil empty slice became nil")
	}
	layout.Store(unsafe.Pointer(&empty), values.Value{})
	if empty != nil {
		t.Fatal("nil slice became non-nil")
	}
}
