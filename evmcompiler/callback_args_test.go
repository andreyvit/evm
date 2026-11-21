package evmcompiler_test

import (
	"context"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
)

func TestCallbackArgumentsAndResults(t *testing.T) {
	callback := &values.Shape{Type: values.NewType("func(string,int)bool", values.Pair), Kind: values.FunctionKind, Cells: 1, Params: []*values.Shape{
		{Type: values.StringType, Kind: values.StringKind, Cells: 1},
		{Type: values.IntType, Kind: values.IntKind, Bits: 64, Cells: 1},
	}, Results: []*values.Shape{{Type: values.Boolean, Kind: values.BoolKind, Cells: 1}}}
	pkg := evm.Package{Path: "host", Types: []evm.TypeDecl{{Shape: callback}}, Functions: []evm.ExternalFunction{
		{Name: "Visit", Signature: evm.Signature{Params: []*values.Type{callback.Type}, Results: []*values.Type{values.IntType}}, Call: func(c *evm.VM) error {
			fn := c.CallArgument(0)
			for i := 0; i < 10; i++ {
				var result [1]values.Value
				if err := evmtest.Callback(c, fn, []values.Value{values.String("item"), values.Int(int64(i))}, result[:]); err != nil {
					return err
				}
				if result[0].R == 0 {
					c.SetCallResult(0, values.Int(int64(i+1)))
					return nil
				}
			}
			c.SetCallResult(0, values.Int(10))
			return nil
		}},
		{Name: "Decide", Signature: evm.Signature{Params: []*values.Type{values.StringType, values.IntType}, Results: []*values.Type{values.Boolean}}, Call: func(c *evm.VM) error {
			c.SetCallResult(0, values.Bool(c.CallArgument(0).AsString() == "item" && c.CallArgument(1).AsInt() < 1))
			return nil
		}},
	}}
	for _, noOptimization := range []bool{false, true} {
		m, err := (evmcompiler.Options{NoOptimization: noOptimization}).Compile("callback.go", `import "host"
   func Guest()int { sum:=0; count:=host.Visit(func(label string,n int)bool{if label!="item"{panic("bad argument")};sum+=n;return n<2});return sum*10+count }
   func Native()int{return host.Visit(host.Decide)}`, pkg)
		if err != nil {
			t.Fatal(err)
		}
		p, err := evm.Link(m, pkg)
		if err != nil {
			t.Fatal(err)
		}
		for name, want := range map[string]int64{"Guest": 33, "Native": 2} {
			var result [1]values.Value
			if err := evmtest.New(p).Run(context.Background(), name, nil, result[:]); err != nil {
				t.Fatal(err)
			}
			if result[0].AsInt() != want {
				t.Fatalf("%s: %d != %d", name, result[0].AsInt(), want)
			}
		}
	}
}
