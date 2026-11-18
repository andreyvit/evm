package evmcompiler_test

import (
	"context"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

func TestTypedNamedFunctionHandle(t *testing.T) {
	integer := &values.Shape{Type: values.IntType, Kind: values.IntKind, Bits: 64, Cells: 1}
	function := &values.Shape{Type: values.NewType("func(int)int", values.Pair), Kind: values.FunctionKind, Cells: 1, Params: []*values.Shape{integer}, Results: []*values.Shape{integer}}
	var retained *evm.NamedFunction
	host := evm.Package{Path: "host", Types: []evm.TypeDecl{{Shape: function}}, Functions: []evm.ExternalFunction{
		{Name: "Register", Signature: evm.Signature{Params: []*values.Type{function.Type}}, Call: func(c *evm.Call) error {
			var err error
			retained, err = c.Program().NamedFunctionValue(c.Argument(0))
			return err
		}},
		{Name: "Native", Signature: evm.Signature{Params: []*values.Type{values.IntType}, Results: []*values.Type{values.IntType}}, Call: func(c *evm.Call) error { c.SetResult(0, c.Argument(0)); return nil }},
	}}
	for _, unoptimized := range []bool{false, true} {
		module, err := (evmcompiler.Options{NoOptimization: unoptimized}).Compile("handles.go", `import "host"
func Twice(n int)int{return n*2}
func Register(){host.Register(Twice)}
func RegisterNil(){host.Register(nil)}
func RegisterNative(){host.Register(host.Native)}
func RegisterCapture(){offset:=3;host.Register(func(n int)int{return n+offset})}
`, host)
		if err != nil {
			t.Fatal(err)
		}
		program, err := evm.Link(module, host)
		if err != nil {
			t.Fatal(err)
		}
		if err := evm.New(program).Run(context.Background(), "Register", nil, nil); err != nil {
			t.Fatal(err)
		}
		if retained == nil || len(retained.Signature.Params) != 1 || retained.Signature.Params[0] != values.IntType || len(retained.Signature.Results) != 1 || retained.Signature.Results[0] != values.IntType {
			t.Fatalf("invalid retained handle: %+v", retained)
		}
		var result [1]values.Value
		if err := evm.New(program).Run(context.Background(), retained.Name, []values.Value{values.Int(21)}, result[:]); err != nil {
			t.Fatal(err)
		}
		if result[0].AsInt() != 42 {
			t.Fatal(result[0])
		}
		for _, name := range []string{"RegisterNil", "RegisterNative", "RegisterCapture"} {
			if err := evm.New(program).Run(context.Background(), name, nil, nil); err == nil {
				t.Fatalf("%s accepted an unsupported entry", name)
			}
		}
	}
	if _, err := evmcompiler.Compile("wrong.go", `import "host";func Wrong(s string)int{return 0};func Run(){host.Register(Wrong)}`, host); err == nil {
		t.Fatal("wrong callback signature compiled")
	}
}
