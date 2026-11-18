package evmbind

import (
	"context"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

type voidPackageHost struct {
	named    *evm.NamedFunction
	retained *evm.FunctionHandle
}

func (h *voidPackageHost) Nested(_ *evm.Call, f *evm.NamedFunctionOf[func(func())]) {}

func (h *voidPackageHost) Run(_ *evm.Call, f func())                         { f() }
func (h *voidPackageHost) Named(_ *evm.Call, f *evm.NamedFunctionOf[func()]) { h.named = f }
func (h *voidPackageHost) Retain(_ *evm.Call, f *evm.FunctionOf[func()])     { h.retained = f }

func TestVoidCallbacksAcrossPackages(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "forward", true: "reverse"}[reverse], func(t *testing.T) {
			a, err := New("first", fstest.MapFS{"a.go": {Data: []byte(`package first;func Run(f func()){}`)}}, Options{Host: reflect.TypeFor[*voidPackageHost]()})
			if err != nil {
				t.Fatal(err)
			}
			b, err := New("second", fstest.MapFS{"b.go": {Data: []byte(`package second
 import "github.com/andreyvit/evm"
 func Named(f *evm.NamedFunctionOf[func()]){}
 func Nested(f *evm.NamedFunctionOf[func(func())]){}
 func Retain(f *evm.FunctionOf[func()]){}
 func Run(f func()){}
 `)}}, Options{Host: reflect.TypeFor[*voidPackageHost]()})
			if err != nil {
				t.Fatal(err)
			}
			hostA, hostB := &voidPackageHost{}, &voidPackageHost{}
			packages := []evm.Package{a.Bind(hostA), b.Bind(hostB)}
			imports := `import("first";"second")`
			if reverse {
				packages[0], packages[1] = packages[1], packages[0]
				imports = `import("second";"first")`
			}
			module, err := evmcompiler.Compile("run.go", imports+`
 var count int
 func Named(){count++}
 func Nested(f func()){f()}
 func Run()int{first.Run(func(){second.Run(func(){count+=2})});second.Named(Named);second.Nested(Nested);second.Retain(func(){count+=3});return count}
 `, packages...)
			if err != nil {
				t.Fatal(err)
			}
			program, err := evm.Link(module, packages...)
			if err != nil {
				t.Fatal(err)
			}
			result := make([]values.Value, 1)
			if err := evm.New(program).Run(context.Background(), "Run", nil, result); err != nil {
				t.Fatal(err)
			}
			if result[0].AsInt() != 2 || hostB.named == nil || hostB.retained == nil {
				t.Fatal("callback dispatch or retention failed")
			}
		})
	}
}

func TestNestedVoidCallbackDeclaration(t *testing.T) {
	s, err := New("nested", fstest.MapFS{"nested.go": {Data: []byte(`package nested;import "github.com/andreyvit/evm";func Nested(f *evm.NamedFunctionOf[func(func())]){}`)}}, Options{Host: reflect.TypeFor[*voidPackageHost]()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = evmcompiler.Compile("run.go", `import "nested";func Run(){nested.Nested(func(f func()){f()})}`, s.Bind(&voidPackageHost{}))
	if err != nil {
		t.Fatal(err)
	}
}
