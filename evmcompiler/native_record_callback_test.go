package evmcompiler_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/kvo/codec/msgpack"
	"github.com/andreyvit/evm/values"
)

func TestNativeCallbackRecordIdentity(t *testing.T) {
	schema := kvo.NewSchema()
	entity := kvo.NewEntityType(schema, "customer")
	total := kvo.NewProp(schema, 1, "total", kvo.TInt64, nil)
	tags := kvo.NewProp(schema, 2, "tags", kvo.ListOf(kvo.TMachineString), nil)
	model := kvo.NewModel(schema, entity, func(b *kvo.ModelBuilder) { b.Prop(schema.MustPropByCode(total)); b.Prop(schema.MustPropByCode(tags)) })
	record := evm.NewRecordType("Customer", model, false)
	row := &values.Shape{Type: record.Pointer, Kind: values.TypedPairKind, PointerLike: true, Cells: 1}
	boolean := &values.Shape{Type: values.Boolean, Kind: values.BoolKind, Cells: 1}
	callback := &values.Shape{Type: values.NewType("func(*db.Customer)bool", values.Pair), Kind: values.FunctionKind, Cells: 1, Params: []*values.Shape{row}, Results: []*values.Shape{boolean}}
	type customer struct {
		Total int64    `msgpack:"total"`
		Tags  []string `msgpack:"tags"`
	}
	projection := kvo.Project(reflect.TypeFor[customer](), model, kvo.FieldMap{reflect.TypeFor[customer](): {total: "Total", tags: "Tags"}})
	value := projection.MessagePack(msgpack.Encode(nil, reflect.ValueOf(&customer{42, []string{"one", "two"}})))
	pkg := evm.Package{Path: "db", Records: []*evm.RecordType{record}, Types: []evm.TypeDecl{{Shape: callback}}, Functions: []evm.ExternalFunction{{Name: "Visit", Signature: evm.Signature{Params: []*values.Type{callback.Type}}, Call: func(c *evm.VM) error {
		v, err := c.RecordValue(model, value)
		if err != nil {
			return err
		}
		var result [1]values.Value
		return evmtest.Callback(c, c.CallArgument(0), []values.Value{v}, result[:])
	}}}}
	source := `import "db"
 func CountRow(row *db.Customer) bool {
 count:=0
 for _,tag:=range row.tags {if tag=="one" {count++}}
 if count!=1 {panic("list iteration")}
 return true
}
func Count() int64 {db.Visit(CountRow);return 1}
func Run() int64 {
 var saved *db.Customer
 count:=0
 db.Visit(func(row *db.Customer)bool{
  saved=row
  for _,tag:=range row.tags {
   db.Visit(func(inner *db.Customer)bool{
    for _,other:=range inner.tags {if tag==other {count++}}
    return true
   })
  }
  return true
 })
 if count!=2 {panic("nested list iteration")}
 return saved.total
}`
	for _, unoptimized := range []bool{false, true} {
		m, err := (evmcompiler.Options{NoOptimization: unoptimized}).Compile("records.go", source, pkg)
		if err != nil {
			t.Fatal(err)
		}
		p, err := evm.Link(m, pkg)
		if err != nil {
			t.Fatal(err)
		}
		for name, want := range map[string]int64{"Run": 42, "Count": 1} {
			var out [1]values.Value
			if err := evmtest.New(p).Run(context.Background(), name, nil, out[:]); err != nil {
				t.Fatal(err)
			}
			if out[0].AsInt() != want {
				t.Fatalf("%s: got %v, want %d", name, out, want)
			}
		}
	}
	row.PointerLike = false
	if _, err := evmcompiler.Compile("wrong.go", source, pkg); err == nil {
		t.Fatal("accepted incompatible record representation")
	}
}

func TestNativeCallbackAfterInterfaceSignature(t *testing.T) {
	str := &values.Shape{Type: values.StringType, Kind: values.StringKind, Cells: 1}
	boolean := &values.Shape{Type: values.Boolean, Kind: values.BoolKind, Cells: 1}
	method := &values.Shape{Type: values.NewType("Check", values.Pair), Kind: values.FunctionKind, Cells: 1, Params: []*values.Shape{str}, Results: []*values.Shape{boolean}}
	iface := &values.Shape{Type: values.NewType("iface.Checker", values.Pair), Kind: values.InterfaceKind, Cells: 1, Methods: []values.Method{{ID: "Check", Signature: method}}}
	first := evm.Package{Path: "iface", Types: []evm.TypeDecl{{Name: "Checker", Shape: iface}}}
	callback := &values.Shape{Type: values.NewType("func(string)bool", values.Pair), Kind: values.FunctionKind, Cells: 1, Params: []*values.Shape{str}, Results: []*values.Shape{boolean}}
	second := evm.Package{Path: "host", Types: []evm.TypeDecl{{Shape: callback}}, Functions: []evm.ExternalFunction{{Name: "Accept", Signature: evm.Signature{Params: []*values.Type{callback.Type}, Results: []*values.Type{values.Boolean}}, Call: func(c *evm.VM) error {
		var result [1]values.Value
		err := evmtest.Callback(c, c.CallArgument(0), []values.Value{values.String("ok")}, result[:])
		c.SetCallResult(0, result[0])
		return err
	}}}}
	for _, imports := range []string{`import("iface";"host")`, `import("host";"iface")`} {
		m, err := evmcompiler.Compile("order.go", imports+`;func Run()bool{var _ iface.Checker;return host.Accept(func(s string)bool{return s=="ok"})}`, first, second)
		if err != nil {
			t.Fatal(err)
		}
		p, err := evm.Link(m, first, second)
		if err != nil {
			t.Fatal(err)
		}
		var out [1]values.Value
		if err := evmtest.New(p).Run(context.Background(), "Run", nil, out[:]); err != nil {
			t.Fatal(err)
		}
		if !out[0].AsBool() {
			t.Fatal(out)
		}
	}
}
