package bench_test

import (
	"context"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
)

// BenchmarkInterfaceHostMethod calls a native method of a host type through
// an interface 1,000 times: the call resolves the method in the type's
// method table and reaches a native that no instruction names. Environment
// links through prepared environments, whose programs share the method
// table and the native; PackageList gives the program its own.
func BenchmarkInterfaceHostMethod(b *testing.B) {
	number := &values.Shape{Type: values.NewType("h.Number", values.Raw), Kind: values.IntKind, Bits: 64, Cells: 1}
	sized := &values.Shape{Type: values.NewType("h size", values.Pair), Kind: values.FunctionKind, Cells: 1, Results: []*values.Shape{{Type: values.IntType, Kind: values.IntKind, Bits: 64, Cells: 1}}}
	sizer := &values.Shape{Type: values.NewType("h.Sizer", values.Pair), Kind: values.InterfaceKind, Cells: 1, Methods: []values.Method{{ID: "Size", Signature: sized}}}
	pkg := evm.Package{Path: "h", Types: []evm.TypeDecl{{Name: "Number", Shape: number}, {Name: "Sizer", Shape: sizer}}, Methods: []evm.ExternalMethod{
		{Name: "Size", Receiver: number.Type, Signature: evm.Signature{Results: []*values.Type{values.IntType}}, Call: func(c *evm.VM) error { return nil }},
	}}
	const source = `import "h"
func Run() int {
	var s h.Sizer = h.Number(3)
	total := 0
	for i := 0; i < 1000; i++ { total += s.Size() }
	return total
}`
	for _, mode := range []string{"PackageList", "Environment"} {
		b.Run(mode, func(b *testing.B) {
			var p *evm.Program
			if mode == "Environment" {
				env, err := evmcompiler.NewEnvironment(pkg)
				if err != nil {
					b.Fatal(err)
				}
				m, err := env.Compile("main.go", source, evmcompiler.Options{})
				if err != nil {
					b.Fatal(err)
				}
				links, err := env.NewLinkEnvironment(pkg)
				if err != nil {
					b.Fatal(err)
				}
				if p, err = links.Link(m); err != nil {
					b.Fatal(err)
				}
			} else {
				m, err := evmcompiler.Compile("main.go", source, pkg)
				if err != nil {
					b.Fatal(err)
				}
				if p, err = evm.Link(m, pkg); err != nil {
					b.Fatal(err)
				}
			}
			vm, ctx := evmtest.New(p), context.Background()
			var out [1]values.Value
			for i := 0; i < 2; i++ {
				if err := vm.Run(ctx, "Run", nil, out[:]); err != nil || out[0].R != 3000 {
					b.Fatalf("got %v, %v", out, err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := vm.Run(ctx, "Run", nil, out[:]); err != nil || out[0].R != 3000 {
					b.Fatalf("got %v, %v", out, err)
				}
			}
		})
	}
}
