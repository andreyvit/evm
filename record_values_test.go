package evm

import (
	"reflect"
	"testing"

	"github.com/andreyvit/evm/kvo/codec/row2"
	"github.com/andreyvit/evm/values"
)

func TestRecordFieldCacheStorageLayouts(t *testing.T) {
	first, second := new(Program), new(Program)
	path := []row2.Step{{Key: 1}}
	check := func(a, b *values.Shape, want bool) {
		t.Helper()
		field := &recordFieldValue{program: first, path: path, s: a}
		state := &editorState{fields: []*recordFieldValue{field}}
		if got := state.field(second, b, path) != nil; got != want {
			t.Fatalf("shared field = %v, want %v", got, want)
		}
	}
	rowType := values.NewType("row", values.Pair)
	row := &values.Shape{Type: rowType, Kind: values.TypedPairKind, Cells: 1}
	nativeRow := *row
	nativeRow.Native = &values.NativeLayout{GoType: reflect.TypeFor[*struct{ Name string }]()}
	check(row, &nativeRow, true)
	check(&nativeRow, row, true)
	otherRow := *row
	otherRow.Type = values.NewType("other row", values.Pair)
	check(&nativeRow, &otherRow, false)
	anyNative := &values.NativeLayout{GoType: reflect.TypeFor[any]()}
	anyA := &values.Shape{Type: values.AnyType, Kind: values.InterfaceKind, Cells: 1, Native: anyNative}
	anyB := *anyA
	check(anyA, &anyB, false)
	mapNative := &values.NativeLayout{GoType: reflect.TypeFor[map[string]string](), Map: &values.NativeMapLayout{}}
	mapA := &values.Shape{Type: values.NewType("map[string]string", values.Pointer), Kind: values.MapKind, Cells: 1, Native: mapNative}
	mapB := *mapA
	check(mapA, &mapB, true)
	mapB.Native = &values.NativeLayout{GoType: mapNative.GoType}
	check(mapA, &mapB, false)
	check(&mapB, mapA, false)
}
