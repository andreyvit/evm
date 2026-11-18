package evm_test

import (
	"context"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

func TestProgramAssertValue(t *testing.T) {
	m, err := evmcompiler.Compile("assert.go", `
 type Item struct{N int}
 func(p *Item) Number()int{return p.N}
 type Numbered interface{Number()int}
 func Concrete(v *Item){}
 func Interface(v Numbered){}
 func Targets()(func(*Item),func(Numbered)){return Concrete,Interface}
 func Pointer()any{return &Item{N:42}}
 func NilPointer()any{var p *Item;return p}
 func Wrong()any{return "wrong"}
 func Empty()any{return nil}
 `)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m)
	if err != nil {
		t.Fatal(err)
	}
	vm := evm.New(p)
	defer vm.Reset()
	var fns [2]values.Value
	if err := vm.Run(context.Background(), "Targets", nil, fns[:]); err != nil {
		t.Fatal(err)
	}
	for _, f := range fns {
		fn, err := p.FunctionValue(f)
		if err != nil {
			t.Fatal(err)
		}
		target := fn.Signature().Params[0]
		for _, name := range []string{"Pointer", "NilPointer", "Wrong", "Empty"} {
			var out [1]values.Value
			if err := vm.Run(context.Background(), name, nil, out[:]); err != nil {
				t.Fatal(err)
			}
			got, ok, err := p.AssertValue(out[0], target)
			want := name == "Pointer" || name == "NilPointer"
			if err != nil || ok != want {
				t.Fatalf("%s as %s: %v, %v", name, target.Name(), ok, err)
			}
			if !ok && got != (values.Value{}) {
				t.Fatal("failed assertion retained payload")
			}
			if ok {
				shape, payload, err := p.UnpackInterface(out[0])
				if err != nil {
					t.Fatal(err)
				}
				if target == shape.Type && got != payload {
					t.Fatal("concrete assertion changed pointer")
				}
				if target != shape.Type && got != out[0] {
					t.Fatal("interface assertion changed value")
				}
			}
		}
	}
	if _, _, err := p.AssertValue(values.Value{}, nil); err == nil {
		t.Fatal("nil type accepted")
	}
}
