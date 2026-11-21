package evm

import (
	"reflect"
	"sync"
	"testing"
	"unsafe"

	"github.com/andreyvit/evm/nativebind"
	"github.com/andreyvit/evm/values"
)

func TestNativeArrayViewMetadataReuse(t *testing.T) {
	p := new(Program)
	element := nativebind.Layout(reflect.TypeFor[*int](), nil, false)
	layout := nativebind.Layout(reflect.TypeFor[[]*int](), nil, false)
	view := &values.NativeSliceView{Layout: layout, Element: element, Length: 2, Capacity: 2}
	row := &values.Shape{Type: values.NewType("row", values.Pair), Kind: values.TypedPairKind, Cells: 1}
	array := &values.Shape{Type: values.NewType("[2]row", values.Pointer), Kind: values.ArrayKind, Cells: 2, Len: 2, Elem: row}
	for range 100 {
		native := p.nativeArrayView(view, 2)
		address := p.nativeViewAddress(array, native.array, nil)
		physical := p.nativeViewAddressShape(address.R)
		if p.nativeArraySlice(physical) != layout {
			t.Fatal("array view lost original slice layout")
		}
	}
	if len(p.viewTable().shapes) != 1 || len(p.viewTable().arrays) != 1 {
		t.Fatalf("metadata grows per invocation: %d addresses, %d arrays", len(p.viewTable().shapes), len(p.viewTable().arrays))
	}
}

func TestNativeSlotLayoutSnapshot(t *testing.T) {
	element := nativebind.Layout(reflect.TypeFor[*int](), nil, false)
	element.Codec = &values.NativeValueCodec{Decode: func(v values.Value) (values.Value, error) { return v, nil }}
	layout := nativebind.Layout(reflect.TypeFor[[]*int](), nil, false)
	layout.Slice.Element = element
	root := &values.NativeLayout{Fields: []values.NativeField{{Layout: layout}, {Layout: layout}}, Map: &values.NativeMapLayout{Key: element, Element: layout}}
	// Nested layouts can refer back to an owning layout.
	element.Fields = []values.NativeField{{Layout: root}}
	copy := snapshotNativeLayout(root, make(map[*values.NativeLayout]*values.NativeLayout))
	first := copy.Fields[0].Layout
	if first == layout || first != copy.Fields[1].Layout || first.Slice == layout.Slice || first.Slice.Element == element || first.Slice.Element.Fields[0].Layout != copy || copy.Map.Element != first || copy.Map.Key != first.Slice.Element {
		t.Fatal("physical layout snapshot lost independence, sharing or cycles")
	}
	layout.Slice.Element = nil
	element.Codec.Decode = nil
	root.Fields[0].Layout = nil
	if first.Slice.Element.Codec.Decode == nil || copy.Fields[0].Layout == nil {
		t.Fatal("declaration mutation changed linked metadata")
	}
}

type namedIntPointers []*int

func TestNativeArrayViewUnnamedSlice(t *testing.T) {
	element := nativebind.Layout(reflect.TypeFor[*int](), nil, false)
	named := nativebind.Layout(reflect.TypeFor[namedIntPointers](), nil, false)
	plain := nativebind.Layout(reflect.TypeFor[[]*int](), nil, false)
	row := &values.Shape{Type: values.NewType("row", values.Pair), Kind: values.TypedPairKind, Cells: 1}
	array := &values.Shape{Type: values.NewType("[1]row", values.Pointer), Kind: values.ArrayKind, Cells: 1, Len: 1, Elem: row}
	for _, order := range [][]*values.NativeLayout{{named, plain}, {plain, named}} {
		p := new(Program)
		for _, layout := range order {
			view := &values.NativeSliceView{Layout: layout, Element: element, Length: 1, Capacity: 1}
			native := p.nativeArrayView(view, 1)
			address := p.nativeViewAddress(array, native.array, nil)
			slice := p.nativeArraySlice(p.nativeViewAddressShape(address.R))
			if slice.GoType != reflect.TypeFor[[]*int]() {
				t.Fatalf("array slice got %v", slice.GoType)
			}
		}
	}
}

func TestNativeCollectionAllocationValidation(t *testing.T) {
	element := nativebind.Layout(reflect.TypeFor[*int](), nil, false)
	element.Codec = &values.NativeValueCodec{
		Decode: func(v values.Value) (values.Value, error) { return v, nil },
		Encode: func(v values.Value, _ func(uint64) error) (values.Value, error) { return v, nil },
	}
	layout := nativebind.Layout(reflect.TypeFor[[]*int](), nil, false)
	layout.Slice.Element = element
	row := &values.Shape{Type: values.NewType("row", values.Pair), Kind: values.TypedPairKind, Cells: 1, NativeAllocation: element}
	slice := &values.Shape{Type: values.NewType("[]row", values.Pair), Kind: values.SliceKind, Cells: 1, Elem: row, NativeAllocation: layout}
	if _, err := snapshotShape(slice, make(map[*values.Shape]*values.Shape)); err != nil {
		t.Fatal(err)
	}
	layout.Slice.ElementSize++
	if _, err := snapshotShape(slice, make(map[*values.Shape]*values.Shape)); err == nil {
		t.Fatal("invalid pointer stride accepted")
	}
	layout.Slice.ElementSize--
	element.Codec.Encode = nil
	if _, err := snapshotShape(slice, make(map[*values.Shape]*values.Shape)); err == nil {
		t.Fatal("incomplete element codec accepted")
	}
}

func TestConcurrentNativeViewMetadata(t *testing.T) {
	p := new(Program)
	element := nativebind.Layout(reflect.TypeFor[*int](), nil, false)
	layout := nativebind.Layout(reflect.TypeFor[[]*int](), nil, false)
	layout.Slice.Element = element
	view := &values.NativeSliceView{Layout: layout, Element: element, Length: 8, Capacity: 8}
	row := &values.Shape{Type: values.NewType("row", values.Pair), Kind: values.TypedPairKind, Cells: 1}
	var workers sync.WaitGroup
	for n := uint64(1); n <= 8; n++ {
		array := &values.Shape{Type: values.NewType("array", values.Pointer), Kind: values.ArrayKind, Cells: n, Len: n, Elem: row}
		workers.Go(func() {
			for range 100 {
				native := p.nativeArrayView(view, n)
				address := p.nativeViewAddress(array, native.array, nil)
				physical := p.nativeViewAddressShape(address.R)
				if physical.Len != n || p.nativeArraySlice(physical).GoType != layout.GoType {
					t.Error("metadata changed across concurrent views")
					return
				}
			}
		})
	}
	workers.Wait()
	if len(p.viewTable().shapes) != 8 || len(p.viewTable().arrays) != 8 {
		t.Fatal("unbounded metadata growth")
	}
}

func TestStringFromNativeRuneView(t *testing.T) {
	runes := []rune("aλ")
	layout := nativebind.Layout(reflect.TypeOf(runes), nil, false)
	elem := nativebind.Layout(reflect.TypeFor[rune](), nil, false)
	view := values.NativeSliceValue(&values.NativeSliceView{Layout: layout, Element: elem, Data: unsafe.Pointer(&runes[0]), Length: 2, Capacity: 2})
	shape := &values.Shape{Kind: values.SliceKind, Elem: &values.Shape{Kind: values.IntKind, Bits: 32, Cells: 1}}
	budget := executionBudget{limits: Limits{AllocationBytes: 100, Allocations: 10}}
	got, err := convertExtended(&values.Shape{Kind: values.StringKind}, shape, view, &budget)
	if err != nil || got.AsString() != string(runes) {
		t.Fatal(got, err)
	}
}
