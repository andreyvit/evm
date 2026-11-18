package evmcompiler_test

import (
	"context"
	"errors"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

func TestNativeInputCallback(t *testing.T) {
	module, err := evmcompiler.Compile("yield.go", `func Emit(yield func(int)){for i:=0;i<3;i++{yield(i)}}`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(module)
	if err != nil {
		t.Fatal(err)
	}
	typ, err := evm.NewNativeFunctionType(evm.Signature{Params: []*values.Type{values.IntType}})
	if err != nil {
		t.Fatal(err)
	}
	var seen []int64
	input, err := p.NativeFunction(typ, func(c *evm.Call) error { seen = append(seen, c.Argument(0).AsInt()); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(p).Run(context.Background(), "Emit", []values.Value{input}, nil); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 || seen[0] != 0 || seen[1] != 1 || seen[2] != 2 {
		t.Fatal(seen)
	}
	failure := errors.New("writer failed")
	failing, err := p.NativeFunction(typ, func(*evm.Call) error { return failure })
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(p).Run(context.Background(), "Emit", []values.Value{failing}, nil); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	other, err := evm.Link(module)
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(other).Run(context.Background(), "Emit", []values.Value{input}, nil); !errors.Is(err, evm.ErrFunction) {
		t.Fatal(err)
	}
	if value, err := p.NativeFunction(typ, nil); err != nil || value != (values.Value{}) {
		t.Fatalf("nil: %v %v", value, err)
	}
	if _, err := p.NativeFunction(nil, func(*evm.Call) error { return nil }); !errors.Is(err, evm.ErrFunction) {
		t.Fatal(err)
	}
}
