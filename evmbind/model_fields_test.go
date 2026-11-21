package evmbind_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/modeldata"
	"github.com/andreyvit/evm/evmbind/internal/modelfielddata"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/kvo/codec/row2"
	"github.com/andreyvit/evm/values"
)

func TestNativeModelPointerFields(t *testing.T) { testNativeModelPointerFields(t, nil) }
func testNativeModelPointerFields(t *testing.T, static *evmbind.Static) {
	t.Helper()
	models, dep, err := modeldata.Declarations()
	if err != nil {
		t.Fatal(err)
	}
	opt := evmbind.Options{Selective: true, Methods: map[string][]string{"Context": {"Clone"}}, Types: []reflect.Type{reflect.TypeFor[modelfielddata.Context]()}, Imports: map[string]*evmbind.Schema{"github.com/andreyvit/evm/evmbind/internal/modeldata": dep}, Static: static}
	if static != nil {
		opt.Mode = evmbind.StaticOnly
	}
	schema, err := evmbind.New("ctx", modelfielddata.Source, opt)
	if err != nil {
		t.Fatal(err)
	}
	pkgs := []evm.Package{models, dep.Package(), schema.Bind(nil)}
	mod, err := evmcompiler.Compile("use.go", `import("ctx";"db")
func Run(c *ctx.Context){
 returned:=c.Clone()
 if returned.Value.name!="inline"{panic("returned inline value")}
 if c.Value.name!="inline"{panic("inline property")}
 saved:=c.Value
 address:=&c.Value
 c.Value=*c.Customer
 if saved.name!="inline" || address.name!="original"{panic("inline copy/address")}
 var row *db.Customer=c.Customer
 if row.name!="original" || c.Other!=nil{panic("model fields")}
 copy:=*c
 fresh:=&ctx.Context{Customer:copy.Customer}
 if fresh.Value.name!=""{panic("zero inline value")}
 fresh.Value=copy.Value
 if fresh.Value.name!="original"{panic("guest inline value")}
 c.Other=fresh.Customer
 c.Customer=nil
}
func Read(c *ctx.Context){for i:=0;i<1000;i++{if (c.Value).name!="inline"{panic("property")}}}
func Store(c *ctx.Context,row *db.Customer){c.Other=row}
`, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(mod, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	row := &BoundModel{Name: "original", Hidden: 42}
	c := &modelfielddata.Context{Customer: row, Value: BoundModel{Name: "inline", Hidden: 7}}
	arg, err := schema.NativePointer(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := evmtest.New(program).RunWithBudget(context.Background(), "Read", []values.Value{arg}, nil, evm.Budget{Allocations: 100, AllocationBytes: 4096}); err != nil {
		t.Fatal(err)
	}
	if err := evmtest.New(program).Run(context.Background(), "Run", []values.Value{arg}, nil); err != nil {
		t.Fatal(err)
	}
	if c.Customer != nil || c.Other != row || row.Hidden != 42 || c.Value.Hidden != 42 {
		t.Fatal("native model pointer was copied", c)
	}
	binding := kvo.Bind(reflect.TypeFor[BoundModel](), models.Records[0].Model)
	encoded := binding.Encoded(row2.Trusted(binding.Encode(nil, reflect.ValueOf(row).Elem(), row2.Fast2)))
	err = evmtest.New(program).Run(context.Background(), "Store", []values.Value{arg, encoded.Value()}, nil)
	if err == nil || !strings.Contains(err.Error(), "explicit conversion") {
		t.Fatal("encoded row silently decoded", err)
	}
	if c.Other != row {
		t.Fatal("failed assignment changed native field")
	}
	if _, err := evmcompiler.Compile("immutable.go", `import "ctx";func Change(c *ctx.Context){c.Customer.name="changed"}`, pkgs...); err == nil {
		t.Fatal("model field became editable")
	}

}
