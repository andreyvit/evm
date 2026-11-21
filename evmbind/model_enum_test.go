package evmbind_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/modelenum"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/kvo"
)

func TestNativeModelEnumProperty(t *testing.T) {
	schema := kvo.NewSchema()
	kindType := kvo.NewScalarSubtype[modelenum.Kind]("Kind", kvo.TInt)
	kind := kvo.NewSignedProp[modelenum.Kind](schema, 1, "kind", kindType)
	typ := kvo.NewEntityType(schema, "Row")
	model := kvo.NewModel(schema, typ, func(b *kvo.ModelBuilder) { b.Prop(kind) })
	model.RegisterNativeType(reflect.TypeFor[modelenum.Row]())
	records, err := evm.ModelPackage("rows", schema)
	if err != nil {
		t.Fatal(err)
	}
	native, err := evmbind.New("native", modelenum.Source, evmbind.Options{
		Only:      []string{"Kind", "Row"},
		Models:    &records,
		Types:     []reflect.Type{reflect.TypeFor[modelenum.Row](), reflect.TypeFor[modelenum.Kind]()},
		Methods:   map[string][]string{"Kind": {"String"}},
		Functions: map[string]any{"Current": modelenum.Current},
	})
	if err != nil {
		t.Fatal(err)
	}
	packages := []evm.Package{records, native.Bind(nil)}
	if _, err := evmcompiler.Compile("only-rows.go", `import "rows"
func Read(row *rows.Row) string {return row.kind.String()}`, packages...); err != nil {
		t.Fatal(err)
	}
	module, err := evmcompiler.Compile("enum.go", `import "native"
 func Run(){row:=native.Current();if row.kind!=native.Ready || row.kind.String()!="ready" {panic("enum identity lost")}}`, packages...)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, packages...)
	if err != nil {
		t.Fatal(err)
	}
	if err := evmtest.New(program).Run(context.Background(), "Run", nil, nil); err != nil {
		t.Fatal(err)
	}
}
