package evmcompiler_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/values"
)

func TestRecordReferencesDoNotImportOwnerSource(t *testing.T) {
	schema := kvo.NewSchema()
	entity := kvo.NewEntityType(schema, "Row")
	model := kvo.NewModel(schema, entity, func(b *kvo.ModelBuilder) {})
	db, err := evm.ModelPackage("db", schema)
	if err != nil {
		t.Fatal(err)
	}
	row := db.Record(model, false)
	key := values.NewType("host.Key", values.Raw)
	host := evm.Package{Path: "host", Types: []evm.TypeDecl{{Name: "Key", Shape: &values.Shape{Type: key, Kind: values.UintKind, Bits: 64, Cells: 1}}}, Functions: []evm.ExternalFunction{{Name: "Read", Signature: evm.Signature{Results: []*values.Type{row.Pointer}}, Call: func(c *evm.VM) error { c.SetCallResult(0, values.Value{}); return nil }}}}
	db.Source = `package db; import "host"; func Lookup() host.Key`
	db.Functions = append(db.Functions, evm.ExternalFunction{Name: "Lookup", Signature: evm.Signature{Results: []*values.Type{key}}, Call: func(c *evm.VM) error { c.SetCallResult(0, values.Uint(7)); return nil }})
	for _, pkgs := range [][]evm.Package{{host, db}, {db, host}} {
		for _, source := range []string{
			`import("host";"db");func Run()uint64{if host.Read()!=nil {panic("row")};return uint64(db.Lookup())}`,
			`import("db";"host");func Run()uint64{if host.Read()!=nil {panic("row")};return uint64(db.Lookup())}`,
		} {
			m, err := evmcompiler.Compile("use.go", source, pkgs...)
			if err != nil {
				t.Fatal(err)
			}
			p, err := evm.Link(m, pkgs...)
			if err != nil {
				t.Fatal(err)
			}
			out := make([]values.Value, 1)
			if err := evmtest.New(p).Run(context.Background(), "Run", nil, out); err != nil || out[0].R != 7 {
				t.Fatal(out, err)
			}
		}
		for _, path := range []string{"host", "db"} {
			text, err := evmcompiler.PackageSource(path, pkgs...)
			if err != nil || !strings.Contains(text, "package "+path) {
				t.Fatal(text, err)
			}
		}
	}
}

func TestSourceImportCycleReportsCycle(t *testing.T) {
	a := evm.Package{Path: "a", Source: `package a; import "b";type A struct{B *b.B}`}
	b := evm.Package{Path: "b", Source: `package b; import "a";type B struct{A *a.A}`}
	_, err := evmcompiler.Compile("use.go", `import "a";func Run(){var x a.A;_=x}`, a, b)
	if err == nil || !strings.Contains(err.Error(), "import cycle through") {
		t.Fatal(err)
	}
}

func TestRecordPropertySharesOwnerScalar(t *testing.T) {
	schema := kvo.NewSchema()
	entity := kvo.NewEntityType(schema, "Row")
	id := kvo.NewProp(schema, 1, "id", kvo.TUint64, nil)
	kvo.NewModel(schema, entity, func(b *kvo.ModelBuilder) { b.Prop(schema.MustPropByCode(id)) })
	db, err := evm.ModelPackage("db", schema)
	if err != nil {
		t.Fatal(err)
	}
	idT := values.NewType("db.ID", values.Raw)
	db.Source = `package db; type ID uint64`
	db.SharedTypes = []evm.SharedType{{Name: "ID", Type: idT, Native: reflect.TypeFor[uint64]()}}
	_, err = evmcompiler.Compile("use.go", `import "db";func Run(r *db.Row) uint64 { return uint64(r.id) }`, db)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRecordOwnerCompletesAfterSourceImports(t *testing.T) {
	schema := kvo.NewSchema()
	entity := kvo.NewEntityType(schema, "Row")
	model := kvo.NewModel(schema, entity, func(b *kvo.ModelBuilder) {})
	y, err := evm.ModelPackage("y", schema)
	if err != nil {
		t.Fatal(err)
	}
	row := y.Record(model, false)
	k := values.NewType("h.K", values.Raw)
	h := evm.Package{Path: "h", Types: []evm.TypeDecl{{Name: "K", Shape: &values.Shape{Type: k, Kind: values.UintKind, Bits: 64, Cells: 1}}}, Functions: []evm.ExternalFunction{{Name: "Read", Signature: evm.Signature{Results: []*values.Type{row.Pointer}}, Call: func(c *evm.VM) error { return nil }}}}
	xt := values.NewType("x.T", values.Raw)
	x := evm.Package{Path: "x", Source: `package x; import "h"; type T uint64; func G() h.K`, SharedTypes: []evm.SharedType{{Name: "T", Type: xt, Native: reflect.TypeFor[uint64]()}}, Functions: []evm.ExternalFunction{{Name: "G", Signature: evm.Signature{Results: []*values.Type{k}}, Call: func(c *evm.VM) error { return nil }}}}
	y.Source = `package y; import "x"; func F() x.T`
	y.Functions = append(y.Functions, evm.ExternalFunction{Name: "F", Signature: evm.Signature{Results: []*values.Type{xt}}, Call: func(c *evm.VM) error { return nil }})
	for _, src := range []string{`import "x";func Run(){_=x.G()}`, `import("y";"x");func Run(){_=x.G();_=y.F()}`} {
		_, err = evmcompiler.Compile("use.go", src, h, x, y)
		if err != nil {
			t.Fatal(src, err)
		}
	}
}

func TestRecordPropertyPreservesSourceError(t *testing.T) {
	schema := kvo.NewSchema()
	entity := kvo.NewEntityType(schema, "Row")
	id := kvo.NewProp(schema, 1, "id", kvo.TUint64, nil)
	kvo.NewModel(schema, entity, func(b *kvo.ModelBuilder) { b.Prop(schema.MustPropByCode(id)) })
	db, err := evm.ModelPackage("db", schema)
	if err != nil {
		t.Fatal(err)
	}
	idT := values.NewType("db.ID", values.Raw)
	db.Source = `package db; type ID uint64; func Bad() Undefined`
	db.SharedTypes = []evm.SharedType{{Name: "ID", Type: idT, Native: reflect.TypeFor[uint64]()}}
	_, err = evmcompiler.Compile("use.go", `import "db";func Run(r *db.Row) uint64 { return uint64(r.id) }`, db)
	if err == nil || !strings.Contains(err.Error(), "undefined: Undefined") || strings.Contains(err.Error(), "redeclared") {
		t.Fatal(err)
	}
}

func TestNativeInterfaceReferencesDoNotImportOwnerSource(t *testing.T) {
	record := &values.Shape{Type: values.NewType("db.Record", values.Raw), Kind: values.IntKind, Bits: 64, Cells: 1}
	pointer := values.NewPointerShape(record)
	dbtx := &values.Shape{Type: values.NewType("func()*db.Record", values.Pair), Kind: values.FunctionKind, Cells: 1, Results: []*values.Shape{pointer}}
	tx := &values.Shape{Type: values.NewType("db.Txish", values.Pair), Kind: values.InterfaceKind, Cells: 1, Methods: []values.Method{{ID: "DBTx", Signature: dbtx}}}
	sig := &values.Shape{Type: values.NewType("func()db.Txish", values.Pair), Kind: values.FunctionKind, Cells: 1, Results: []*values.Shape{tx}}
	ctx := &values.Shape{Type: values.NewType("host.Context", values.Pair), Kind: values.InterfaceKind, Cells: 1, Methods: []values.Method{{ID: "Tx", Signature: sig}}}
	host := evm.Package{Path: "host", Types: []evm.TypeDecl{{Name: "Context", Shape: ctx}}}
	db := evm.Package{Path: "db", Types: []evm.TypeDecl{{Name: "Txish", Shape: tx}, {Name: "Record", Shape: record}, {Shape: pointer}}, Source: `package db; import "host"; func From(c host.Context) Txish`, Functions: []evm.ExternalFunction{{Name: "From", Signature: evm.Signature{Params: []*values.Type{ctx.Type}, Results: []*values.Type{tx.Type}}, Call: func(c *evm.VM) error { return nil }}}}
	for _, pkgs := range [][]evm.Package{{host, db}, {db, host}} {
		for _, src := range []string{
			`import("host";"db");func Run(c host.Context) db.Txish {return db.From(c)}`,
			`import("db";"host");func Run(c host.Context) db.Txish {return db.From(c)}`,
		} {
			if _, err := evmcompiler.Compile("use.go", src, pkgs...); err != nil {
				t.Fatal(err)
			}
		}
		for _, path := range []string{"host", "db"} {
			if _, err := evmcompiler.PackageSource(path, pkgs...); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestNativeMethodsThroughIndirectSourceImport(t *testing.T) {
	row := &values.Shape{Type: values.NewType("rows.Row", values.Raw), Kind: values.IntKind, Bits: 64, Cells: 1}
	rows := evm.Package{Path: "rows", Types: []evm.TypeDecl{{Name: "Row", Shape: row}}, Methods: []evm.ExternalMethod{{Name: "Value", Receiver: row.Type, Signature: evm.Signature{Results: []*values.Type{values.IntType}}, Call: func(c *evm.VM) error { c.SetCallResult(0, values.Int(7)); return nil }}}}
	provider := evm.Package{Path: "provider", Functions: []evm.ExternalFunction{{Name: "Get", Signature: evm.Signature{Results: []*values.Type{row.Type}}, Call: func(c *evm.VM) error { return nil }}}}
	source := evm.Package{Path: "source", Source: `package source;import "provider";func Read[T any]()int{return provider.Get().Value()}`}
	_, err := evmcompiler.Compile("use.go", `import "source";func Run()int{return source.Read[int]()}`, source, provider, rows)
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeSourceDependentCycle(t *testing.T) {
	key := values.NewType("p.Key", values.Pair)
	field := &values.Shape{Type: key, Kind: values.StringKind, Cells: 1}
	wrapper := &values.Shape{Type: values.NewType("p.Wrapper", values.Pointer), Kind: values.StructKind, Cells: 1, Fields: []values.Field{{Name: "Key", Shape: field}}}
	p := evm.Package{Path: "p", Source: `package p;import "q";type Key string;type Marker=q.Marker`, SharedTypes: []evm.SharedType{{Name: "Key", Type: key, Native: reflect.TypeFor[string]()}}, Types: []evm.TypeDecl{{Name: "Wrapper", Shape: wrapper}}}
	q := evm.Package{Path: "q", Types: []evm.TypeDecl{{Name: "Marker", Shape: &values.Shape{Type: values.NewType("q.Marker", values.Raw), Kind: values.IntKind, Bits: 64, Cells: 1}}}, Functions: []evm.ExternalFunction{{Name: "Get", Signature: evm.Signature{Results: []*values.Type{wrapper.Type}}, Call: func(*evm.VM) error { return nil }}}}
	_, err := evmcompiler.Compile("use.go", `import "p";func Run(){var x p.Marker;_=x}`, p, q)
	if err == nil || !strings.Contains(err.Error(), "import cycle") {
		t.Fatal(err)
	}
}

func TestDuplicateNativeValueAndPointerMethods(t *testing.T) {
	scalar := &values.Shape{Type: values.NewType("native.Number", values.Raw), Kind: values.IntKind, Bits: 64, Cells: 1}
	pointer := values.NewPointerShape(scalar)
	p := evm.Package{Path: "native", Source: "package native", Types: []evm.TypeDecl{{Name: "Number", Shape: scalar}, {Shape: pointer}}, Methods: []evm.ExternalMethod{
		{Name: "M", Receiver: scalar.Type, Call: func(*evm.VM) error { return nil }},
		{Name: "M", Receiver: pointer.Type, Call: func(*evm.VM) error { return nil }},
	}}
	_, err := evmcompiler.Compile("use.go", `import "native";func Run(){var n native.Number;n.M()}`, p)
	if err == nil || !strings.Contains(err.Error(), "duplicate method") {
		t.Fatal(err)
	}
}
