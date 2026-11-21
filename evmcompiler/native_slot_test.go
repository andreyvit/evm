package evmcompiler_test

import (
	"context"
	"reflect"
	"testing"
	"unsafe"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/nativebind"
	"github.com/andreyvit/evm/values"
)

func TestNativeFieldPointerUsesGoStorage(t *testing.T) {
	type Row struct {
		N    int32
		Next int32
		A    [4]byte
		Z    complex128
	}
	row := Row{Next: 19}
	i32 := &values.Shape{Type: values.Int32, Kind: values.IntKind, Bits: 32, Cells: 1}
	u8 := &values.Shape{Type: values.Uint8, Kind: values.UintKind, Bits: 8, Cells: 1}
	arr := &values.Shape{Type: values.NewType("[4]uint8", values.Pointer), Kind: values.ArrayKind, Elem: u8, Len: 4, Cells: 4}
	f64 := &values.Shape{Type: values.Float64, Kind: values.FloatKind, Bits: 64, Cells: 1}
	z := &values.Shape{Type: values.NewType("complex128", values.Pointer), Kind: values.ComplexKind, Bits: 128, Cells: 2, Elem: f64}
	s := &values.Shape{Type: values.NewType("host.Row", values.Pointer), Kind: values.StructKind, Cells: 8, Fields: []values.Field{{Name: "N", Shape: i32}, {Name: "Next", Shape: i32, Offset: 1}, {Name: "A", Shape: arr, Offset: 2}, {Name: "Z", Shape: z, Offset: 6}}}
	typ := reflect.TypeOf(row)
	s.Native = nativebind.Layout(typ, nativebind.BindFields(typ, nil, nativebind.Automatic), false)
	ptr := values.NewPointerShape(s)
	ptr.Type = values.NewType("*host.Row", values.Pair)
	ptr.Native = nativebind.Layout(reflect.PointerTo(typ), nil, false)
	host := evm.Package{Path: "host", Types: []evm.TypeDecl{{Name: "Row", Shape: s}, {Shape: ptr}}, Functions: []evm.ExternalFunction{{Name: "Get", Signature: evm.Signature{Results: []*values.Type{ptr.Type}}, Call: func(c *evm.Call) error {
		c.SetResult(0, values.Value{R: values.NativeAddress, P: unsafe.Pointer(&row)})
		return nil
	}}}}
	module, err := evmcompiler.Compile("row.go", `import "host"
func set(p *int32) { *p = 7 }
func setZ(p *complex128) { *p = complex(1,2) }
func Run() int32 { r := host.Get(); set(&r.N); setZ(&r.Z); r.A[:][1] = 9; return r.N + int32(imag(r.Z)) }`, host)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, host)
	if err != nil {
		t.Fatal(err)
	}
	var result [1]values.Value
	if err := evm.New(program).Run(context.Background(), "Run", nil, result[:]); err != nil {
		t.Fatal(err)
	}
	if result[0].R != 9 || row != (Row{N: 7, Next: 19, A: [4]byte{0, 9}, Z: complex(1, 2)}) {
		t.Fatalf("result %d, row %+v", result[0].R, row)
	}
}

func TestNamedNativeCollectionConversion(t *testing.T) {
	type IDs []int32
	type Counts map[string]int32
	ids := IDs{3}
	counts := Counts{"a": 6}
	elem := &values.Shape{Type: values.Int32, Kind: values.IntKind, Bits: 32, Cells: 1}
	key := &values.Shape{Type: values.StringType, Kind: values.StringKind, Cells: 1}
	seq := &values.Shape{Type: values.NewType("host.IDs", values.Pair), Kind: values.SliceKind, Elem: elem, Cells: 1, Native: nativebind.Layout(reflect.TypeOf(ids), nil, false)}
	dict := &values.Shape{Type: values.NewType("host.Counts", values.Pointer), Kind: values.MapKind, Key: key, Elem: elem, Cells: 1, Native: nativebind.Layout(reflect.TypeOf(counts), nil, false)}
	host := evm.Package{Path: "host", Types: []evm.TypeDecl{{Name: "IDs", Shape: seq}, {Name: "Counts", Shape: dict}}}
	module, err := evmcompiler.Compile("convert.go", `import "host"
func Run(ids host.IDs, counts host.Counts) int32 {
 s := []int32(ids)
 s[0]++
 m := map[string]int32(counts)
 m["b"] = s[0]
 return int32(len(m)) + m["a"]
}`, host)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, host)
	if err != nil {
		t.Fatal(err)
	}
	args := []values.Value{nativebind.Load(reflect.TypeOf(ids), unsafe.Pointer(&ids)), nativebind.Load(reflect.TypeOf(counts), unsafe.Pointer(&counts))}
	var results [1]values.Value
	if err := evm.New(program).Run(context.Background(), "Run", args, results[:]); err != nil {
		t.Fatal(err)
	}
	if results[0].R != 8 || ids[0] != 4 || counts["b"] != 4 {
		t.Fatal(results, ids, counts)
	}
}
