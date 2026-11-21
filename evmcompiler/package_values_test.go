package evmcompiler_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
)

func TestHostPackageValues(t *testing.T) {
	table := values.NewType("db.Table", values.Raw)
	shape := &values.Shape{Type: table, Kind: values.UintKind, Bits: 64, Cells: 1}
	calls := 0
	pkg := evm.Package{Path: "db", Types: []evm.TypeDecl{{Name: "Table", Shape: shape}},
		Values: []evm.ExternalValue{{Name: "Customers", Type: table, Read: func(c *evm.VM) error {
			calls++
			c.SetCallResult(0, values.Uint(uint64(calls)))
			return nil
		}}},
		Methods: []evm.ExternalMethod{{Name: "ID", Receiver: table, Signature: evm.Signature{Results: []*values.Type{values.Uint64}}, Call: func(c *evm.VM) error {
			c.SetCallResult(0, c.CallArgument(0))
			return nil
		}}},
	}
	m, err := evmcompiler.Compile("test.go", `import "db"; func Run() uint64 { first := db.Customers; return first.ID()*10 + db.Customers.ID() }`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, pkg)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]values.Value, 1)
	if err := evmtest.New(p).Run(context.Background(), "Run", nil, out); err != nil {
		t.Fatal(err)
	}
	if out[0].R != 12 || calls != 2 {
		t.Fatalf("result=%d reads=%d", out[0].R, calls)
	}
	source, err := evmcompiler.PackageSource("db", pkg)
	if err != nil || !strings.Contains(source, "var Customers Table") {
		t.Fatalf("%s: %v", source, err)
	}
	for _, body := range []string{`db.Customers = 4`, `func(p *db.Table) { *p = 3 }(&db.Customers)`} {
		_, err := evmcompiler.Compile("test.go", `import "db"; func Run() { `+body+` }`, pkg)
		if err == nil || !strings.Contains(err.Error(), "host-owned") {
			t.Fatalf("%s: %v", body, err)
		}
	}
}

func TestHostPackagePointerValue(t *testing.T) {
	row := values.NewStructShape("host.Row", values.Field{Name: "Count", Shape: &values.Shape{Type: values.Int64, Kind: values.IntKind, Bits: 64, Cells: 1}})
	ptr := values.NewPointerShape(row)
	cells := []values.Value{values.Int(7)}
	pkg := evm.Package{Path: "host", Types: []evm.TypeDecl{{Name: "Row", Shape: row}, {Shape: ptr}}, Values: []evm.ExternalValue{{Name: "Current", Type: ptr.Type, Read: func(c *evm.VM) error {
		c.SetCallResult(0, values.Value{P: unsafe.Pointer(&cells[0])})
		return nil
	}}}}
	for _, noOptimization := range []bool{false, true} {
		cells[0] = values.Int(7)
		m, err := (evmcompiler.Options{NoOptimization: noOptimization}).Compile("test.go", `import "host"; func Run() int64 { p := host.Current; p.Count += 2; return host.Current.Count }`, pkg)
		if err != nil {
			t.Fatal(err)
		}
		p, err := evm.Link(m, pkg)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]values.Value, 1)
		if err := evmtest.New(p).Run(context.Background(), "Run", nil, out); err != nil {
			t.Fatal(err)
		}
		if out[0].AsInt() != 9 || cells[0].AsInt() != 9 {
			t.Fatalf("lost pointer alias: %v %v", out, cells)
		}
	}
}

func TestHostStructFieldSharedScalar(t *testing.T) {
	id := values.NewType("ids.ID", values.Raw)
	ids := evm.Package{Path: "ids", Source: "package ids; type ID uint64", SharedTypes: []evm.SharedType{{Name: "ID", Native: reflect.TypeFor[uint64](), Type: id}}}
	row := values.NewStructShape("host.Row", values.Field{Name: "ID", Shape: &values.Shape{Type: id, Kind: values.UintKind, Bits: 64, Cells: 1}})
	host := evm.Package{Path: "host", Types: []evm.TypeDecl{{Name: "Row", Shape: row}}}
	_, err := evmcompiler.Compile("test.go", `import "host"; func Run() uint64 { r:=host.Row{ID:7}; return uint64(r.ID) }`, host, ids)
	if err != nil {
		t.Fatal(err)
	}
}
