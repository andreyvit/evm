package evm_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/andreyvit/evm/nativebind"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"unsafe"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/kvo/codec/row2"
	"github.com/andreyvit/evm/values"
)

type AnyModelRow struct {
	Name   string `kvo:"name"`
	Hidden int    `kvo:"-"`
	Shared *int   `kvo:"-"`
}
type anyModelAlternate struct {
	Label string `kvo:"name"`
}

func TestNativeModelAny(t *testing.T) {
	schema := kvo.NewSchema()
	name := kvo.NewStringProp[string](schema, 1, "name", kvo.TMachineString)
	model := kvo.NewModel(schema, kvo.NewEntityType(schema, "ZCustomer", reflect.TypeFor[AnyModelRow](), reflect.TypeFor[anyModelAlternate]()), func(b *kvo.ModelBuilder) { b.Prop(name) })
	models, err := evm.ModelPackage("db", schema)
	if err != nil {
		t.Fatal(err)
	}
	models.Record(model, false).NativeAllocation = reflect.TypeFor[AnyModelRow]()
	evm.RegisterNativeCopies(&models)
	shared := 7
	row := &AnyModelRow{Name: "first", Hidden: 42, Shared: &shared}
	alt := &anyModelAlternate{"second"}
	binding := kvo.Bind(reflect.TypeFor[AnyModelRow](), model)
	encoded := binding.Encoded(row2.Trusted(binding.Encode(nil, reflect.ValueOf(row).Elem(), row2.Fast2)))
	input := []any{row, alt, (*AnyModelRow)(nil), (*anyModelAlternate)(nil), encoded, binding.Record(reflect.ValueOf(row)), kvo.Project(reflect.TypeFor[AnyModelRow](), model, nil).Record(reflect.ValueOf(row)), *row}
	checks := 0
	host := evm.Package{Path: "host", Functions: []evm.ExternalFunction{
		{Name: "Get", Signature: evm.Signature{Params: []*values.Type{values.IntType}, Results: []*values.Type{values.AnyType}}, Call: func(c *evm.Call) error {
			v, err := c.AnyValue(input[c.Argument(0).AsInt()])
			if err != nil {
				return err
			}
			c.SetResult(0, v)
			return nil
		}},
		{Name: "Check", Signature: evm.Signature{Params: []*values.Type{values.IntType, values.AnyType}}, Call: func(c *evm.Call) error {
			index := int(c.Argument(0).AsInt())
			v, err := c.NativeAny(c.Argument(1))
			if err != nil {
				return err
			}
			want := input[index]
			if index >= 5 && index < 7 {
				want = row
			}
			if !reflect.DeepEqual(v, want) {
				return fmt.Errorf("roundtrip %d: %T %#v != %T %#v", index, v, v, want, want)
			}
			if index < 4 || index >= 5 && index < 7 {
				if v != want {
					return fmt.Errorf("pointer identity %d", index)
				}
			}
			if index == 4 && v.(kvo.Record).Value() != encoded.Value() {
				return fmt.Errorf("encoded identity lost")
			}
			checks++
			return nil
		}},
		{Name: "Echo", Signature: evm.Signature{Params: []*values.Type{values.AnyType}, Results: []*values.Type{values.AnyType}}, Call: func(c *evm.Call) error {
			x, err := c.NativeAny(c.Argument(0))
			if err != nil {
				return err
			}
			if r, ok := x.(kvo.Record); ok && (r.Model() != model || !r.Missing()) {
				return fmt.Errorf("guest nil lost model")
			}
			v, err := c.AnyValue(x)
			if err != nil {
				return err
			}
			c.SetResult(0, v)
			return nil
		}},
		{Name: "CheckCopy", Signature: evm.Signature{Params: []*values.Type{models.Record(model, false).Pointer}}, Call: func(c *evm.Call) error {
			record, err := c.Record(c.Argument(0), model)
			if err != nil {
				return err
			}
			copied := record.NativePointer().(*AnyModelRow)
			if copied == row || copied.Hidden != 42 || copied.Shared != row.Shared {
				return fmt.Errorf("native shallow copy lost fields or identity")
			}
			return nil
		}},
		{Name: "Change", Call: func(c *evm.Call) error { row.Name = "changed"; return nil }},
	}}
	native, err := evmbind.New("A", fstest.MapFS{"types.go": &fstest.MapFile{Data: []byte("package native; type AnyModelRow struct { Name string `kvo:\"name\"`; Hidden int `kvo:\"-\"`; Shared *int `kvo:\"-\"` }")}}, evmbind.Options{Only: []string{"AnyModelRow"}, Types: []reflect.Type{reflect.TypeFor[AnyModelRow]()}})
	if err != nil {
		t.Fatal(err)
	}
	module, err := evmcompiler.Compile("models.go", `import("db";"host";native "A")
 func Copy(p *db.ZCustomer) *db.ZCustomer {return p.NativeCopy()}
 func Run(){
  var zero db.ZCustomer
  if host.Echo(zero).(db.ZCustomer).name!=""{panic("zero value roundtrip")}
  value:=host.Get(7).(db.ZCustomer)
  host.Check(7,value)
  if _,ok:=host.Get(7).(native.AnyModelRow);ok{panic("model value must outrank native struct binding")}
  if host.Echo(value).(db.ZCustomer).name!="first"{panic("value roundtrip")}
  first:=host.Get(0).(*db.ZCustomer)
  if _,ok:=host.Get(0).(*native.AnyModelRow);ok{panic("model must outrank native struct binding")}
  if first.name!="first" || host.Get(1).(*db.ZCustomer).name!="second"{panic("properties")}
  for i:=0;i<7;i++{v:=host.Get(i);r:=v.(*db.ZCustomer);if i==2 || i==3{if v==nil || r!=nil{panic("typed nil")}};host.Check(i,v)}
  if host.Get(2).(*db.ZCustomer)!=host.Get(3).(*db.ZCustomer){panic("nil row equality")}
  if host.Get(2)!=host.Get(3){panic("nil any equality")}
  if host.Get(0)!=host.Get(5){panic("same native row equality")}
  var absent *db.ZCustomer
  nilRow:=host.Echo(absent);if nilRow==nil || nilRow.(*db.ZCustomer)!=nil{panic("guest nil roundtrip")}
  if host.Echo(nil)!=nil || host.Echo(host.Get(0))!=host.Get(0){panic("echo")}
  projected:=host.Get(6).(*db.ZCustomer)
  if first!=projected || host.Get(0)!=host.Get(6){panic("projection pointer identity")}
  keys:=map[*db.ZCustomer]int{first:9}; if keys[projected]!=9{panic("projection map identity")}
  anyKeys:=map[any]int{host.Get(0):8};if anyKeys[host.Get(6)]!=8{panic("projection any map identity")}
  encoded:=host.Get(4).(*db.ZCustomer)
  held:=[]any{encoded, first}; held=append(held,encoded)
  if held[2].(*db.ZCustomer)!=encoded{panic("encoded handle changed")}
  converted:=encoded.NativeCopy()
  copiedNative:=first.NativeCopy();host.CheckCopy(copiedNative)
  alternate:=host.Get(1).(*db.ZCustomer).NativeCopy()
  if converted==encoded || copiedNative==first || alternate.name!="second" {panic("native copy identity")}
  rows:=[]*db.ZCustomer{converted,copiedNative,alternate}
  rowValues:=[]db.ZCustomer{*converted}
  if rows[0].name!="first" || rowValues[0].name!="first" {panic("native copy collections")}
  if absent.NativeCopy()!=nil{panic("nil native copy")}
  host.Change();if copiedNative.name!="first" || encoded.name!="first" {panic("copy aliases source")};if value.name!="first"{panic("native value alias")};if first.name!="changed"{panic("native alias")}
 }
 `, models, host, native.Package())
	if err != nil {
		t.Fatal(err)
	}
	models.Record(model, false).NativeAllocation = reflect.TypeFor[anyModelAlternate]()
	if _, err := evm.Link(module, models, host, native.Package()); err == nil || !strings.Contains(err.Error(), "changed since compilation") {
		t.Fatalf("changed native allocation: %v", err)
	}
	models.Record(model, false).NativeAllocation = reflect.TypeFor[AnyModelRow]()
	program, err := evm.Link(module, models, host, native.Package())
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(program).Run(context.Background(), "Run", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := evm.New(program).RunWithBudget(context.Background(), "Copy", []values.Value{binding.Record(reflect.ValueOf(row)).Value()}, make([]values.Value, 1), evm.Budget{AllocationBytes: 1}); !errors.Is(err, evm.ErrAllocation) {
		t.Fatalf("native copy allocation budget: %v", err)
	}
	if checks != 8 {
		t.Fatal(checks)
	}

	only, err := evmcompiler.Compile("assert_only.go", `import("db";"host")
 func Run()string{return host.Get(0).(*db.ZCustomer).name}`, models, host)
	if err != nil {
		t.Fatal(err)
	}
	onlyProgram, err := evm.Link(only, models, host)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(onlyProgram).Run(context.Background(), "Run", nil, out[:]); err != nil || out[0].AsString() != "changed" {
		t.Fatal(out, err)
	}
}

func TestBorrowNativeModelResults(t *testing.T) {
	schema := kvo.NewSchema()
	name := kvo.NewStringProp[string](schema, 1, "name", kvo.TMachineString)
	model := kvo.NewModel(schema, kvo.NewEntityType(schema, "Person", reflect.TypeFor[AnyModelRow]()), func(b *kvo.ModelBuilder) { b.Prop(name) })
	models, err := evm.ModelPackage("models", schema)
	if err != nil {
		t.Fatal(err)
	}
	read := models.Record(model, false)
	read.NativeAllocation = reflect.TypeFor[AnyModelRow]()
	row := AnyModelRow{Name: "Ada"}
	borrow := func(c *evm.Call) error {
		c.SetResult(0, nativebind.Load(reflect.TypeOf(row), unsafe.Pointer(&row)))
		return nil
	}
	host := evm.Package{Path: "host", Functions: []evm.ExternalFunction{
		{Name: "Handle", Signature: evm.Signature{Results: []*values.Type{read.Pointer}}, Call: borrow},
		{Name: "Snapshot", Signature: evm.Signature{Results: []*values.Type{read.Value}}, Call: borrow},
	}}
	module, err := evmcompiler.Compile("model.go", `import "host";func Run()string{return host.Handle().name + host.Snapshot().name}`, models, host)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, models, host)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if out[0].AsString() != "AdaAda" {
		t.Fatal(out[0].AsString())
	}
}
