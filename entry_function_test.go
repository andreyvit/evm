package evm

import (
	"context"
	"errors"
	"testing"

	"github.com/andreyvit/evm/values"
)

func TestEntryFunction(t *testing.T) {
	sig := Signature{Params: []*values.Type{values.Int64}, Results: []*values.Type{values.Int64}}
	p, err := Link(&Module{
		Functions: []Function{
			{Name: "Identity", Signature: sig, RCount: 1, Code: []Instruction{ABC(OpReturn, 0, 0, 0)}},
			{Name: "Unused", Code: []Instruction{ABC(OpReturn, 0, 0, 0)}},
		},
		FunctionRefs: []FunctionRef{{Index: 0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	named, err := p.NamedFunctionValue(values.Value{R: 1})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := p.EntryFunction(named)
	if err != nil {
		t.Fatal(err)
	}
	value, err := handle.Value(p, sig)
	if err != nil {
		t.Fatal(err)
	}
	if value != (values.Value{R: 1}) {
		t.Fatalf("function value = %v", value)
	}
	inCallResult := make([]values.Value, 1)
	if err := New(p).RunNative(context.Background(), func(c *Call) error {
		return c.RunCallbackArgs(value, []values.Value{values.Int(17)}, inCallResult)
	}, Budget{}); err != nil {
		t.Fatal(err)
	}
	if inCallResult[0].R != 17 {
		t.Fatalf("in-call result = %v", inCallResult)
	}
	result := make([]values.Value, 1)
	if err := New(p).RunFunction(context.Background(), handle, []values.Value{values.Int(42)}, result); err != nil {
		t.Fatal(err)
	}
	if result[0].R != 42 {
		t.Fatalf("result = %v", result)
	}
	for _, tc := range []struct {
		name string
		fn   *NamedFunction
		want error
	}{
		{"nil", nil, ErrNilFunction},
		{"missing", &NamedFunction{Name: "Missing"}, ErrFunction},
		{"signature", &NamedFunction{Name: "Identity"}, ErrFunction},
		{"unreferenced", &NamedFunction{Name: "Unused"}, ErrFunction},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := p.EntryFunction(tc.fn); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}
