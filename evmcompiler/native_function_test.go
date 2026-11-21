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

func TestRuntimeNativeFunctions(t *testing.T) {
	integer := &values.Shape{Type: values.IntType, Kind: values.IntKind, Cells: 1, Bits: 64}
	boolean := &values.Shape{Type: values.Boolean, Kind: values.BoolKind, Cells: 1}
	callback := &values.Shape{Type: values.NewType("func(int)bool", values.Pair), Kind: values.FunctionKind, Cells: 1, Params: []*values.Shape{integer}, Results: []*values.Shape{boolean}}
	seq := &values.Shape{Type: values.NewType("func(func(int)bool)", values.Pair), Kind: values.FunctionKind, Cells: 1, Params: []*values.Shape{callback}}
	seqType, err := evm.NewNativeFunctionType(evm.Signature{Params: []*values.Type{callback.Type}})
	if err != nil {
		t.Fatal(err)
	}
	callbackType, err := evm.NewNativeFunctionType(evm.Signature{Params: []*values.Type{values.IntType}, Results: []*values.Type{values.Boolean}})
	if err != nil {
		t.Fatal(err)
	}
	var saved values.Value
	calls := 0
	pkg := evm.Package{Path: "host", Types: []evm.TypeDecl{{Shape: callback}, {Shape: seq}}, Functions: []evm.ExternalFunction{
		{Name: "Sequence", Signature: evm.Signature{Params: []*values.Type{values.IntType}, Results: []*values.Type{seq.Type}}, Call: func(c *evm.VM) error {
			start := c.CallArgument(0).AsInt()
			v, err := c.NativeFunction(seqType, func(c *evm.VM) error {
				yield := c.CallArgument(0)
				for n := start; n < start+10; n++ {
					var result [1]values.Value
					if err := evmtest.Callback(c, yield, []values.Value{values.Int(n)}, result[:]); err != nil {
						return err
					}
					if result[0].R == 0 {
						break
					}
				}
				return nil
			})
			c.SetCallResult(0, v)
			return err
		}},
		{Name: "Predicate", Signature: evm.Signature{Results: []*values.Type{callback.Type}}, Call: func(c *evm.VM) error {
			count := 0
			v, err := c.NativeFunction(callbackType, func(c *evm.VM) error { count++; calls++; c.SetCallResult(0, values.Bool(count < 3)); return nil })
			if err == nil {
				saved = v
			}
			c.SetCallResult(0, v)
			return err
		}},
		{Name: "Saved", Signature: evm.Signature{Results: []*values.Type{callback.Type}}, Call: func(c *evm.VM) error { c.SetCallResult(0, saved); return nil }},
		{Name: "Nil", Signature: evm.Signature{Results: []*values.Type{callback.Type}}, Call: func(c *evm.VM) error {
			v, err := c.NativeFunction(callbackType, nil)
			c.SetCallResult(0, v)
			return err
		}},
		{Name: "Wrong", Signature: evm.Signature{Results: []*values.Type{callback.Type}}, Call: func(c *evm.VM) error {
			v, err := c.NativeFunction(seqType, func(*evm.VM) error { t.Fatal("wrong signature invoked"); return nil })
			c.SetCallResult(0, v)
			return err
		}},
		{Name: "GC", Call: func(c *evm.VM) error { runtime.GC(); return nil }},
	}}
	source := `import "host"
 func Sum()int { sum:=0; sequence:=host.Sequence(7); host.GC();for n:=range sequence {sum+=n;if n==9 {break}};return sum }
 func Callback(){ host.Sequence(7)(host.Predicate()) }
 func Deferred(){ f:=host.Predicate();defer f(8); f(7) }
 func Retained()bool{return host.Saved()(99)}
 func Nil()bool{return host.Nil()==nil}
 func NilCall(){host.Nil()(0)}
 func WrongCall(){host.Wrong()(0)}
 func WrongDefer(){defer host.Wrong()(0)}
 func Spin(){host.Sequence(0)(func(n int)bool{for{}})}
 func Allocate(){for{host.Predicate()}}

 func Nested()int {sum:=0;for x:=range host.Sequence(1){for y:=range host.Sequence(4){sum+=x*y;break};break};return sum}
 `
	for _, noOptimization := range []bool{false, true} {
		m, err := (evmcompiler.Options{NoOptimization: noOptimization}).Compile("native-functions.go", source, pkg)
		if err != nil {
			t.Fatal(err)
		}
		p, err := evm.Link(m, pkg)
		if err != nil {
			t.Fatal(err)
		}
		for name, want := range map[string]int64{"Sum": 24, "Nested": 4, "Nil": 1} {
			var result [1]values.Value
			if err := evmtest.New(p).Run(context.Background(), name, nil, result[:]); err != nil {
				t.Fatal(name, err)
			}
			if result[0].AsInt() != want {
				t.Fatalf("%s: %d != %d", name, result[0].AsInt(), want)
			}
		}
		calls = 0
		if err := evmtest.New(p).Run(context.Background(), "Callback", nil, nil); err != nil {
			t.Fatal(err)
		}
		if calls != 3 {
			t.Fatalf("callback calls = %d", calls)
		}
		calls = 0
		if err := evmtest.New(p).Run(context.Background(), "Deferred", nil, nil); err != nil {
			t.Fatal(err)
		}
		if calls != 2 {
			t.Fatalf("deferred calls = %d", calls)
		}
		runtime.GC()
		var result [1]values.Value
		if err := evmtest.New(p).Run(context.Background(), "Retained", nil, result[:]); err != nil {
			t.Fatal(err)
		}
		if result[0].R != 0 || calls != 3 {
			t.Fatal("native closure did not retain state")
		}
		if err := evmtest.New(p).Run(context.Background(), "NilCall", nil, nil); !errors.Is(err, evm.ErrNilFunction) {
			t.Fatalf("nil call: %v", err)
		}
		for _, name := range []string{"WrongCall", "WrongDefer"} {
			if err := evmtest.New(p).Run(context.Background(), name, nil, nil); !errors.Is(err, evm.ErrFunction) {
				t.Fatalf("%s: %v", name, err)
			}
		}
		if err := evmtest.New(p).RunWithBudget(context.Background(), "Spin", nil, nil, evm.Budget{Steps: 100}); !errors.Is(err, evm.ErrSteps) {
			t.Fatalf("shared steps: %v", err)
		}
		if err := evmtest.New(p).RunWithBudget(context.Background(), "Allocate", nil, nil, evm.Budget{AllocationBytes: 4096}); !errors.Is(err, evm.ErrAllocation) {
			t.Fatalf("native closure allocation: %v", err)
		}
		other, err := evm.Link(m, pkg)
		if err != nil {
			t.Fatal(err)
		}
		if err := evmtest.New(other).Run(context.Background(), "Retained", nil, result[:]); !errors.Is(err, evm.ErrFunction) {
			t.Fatalf("foreign callback: %v", err)
		}
	}
}
