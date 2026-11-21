package evmcompiler_test

import (
	"slices"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/values"
)

// The types in the signature of a native of a package with source are
// shaped when the environment is built: its modules list the environment's
// shapes for them and build none of their own, and so share their linked
// form.
func TestEnvironmentShapesNativeSourceSignatures(t *testing.T) {
	s := kvo.NewSchema()
	order := kvo.NewEntityType(s, "order")
	total := kvo.NewProp(s, 7, "total", kvo.TInt64, nil)
	model := kvo.NewModel(s, order, func(b *kvo.ModelBuilder) { b.Prop(s.MustPropByCode(total)) })
	record := evm.NewRecordType("Order", model, false)
	db := evm.Package{Path: "db", Records: []*evm.RecordType{record}}
	tools := evm.Package{Path: "tools", Source: "package tools\n", Functions: []evm.ExternalFunction{{Name: "Total", Signature: evm.Signature{Params: []*values.Type{record.Pointer}, Results: []*values.Type{values.Int64}}, Call: func(c *evm.Call) error {
		c.SetResult(0, values.Int(7))
		return nil
	}}}}
	packages := []evm.Package{db, tools}
	env, err := evmcompiler.NewEnvironment(packages...)
	if err != nil {
		t.Fatal(err)
	}
	shared := env.Shapes()
	const source = `import "tools"
func Run() int64 { return tools.Total(nil) }`
	for round := 0; round < 2; round++ {
		m, err := env.Compile("main.go", source, evmcompiler.Options{})
		if err != nil {
			t.Fatal(err)
		}
		listed := false
		for shape := range m.ListedShapes() {
			if shape.Type != record.Pointer {
				continue
			}
			listed = true
			if !slices.Contains(shared, shape) {
				t.Errorf("round %d: the module built a shape of its own for %s", round, shape.Type.Name())
			}
		}
		if !listed {
			t.Fatalf("round %d: the module lists no shape for the parameter type", round)
		}
		lazy, err := evmcompiler.Compile("main.go", source, packages...)
		if err != nil {
			t.Fatal(err)
		}
		if difference := evmcompiler.ModuleDifference(lazy, m); difference != "" {
			t.Fatal(difference)
		}
		p, err := evm.Link(m, packages...)
		if err != nil {
			t.Fatal(err)
		}
		if p.Shape(record.Pointer) == nil {
			t.Fatal("the program does not know the shape of a signature type")
		}
	}
}
