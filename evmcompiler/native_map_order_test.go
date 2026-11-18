package evmcompiler_test

import (
	"context"
	"strings"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

func TestNativeMapKeyDeclarationOrder(t *testing.T) {
	key := &values.Shape{Type: values.NewType("host.Key", values.Raw), Kind: values.IntKind, Bits: 64, Cells: 1}
	dict := &values.Shape{Type: values.NewType("host.Dict", values.Pointer), Kind: values.MapKind, Key: key, Elem: &values.Shape{Type: values.Int64, Kind: values.IntKind, Bits: 64, Cells: 1}, Cells: 1}
	kd := evm.TypeDecl{Name: "Key", Shape: key, ScalarBase: values.Int64}
	md := evm.TypeDecl{Name: "Dict", Shape: dict}
	for _, decls := range [][]evm.TypeDecl{{md, kd}, {kd, md}} {
		pkg := evm.Package{Path: "host", Types: decls}
		mod, err := evmcompiler.Compile("keys.go", `import "host";func Run()int64{v:=host.Dict{host.Key(2):7};return v[2]}`, pkg)
		if err != nil {
			t.Fatal(err)
		}
		program, err := evm.Link(mod, pkg)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]values.Value, 1)
		if err := evm.New(program).Run(context.Background(), "Run", nil, out); err != nil || out[0].AsInt() != 7 {
			t.Fatal(out, err)
		}
	}
}

func TestNativeMapRejectsNoncomparableDeclaredKey(t *testing.T) {
	integer := &values.Shape{Type: values.Int64, Kind: values.IntKind, Bits: 64, Cells: 1}
	keyType := values.NewType("host.Key", values.Pointer)
	keyReference := &values.Shape{Type: keyType, Kind: values.StructKind}
	slice := &values.Shape{Type: values.NewType("[]int64", values.Pair), Kind: values.SliceKind, Elem: integer, Cells: 1}
	keyDefinition := &values.Shape{Type: keyType, Kind: values.StructKind, Fields: []values.Field{{Name: "Items", Shape: slice}}, Cells: 1}
	dict := &values.Shape{Type: values.NewType("host.Dict", values.Pointer), Kind: values.MapKind, Key: keyReference, Elem: integer, Cells: 1}
	pkg := evm.Package{Path: "host", Types: []evm.TypeDecl{{Name: "Dict", Shape: dict}, {Name: "Key", Shape: keyDefinition}}}
	_, err := evmcompiler.Compile("invalid.go", `import "host";func Run(){var v host.Dict;_=v}`, pkg)
	if err == nil || !strings.Contains(err.Error(), "invalid map key") {
		t.Fatal(err)
	}
	entry := evm.Package{Path: "entry", Functions: []evm.ExternalFunction{{Name: "Get", Signature: evm.Signature{Results: []*values.Type{values.AnyType}}, DynamicTypes: []*values.Type{dict.Type}, Call: func(*evm.Call) error { return nil }}}}
	_, err = evmcompiler.Compile("dynamic.go", `import "entry";func Run(){entry.Get()}`, entry, pkg)
	if err == nil || !strings.Contains(err.Error(), "invalid map key") {
		t.Fatal("dynamic map key escaped validation", err)
	}
}
