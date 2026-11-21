package evmcompiler_test

import (
	"context"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/kvo/codec/row2"
	"github.com/andreyvit/evm/values"
)

func TestSchemaPackage(t *testing.T) {
	s := kvo.NewSchema()
	tRow := kvo.NewEntityType(s, "customer_row")
	count := kvo.NewProp(s, 5, "count", kvo.TUint64, nil)
	note := kvo.NewProp(s, 6, "note", kvo.TMachineString, nil)
	model := kvo.NewModel(s, tRow, func(b *kvo.ModelBuilder) { b.Prop(s.MustPropByCode(count)); b.Prop(s.MustPropByCode(note)) })
	m, err := evm.ModelPackage("m", s)
	if err != nil {
		t.Fatal(err)
	}
	row := m.Record(model, false)
	ctx, scope := evm.WithRecords(context.Background())
	defer scope.Close()
	delta := row2.Edit(row2.Empty(row2.Compact2))
	delta.Set([]row2.Step{{Key: count}}, values.Uint(9), row2.Word)
	delta.Set([]row2.Step{{Key: note}}, values.Bytes([]byte("nine")), row2.Data)
	v := scope.Read(model, delta.Snapshot())
	m.Functions = []evm.ExternalFunction{{On: row, Name: "Find", Signature: evm.Signature{Results: []*values.Type{row.Pointer}}, Call: func(c *evm.VM) error { c.SetCallResult(0, v); return nil }}}
	bh := evm.Package{Path: "bh", Functions: []evm.ExternalFunction{{Name: "Read", Signature: evm.Signature{Results: []*values.Type{row.Pointer}}, Call: m.Functions[0].Call}}}
	for _, src := range []string{
		`import schema "m"; func Run()uint64 {find:=schema.CustomerRow.Find;return find().count}`,
		`import "m";type C=m.CustomerRow; func Run()uint64{c:=C.Find();if c.note!="nine"{return 0};return c.count}`,
		`import . "m"; func Run()uint64{return CustomerRow.Find().count}`,
		`import "bh";func Run()uint64{return bh.Read().count}`,
	} {
		module, err := evmcompiler.Compile("script.go", src, bh, m)
		if err != nil {
			t.Fatal(err)
		}
		program, err := evm.Link(module, bh, m)
		if err != nil {
			t.Fatal(err)
		}
		var out [1]values.Value
		if err := evmtest.New(program).Run(ctx, "Run", nil, out[:]); err != nil || out[0].R != 9 {
			t.Fatal(out, err)
		}
	}
	for _, src := range []string{`import "m";func Run(c *m.CustomerRow){c.Find()}`, `import "m";func Run(){m.CustomerRow.Find(5)}`, `import "m";func Run(c *m.CustomerRow){c.note="read-only"}`} {
		if _, err := evmcompiler.Compile("bad.go", src, m); err == nil {
			t.Fatalf("accepted %s", src)
		}
	}
	for path, wants := range map[string][]string{"m": {"type CustomerRow struct", "count uint64", "string", "m.CustomerRow.Find", "type CustomerRowEdit struct"}, "bh": {"func Read() *m.CustomerRow"}} {
		src, err := evmcompiler.PackageSource(path, bh, m)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parser.ParseFile(token.NewFileSet(), "api.go", src, 0); err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(src, want) {
				t.Fatalf("missing %q in %s", want, src)
			}
		}
	}
}

func TestModelJSONDetachesNestedData(t *testing.T) {
	schema := kvo.NewSchema()
	entity := kvo.NewEntityType(schema, "document")
	title := kvo.NewProp(schema, 1, "title", kvo.TMachineString, nil)
	labels := kvo.NewProp(schema, 2, "labels", kvo.ListOf(kvo.TMachineString), nil)
	model := kvo.NewModel(schema, entity, func(b *kvo.ModelBuilder) { b.Prop(schema.MustPropByCode(title)); b.Prop(schema.MustPropByCode(labels)) })
	pkg, err := evm.ModelPackage("m", schema)
	if err != nil {
		t.Fatal(err)
	}
	edit := row2.Edit(row2.Empty(row2.Compact2))
	edit.Set([]row2.Step{{Key: title}}, values.Bytes([]byte("invoice")), row2.Data)
	var list row2.Writer
	list.Begin(0, "", 2, row2.Compact2)
	list.Put(0, "", values.Bytes([]byte("paid")), row2.Data)
	list.End()
	edit.Set([]row2.Step{{Key: labels}}, values.Bytes(list.BytesWritten()), row2.Data)
	ctx, scope := evm.WithRecords(context.Background())
	defer scope.Close()
	pkg.Functions = append(pkg.Functions, evm.ExternalFunction{On: pkg.Record(model, false), Name: "Get", Signature: evm.Signature{Results: []*values.Type{pkg.Record(model, false).Pointer}}, Call: func(c *evm.VM) error { return c.SetReadRecordResult(model, edit.Snapshot()) }}, evm.ExternalFunction{Name: "Close", Call: func(c *evm.VM) error { scope.Close(); return nil }})
	m, err := evmcompiler.Compile("main.go", `import "m";func Main()any{v:=m.Document.Get().ToJSON();m.Close();v["title"]="copy";return v}`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(m, pkg)
	if err != nil {
		t.Fatal(err)
	}
	var result [1]values.Value
	if err := evmtest.New(program).Run(ctx, "Main", nil, result[:]); err != nil {
		t.Fatal(err)
	}
	raw, err := program.EncodeJSON(context.Background(), result[0], 1024)
	if err != nil || string(raw) != `{"labels":["paid"],"title":"copy"}` {
		t.Fatal(string(raw), err)
	}
}

func TestModelRowNameOverride(t *testing.T) {
	s := kvo.NewSchema()
	customer := kvo.NewModel(s, kvo.NewEntityType(s, "customer"), func(b *kvo.ModelBuilder) {})
	settings := kvo.NewModel(s, kvo.NewEntityType(s, "tenant_settings"), func(b *kvo.ModelBuilder) { b.SetRowName("TenantSettingsRow") })
	pkg, err := evm.ModelPackage("models", s)
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Record(customer, false).Name != "Customer" || pkg.Record(settings, false).Name != "TenantSettingsRow" || pkg.Record(settings, true).Name != "TenantSettingsRowEdit" {
		t.Fatal("row names not preserved")
	}
	if settings.Name() != "tenant_settings" {
		t.Fatal("row name changed schema identity")
	}
	kvo.NewModel(s, kvo.NewEntityType(s, "other_settings"), func(b *kvo.ModelBuilder) { b.SetRowName("TenantSettingsRow") })
	if _, err := evm.ModelPackage("models", s); err == nil {
		t.Fatal("colliding row override accepted")
	}
}
