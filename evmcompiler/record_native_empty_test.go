package evmcompiler_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/values"
)

type nativeEmptyRow struct {
	Hidden int `kvo:"-"`
}

func TestNativeRecordWithoutProperties(t *testing.T) {
	schema := kvo.NewSchema()
	model := kvo.NewModel(schema, kvo.NewEntityType(schema, "Row", reflect.TypeFor[nativeEmptyRow]()), func(b *kvo.ModelBuilder) {})
	db, err := evm.ModelPackage("db", schema)
	if err != nil {
		t.Fatal(err)
	}
	row := db.Record(model, false)
	row.NativeAllocation = reflect.TypeFor[nativeEmptyRow]()
	binding := kvo.Bind(reflect.TypeFor[nativeEmptyRow](), model)
	a := &nativeEmptyRow{Hidden: 1}
	b := &nativeEmptyRow{Hidden: 2}
	var got *nativeEmptyRow
	db.Functions = []evm.ExternalFunction{
		{Name: "A", Signature: evm.Signature{Results: []*values.Type{row.Pointer}}, Call: func(c *evm.VM) error { c.SetCallResult(0, binding.Record(reflect.ValueOf(a)).Value()); return nil }},
		{Name: "B", Signature: evm.Signature{Results: []*values.Type{row.Pointer}}, Call: func(c *evm.VM) error { c.SetCallResult(0, binding.Record(reflect.ValueOf(b)).Value()); return nil }},
		{Name: "Keep", Signature: evm.Signature{Params: []*values.Type{row.Pointer}}, Call: func(c *evm.VM) error {
			r, err := c.Record(c.CallArgument(0), model)
			if err != nil {
				return err
			}
			got, _ = r.NativePointer().(*nativeEmptyRow)
			return nil
		}},
	}
	mod, err := evmcompiler.Compile("z.go", `import "db"
func Run() bool { x:=*db.A(); y:=*db.B(); db.Keep(&x); if x==y {panic("hidden ignored")}; return true }`, db)
	if err != nil {
		t.Fatal(err)
	}
	prog, err := evm.Link(mod, db)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	err = evmtest.New(prog).Run(context.Background(), "Run", nil, out[:])
	if err != nil || got == nil || got.Hidden != 1 || !out[0].AsBool() {
		t.Fatal(err, got, out)
	}
}
