package evmcompiler_test

import (
	"context"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
)

func TestFunctionHandleReportsCaptures(t *testing.T) {
	function := &values.Shape{Type: values.NewType("func()int", values.Pair), Kind: values.FunctionKind, Cells: 1, Results: []*values.Shape{{Type: values.IntType, Kind: values.IntKind, Bits: 64, Cells: 1}}}
	var retained *evm.Callable
	host := evm.Package{Path: "host", Types: []evm.TypeDecl{{Shape: function}}, Functions: []evm.ExternalFunction{
		{Name: "Register", Signature: evm.Signature{Params: []*values.Type{function.Type}}, Call: func(c *evm.VM) error {
			var err error
			retainedValue, err := c.Program().Callable(c.CallArgument(0))
			retained = &retainedValue
			return err
		}},
	}}
	module, err := evmcompiler.Compile("handles.go", `import "host"
type Counter struct{ n int }
func (c *Counter) Next() int { c.n++; return c.n }
func One() int { return 1 }
func Named() { host.Register(One) }
func Literal() { host.Register(func() int { return 2 }) }
func Closure() { hits := 0; host.Register(func() int { hits++; return hits }) }
func Bound() { c := &Counter{}; host.Register(c.Next) }
`, host)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, host)
	if err != nil {
		t.Fatal(err)
	}
	for entry, want := range map[string]bool{"Named": false, "Literal": false, "Closure": true, "Bound": true} {
		if err := evmtest.New(program).Run(context.Background(), entry, nil, nil); err != nil {
			t.Fatal(entry, err)
		}
		if got := retained.HasCaptures(); got != want {
			t.Fatalf("%s: HasCaptures() = %v, want %v", entry, got, want)
		}
	}
}
