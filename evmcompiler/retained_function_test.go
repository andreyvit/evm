package evmcompiler_test

import (
	"context"
	"errors"
	"runtime"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
)

func TestRetainedFunctions(t *testing.T) {
	for _, unoptimized := range []bool{false, true} {
		integer := &values.Shape{Type: values.IntType, Kind: values.IntKind, Bits: 64, Cells: 1}
		function := &values.Shape{Type: values.NewType("func(int)int", values.Pair), Kind: values.FunctionKind, Cells: 1, Params: []*values.Shape{integer}, Results: []*values.Shape{integer}}
		var handles []*evm.Callable
		host := evm.Package{Path: "host", Types: []evm.TypeDecl{{Shape: function}}, Functions: []evm.ExternalFunction{
			{Name: "Keep", Signature: evm.Signature{Params: []*values.Type{function.Type}}, Call: func(c *evm.VM) error {
				fValue, err := c.Program().Callable(c.CallArgument(0))
				f := &fValue
				if err != nil {
					return err
				}
				handles = append(handles, f)
				return nil
			}},
			{Name: "Native", Signature: evm.Signature{Params: []*values.Type{values.IntType}, Results: []*values.Type{values.IntType}}, Call: func(c *evm.VM) error { c.SetCallResult(0, values.Int(c.CallArgument(0).AsInt()+20)); return nil }},
		}}
		nativeType, err := evm.NewNativeFunctionType(evm.Signature{Params: []*values.Type{values.IntType}, Results: []*values.Type{values.IntType}})
		if err != nil {
			t.Fatal(err)
		}
		host.Functions = append(host.Functions, evm.ExternalFunction{Name: "Make", Signature: evm.Signature{Results: []*values.Type{function.Type}}, Call: func(c *evm.VM) error {
			v, err := c.NativeFunction(nativeType, func(c *evm.VM) error { c.SetCallResult(0, values.Int(c.CallArgument(0).AsInt()+40)); return nil })
			c.SetCallResult(0, v)
			return err
		}})
		module, err := (evmcompiler.Options{NoOptimization: unoptimized}).Compile("retained.go", `import "host"
type Counter struct { n int }
func(c *Counter)Add(n int)int{c.n+=n;return c.n}
func Keep(){
 n:=10
 host.Keep(func(x int)int{n+=x;return n})
 c:=&Counter{30};host.Keep(c.Add)
 host.Keep(host.Native)
 host.Keep(func(x int)int{for{};return x})
 host.Keep(host.Make())
}
func Nil(){host.Keep(nil)}
`, host)
		if err != nil {
			t.Fatal(err)
		}
		p, err := evm.Link(module, host)
		if err != nil {
			t.Fatal(err)
		}
		vm := evmtest.New(p)
		if err := vm.Run(context.Background(), "Keep", nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := vm.Reset(); err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		if len(handles) != 5 {
			t.Fatal(len(handles))
		}
		for i, want := range []int64{12, 32, 22} {
			var out [1]values.Value
			if err := evmtest.New(p).RunFunction(context.Background(), handles[i], []values.Value{values.Int(2)}, out[:]); err != nil {
				t.Fatal(err)
			}
			if out[0].AsInt() != want {
				t.Fatalf("handle %d: got %d want %d", i, out[0].AsInt(), want)
			}
		}
		var out [1]values.Value
		if err := evmtest.New(p).RunFunction(context.Background(), handles[4], []values.Value{values.Int(2)}, out[:]); err != nil || out[0].AsInt() != 42 {
			t.Fatalf("native closure: %v %v", out, err)
		}
		if err := vm.RunFunction(context.Background(), handles[0], []values.Value{values.Int(3)}, out[:]); err != nil || out[0].AsInt() != 15 {
			t.Fatalf("shared capture: %v %v", out, err)
		}
		other, err := evm.Link(module, host)
		if err != nil {
			t.Fatal(err)
		}
		if err := evmtest.New(other).RunFunction(context.Background(), handles[0], []values.Value{values.Int(1)}, out[:]); !errors.Is(err, evm.ErrFunction) {
			t.Fatal(err)
		}
		if err := evmtest.New(p).RunFunctionWithBudget(context.Background(), handles[3], []values.Value{values.Int(1)}, out[:], evm.Budget{Steps: 20}); !errors.Is(err, evm.ErrSteps) {
			t.Fatal(err)
		}
		if err := evmtest.New(p).Run(context.Background(), "Nil", nil, nil); !errors.Is(err, evm.ErrNilFunction) {
			t.Fatal(err)
		}
		sig := handles[0].Signature()
		sig.Params[0] = values.StringType
		if handles[0].Signature().Params[0] != values.IntType {
			t.Fatal("signature mutated")
		}
	}
}
