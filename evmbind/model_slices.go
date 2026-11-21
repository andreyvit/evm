package evmbind

import (
	"fmt"
	"github.com/andreyvit/evm/nativebind"
	"github.com/andreyvit/evm/values"
	"reflect"
	"unsafe"
)

// NativeModelSliceResult preserves the backing of a generated native result.
func (f *Frame) NativeModelSliceResult(name string, data unsafe.Pointer, length, capacity int) values.Value {
	return f.modelSliceResult(f.s.src.nodes[name], data, length, capacity)
}

func (f *Frame) decodeModelSlice(n *node, v values.Value) reflect.Value {
	typ := n.nativeLayout.GoType
	if v.SliceData() == nil {
		return reflect.Zero(typ)
	}
	if view := v.NativeSlice(); view != nil && view.Layout.GoType.Elem() == typ.Elem() {
		return reflect.SliceAt(typ.Elem(), view.Data, int(view.Capacity)).Slice(0, int(view.Length)).Convert(typ)
	}
	panic(fmt.Errorf("model slice %s has incompatible storage; explicit conversion required", n.name))
}

func (f *Frame) encodeModelSlice(n *node, x reflect.Value) values.Value {
	return f.modelSliceResult(n, x.UnsafePointer(), x.Len(), x.Cap())
}
func (f *Frame) modelSliceResult(n *node, data unsafe.Pointer, length, capacity int) values.Value {
	if uint64(capacity) > 1<<32-1 {
		panic("native slice exceeds VM capacity")
	}
	if n.nativeLayout == nil || n.elem.nativeLayout == nil {
		panic(fmt.Errorf("missing model slice layout %s", n.name))
	}
	v, err := f.VM.NativeSlice(n.nativeLayout, n.elem.nativeLayout, data, uint32(length), uint32(capacity))
	if err != nil {
		panic(err)
	}
	return v
}

func isModelSlice(n *node) bool {
	return n.kind == nodeSlice && (n.elem.kind == nodeRecord || isModelSlice(n.elem))
}
func modelSliceLayout(n *node, native map[*node]reflect.Type, direct bool) *values.NativeLayout {
	if n.nativeLayout != nil {
		return n.nativeLayout
	}
	n.nativeLayout = nativebind.Layout(native[n], nil, direct)
	n.nativeLayout.Slice.Element = modelSliceLayout(n.elem, native, direct)
	return n.nativeLayout
}

// NativeSliceData checks a native view before passing its backing array to Go.
// Ordinary native slice carriers keep their existing direct-storage contract.
func NativeSliceData(v values.Value, element reflect.Type) unsafe.Pointer {
	if v.SliceLen() > v.SliceCap() {
		panic("invalid native slice")
	}
	if view := v.NativeSlice(); view != nil {
		if view.Layout == nil || view.Layout.GoType == nil || view.Layout.GoType.Elem() != element {
			panic("slice requires matching native storage; explicit conversion required")
		}
	}
	return v.SliceData()
}
