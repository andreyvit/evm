package evmcompiler_test

import (
	"context"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/values"
)

func TestEmbeddedModelPointerThroughAny(t *testing.T) {
	schema := kvo.NewSchema()
	entity := kvo.NewEntityType(schema, "Row")
	name := kvo.NewProp(schema, 1, "name", kvo.TMachineString, nil)
	kvo.NewModel(schema, entity, func(b *kvo.ModelBuilder) { b.Prop(schema.MustPropByCode(name)) })
	db, err := evm.ModelPackage("db", schema)
	if err != nil {
		t.Fatal(err)
	}
	mod, err := evmcompiler.Compile("embedding.go", `import("db";"fmt")
 type Envelope struct {*db.Row}
 type Outer struct {*Envelope}
 func (e *Envelope) String()string{return "envelope"}
 func (e *Envelope) Label()string {if e.Row==nil{return "nil"};return e.Row.name}
 func Run()bool{
 var x any=&Outer{Envelope:&Envelope{}}
 var label interface{Label()string}=x.(*Outer)
 return x.(*Outer).Row==nil && label.Label()=="nil" && fmt.Sprint(x)=="envelope"
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
	if err := evmtest.New(program).Run(context.Background(), "Run", nil, out); err != nil || !out[0].AsBool() {
		t.Fatal(out, err)
	}
}
