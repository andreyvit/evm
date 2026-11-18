package evmcompiler_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/values"
)

type nativeValueRow struct {
	Name   string `kvo:"name"`
	Hidden int    `kvo:"-"`
	Shared *int   `kvo:"-"`
}

func TestNativeRecordValueStorage(t *testing.T) {
	schema := kvo.NewSchema()
	name := kvo.NewStringProp[string](schema, 1, "name", kvo.TMachineString)
	model := kvo.NewModel(schema, kvo.NewEntityType(schema, "Row", reflect.TypeFor[nativeValueRow]()), func(b *kvo.ModelBuilder) { b.Prop(name) })
	db, err := evm.ModelPackage("db", schema)
	if err != nil {
		t.Fatal(err)
	}
	row := db.Record(model, false)
	row.NativeAllocation = reflect.TypeFor[nativeValueRow]()
	binding := kvo.Bind(reflect.TypeFor[nativeValueRow](), model)
	shared := 10
	original := &nativeValueRow{Name: "before", Hidden: 42, Shared: &shared}
	var retained *nativeValueRow
	var hidden []int
	var references []*int
	db.Functions = []evm.ExternalFunction{
		{Name: "Get", Signature: evm.Signature{Results: []*values.Type{row.Pointer}}, Call: func(c *evm.Call) error { c.SetResult(0, binding.Record(reflect.ValueOf(original)).Value()); return nil }},
		{Name: "Change", Signature: evm.Signature{}, Call: func(c *evm.Call) error { original.Name = "after"; *original.Shared = 20; return nil }},
		{Name: "Retain", Signature: evm.Signature{Params: []*values.Type{row.Pointer}}, Call: func(c *evm.Call) error {
			r, err := c.Record(c.Argument(0), model)
			if err != nil {
				return err
			}
			retained = r.NativePointer().(*nativeValueRow)
			hidden = append(hidden, retained.Hidden)
			references = append(references, retained.Shared)
			return nil
		}},
		{Name: "Mutate", Signature: evm.Signature{}, Call: func(c *evm.Call) error { retained.Name = "mutated"; return nil }},
	}
	for _, source := range []string{
		`import "db"; func Run(p *db.Row) { *p = db.Row{} }`,
		`import "db"; func Run() {var b struct{Row db.Row}; *(&b.Row)=db.Row{} }`,
		`import "db"; func Run() {a:=make([]db.Row,1); *(&a[0])=db.Row{} }`,
		`import ("db";"util"); func Run(p *db.Row) {util.Set(p,db.Row{})}`,
		`import "db"; type Alias = db.Row; func Run(p *Alias) { *p = Alias{} }`,
		`import "db"; func Run(p *db.Row) {for _, *p = range []db.Row{{}} {}}`,
		`import "db"; func Run() {var row db.Row; p:=&row; *p=db.Row{} }`,
		`import "db"; func reset[T any](p *T) {*p=*new(T)}; func Run(p *db.Row) {reset(p)}`,
	} {
		if _, err := evmcompiler.Compile("write.go", source, db, evm.Package{Path: "util", Source: "package util; func Set[T any](p *T,v T){*p=v}"}); err == nil || !strings.Contains(err.Error(), "cannot replace a read-only model row") {
			t.Fatalf("expected read-only replacement error: %v", err)
		}
	}
	for _, source := range []string{
		`import "db"; type R db.Row; func Run(p *db.Row){*(*R)(p)=R{}}`,
		`import "db"; type R db.Row; func Run(p *db.Row)string{r:=*(*R)(p);return db.Row(r).name}`,
		`import "db"; type P *db.Row; func Run(p P){*p=db.Row{}}`,
	} {
		if _, err := evmcompiler.Compile("derived.go", source, db); err == nil || !strings.Contains(err.Error(), "use a type alias") {
			t.Fatalf("expected derived row rejection: %v", err)
		}
	}
	mod, err := evmcompiler.Compile("copy.go", `import "db"
 func row() db.Row {return *db.Get()}
 var global db.Row
 func Run()bool{
 if global.name!="" || row().name!="before"{return false}
 m:=map[int]db.Row{1:row()}; if m[1].name!="before"{return false}
 p:=db.Get(); saved:=*p; db.Change()
 if saved.name!="before"{return false}
 db.Retain(&saved); db.Mutate()
 if saved.name!="mutated" || p.name!="after"{return false}
 type Box struct{Row db.Row; Flag int}
 box:=Box{Row:*p,Flag:7}; other:=box
 db.Retain(&box.Row); db.Mutate()
 if other.Row.name!="after" || box.Flag!=7{return false}
 a:=[2]db.Row{saved}; db.Retain(&a[0]); a=[2]db.Row{*p}
 if a[0].name!="after"{return false}
 db.Mutate()
 if a[0].name!="mutated" || saved.name!="mutated"{return false}
 if *p==saved || saved!=saved{return false}
 var zero db.Row
 db.Retain(&zero);db.Mutate()
 if zero.name!="mutated"{return false}
 global=*p; db.Retain(&global); db.Mutate()
 return global.name=="mutated"

 }
 `, db)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(mod, db)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]values.Value, 1)
	if err := evm.New(program).Run(context.Background(), "Run", nil, out); err != nil {
		t.Fatal(err)
	}
	if !out[0].AsBool() {
		t.Fatal("native value did not preserve copy/address semantics")
	}
	if !reflect.DeepEqual(hidden, []int{42, 42, 42, 0, 42}) {
		t.Fatalf("hidden fields: %v", hidden)
	}
	if !reflect.DeepEqual(references, []*int{&shared, &shared, &shared, nil, &shared}) || shared != 20 {
		t.Fatal("native shallow copies lost shared pointer fields")
	}
	if retained == original || retained == nil || retained.Hidden != 42 {
		t.Fatal("native value lost storage or hidden fields")
	}
}

type nativeSliceValueRow struct {
	Name   string `kvo:"name"`
	Hidden []int  `kvo:"-"`
}

func TestNativeRecordValueComparability(t *testing.T) {
	schema := kvo.NewSchema()
	name := kvo.NewStringProp[string](schema, 1, "name", kvo.TMachineString)
	model := kvo.NewModel(schema, kvo.NewEntityType(schema, "Row", reflect.TypeFor[nativeSliceValueRow]()), func(b *kvo.ModelBuilder) { b.Prop(name) })
	db, err := evm.ModelPackage("db", schema)
	if err != nil {
		t.Fatal(err)
	}
	db.Record(model, false).NativeAllocation = reflect.TypeFor[nativeSliceValueRow]()
	for _, source := range []string{
		`import "db"; func Run(a,b db.Row) bool {return a==b}`,
		`import "db"; func Run() { _=make(map[db.Row]int) }`,
	} {
		if _, err := evmcompiler.Compile("compare.go", source, db); err == nil {
			t.Fatal("noncomparable native row accepted")
		}
	}
	mod, err := evmcompiler.Compile("copy.go", `import "db"; func Run() bool {var a db.Row; b:=a; return b.name==""}`, db)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(mod, db)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if !out[0].AsBool() {
		t.Fatal("zero noncomparable row did not copy")
	}
}

func BenchmarkNativeRecordValueRead(b *testing.B) {
	schema := kvo.NewSchema()
	name := kvo.NewStringProp[string](schema, 1, "name", kvo.TMachineString)
	model := kvo.NewModel(schema, kvo.NewEntityType(schema, "Row", reflect.TypeFor[nativeValueRow]()), func(m *kvo.ModelBuilder) { m.Prop(name) })
	db, err := evm.ModelPackage("db", schema)
	if err != nil {
		b.Fatal(err)
	}
	db.Record(model, false).NativeAllocation = reflect.TypeFor[nativeValueRow]()
	mod, err := evmcompiler.Compile("read.go", `import "db"
func Pointer(p *db.Row) string {return p.name}
func Copy(p *db.Row) string {saved:=*p;return saved.name}
`, db)
	if err != nil {
		b.Fatal(err)
	}
	program, err := evm.Link(mod, db)
	if err != nil {
		b.Fatal(err)
	}
	original := &nativeValueRow{Name: "value", Hidden: 42}
	arg := kvo.Bind(reflect.TypeFor[nativeValueRow](), model).Record(reflect.ValueOf(original)).Value()
	for _, fn := range []string{"Pointer", "Copy"} {
		b.Run(fn, func(b *testing.B) {
			vm := evm.New(program)
			vm.Limits.Allocations = ^uint64(0)
			vm.Limits.AllocationBytes = ^uint64(0)
			args := []values.Value{arg}
			var out [1]values.Value
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if err := vm.Run(ctx, fn, args, out[:]); err != nil {
					b.Fatal(err)
				}
			}
			if out[0].AsString() != "value" {
				b.Fatal("wrong property")
			}
		})
	}
}
