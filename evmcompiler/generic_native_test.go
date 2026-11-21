package evmcompiler_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
)

func TestGenericNativeFunctions(t *testing.T) {
	pkg := evm.Package{Path: "host", Source: `package host
func First[T comparable](a, b T) T
`, Functions: []evm.ExternalFunction{{Name: "First", Instantiate: func(args []*values.Type) (evm.ExternalFunction, error) {
		if len(args) != 1 {
			return evm.ExternalFunction{}, fmt.Errorf("First needs one type argument")
		}
		return evm.ExternalFunction{Signature: evm.Signature{Params: []*values.Type{args[0], args[0]}, Results: args}, Call: func(c *evm.VM) error { c.SetCallResult(0, c.CallArgument(0)); return nil }}, nil
	}}}}
	for _, expr := range []string{"host.First(42, 8)", "host.First[int](42, 8)", "func() int { f:=host.First[int];return f(42,8) }()"} {
		t.Run(expr, func(t *testing.T) {
			m, err := evmcompiler.Compile("guest.go", `import "host"
type ID uint64
func Run() int { if host.First("first","second")!="first" {return -1};if host.First(ID(7),ID(8))!=7{return -2};return `+expr+` }`, pkg)
			if err != nil {
				t.Fatal(err)
			}
			p, err := evm.Link(m, pkg)
			if err != nil {
				t.Fatal(err)
			}
			out := make([]values.Value, 1)
			if err := evmtest.New(p).Run(context.Background(), "Run", nil, out); err != nil {
				t.Fatal(err)
			}
			if out[0].AsInt() != 42 {
				t.Fatal(out[0])
			}
			m.Imports[0].TypeArgs = nil
			if _, err := evm.Link(m, pkg); err == nil {
				t.Fatal("missing type arguments linked")
			}
		})
	}
	for _, expr := range []string{`host.First(1,"bad")`, `host.First([]int{1},[]int{2})`} {
		if _, err := evmcompiler.Compile("bad.go", `import "host";func Run(){_=`+expr+`}`, pkg); err == nil {
			t.Fatal("invalid generic call compiled", expr)
		}
	}
	bad := pkg
	bad.Functions = []evm.ExternalFunction{{Name: "First", Instantiate: func([]*values.Type) (evm.ExternalFunction, error) {
		return evm.ExternalFunction{Signature: evm.Signature{Params: []*values.Type{values.IntType}, Results: []*values.Type{values.IntType}}}, nil
	}}}
	if _, err := evmcompiler.Compile("bad.go", `import "host";func Run()int{return host.First(1,2)}`, bad); err == nil || !strings.Contains(err.Error(), "signature mismatch") {
		t.Fatal(err)
	}
}

func TestGenericNativeMethods(t *testing.T) {
	table := values.NewType("host.Table", values.Raw)
	pkg := evm.Package{Path: "host", Source: `//go:build go1.27
package host
type Table uint64
func (t Table) First[T comparable](a,b T) T
`, SharedTypes: []evm.SharedType{{Name: "Table", Type: table, Native: reflect.TypeFor[uint64]()}}, Methods: []evm.ExternalMethod{{Name: "First", Receiver: table, Instantiate: func(args []*values.Type) (evm.ExternalFunction, error) {
		if len(args) != 1 {
			return evm.ExternalFunction{}, fmt.Errorf("First needs one type argument")
		}
		return evm.ExternalFunction{Signature: evm.Signature{Params: []*values.Type{args[0], args[0]}, Results: args}, Call: func(c *evm.VM) error {
			v := c.CallArgument(1)
			if c.CallArgument(0).R == 0 {
				v = c.CallArgument(2)
			}
			c.SetCallResult(0, v)
			return nil
		}}, nil
	}}}}
	for _, expr := range []string{`host.Table(1).First(42,8)`, `host.Table(1).First[int](42,8)`, `host.Table.First[int](1,42,8)`, `func()int{f:=host.Table(1).First[int];return f(42,8)}()`} {
		t.Run(expr, func(t *testing.T) {
			m, err := evmcompiler.Compile("guest.go", `import "host";func Run()int{if host.Table(0).First("first","second")!="second"{return -1};return `+expr+`}`, pkg)
			if err != nil {
				t.Fatal(err)
			}
			p, err := evm.Link(m, pkg)
			if err != nil {
				t.Fatal(err)
			}
			out := make([]values.Value, 1)
			if err := evmtest.New(p).Run(context.Background(), "Run", nil, out); err != nil {
				t.Fatal(err)
			}
			if out[0].AsInt() != 42 {
				t.Fatal(out[0])
			}
		})
	}
}

func TestNativeSourceSignatureMismatch(t *testing.T) {
	pkg := evm.Package{Path: "host", Source: `package host;func First(a,b int) int`, Functions: []evm.ExternalFunction{{Name: "First", Signature: evm.Signature{Params: []*values.Type{values.StringType}, Results: []*values.Type{values.StringType}}, Call: func(*evm.VM) error { return nil }}}}
	if _, err := evmcompiler.Compile("bad.go", `import "host";func Run()int{return host.First(1,2)}`, pkg); err == nil || !strings.Contains(err.Error(), "native source signature mismatch") {
		t.Fatal(err)
	}
}

func TestGenericNativeDeclarationValidation(t *testing.T) {
	factory := func(args []*values.Type) (evm.ExternalFunction, error) {
		if len(args) != 1 {
			return evm.ExternalFunction{}, fmt.Errorf("one type argument required")
		}
		return evm.ExternalFunction{Signature: evm.Signature{Params: args, Results: args}, Call: func(c *evm.VM) error { c.SetCallResult(0, c.CallArgument(0)); return nil }}, nil
	}
	base := evm.Package{Path: "host", Source: `package host;func Identity[T any](v T) T`, Functions: []evm.ExternalFunction{{Name: "Identity", Instantiate: factory}}}
	for _, modify := range []func(*evm.Package){
		func(p *evm.Package) { p.Source = "" },
		func(p *evm.Package) { p.Functions[0].Call = func(*evm.VM) error { return nil } },
		func(p *evm.Package) { p.Functions[0].Signature.Params = []*values.Type{values.IntType} },
	} {
		p := base
		p.Functions = append([]evm.ExternalFunction(nil), base.Functions...)
		modify(&p)
		if _, err := evmcompiler.Compile("bad.go", `import "host";func Run()int{return host.Identity(42)}`, p); err == nil || !strings.Contains(err.Error(), "generic binding requires source") {
			t.Fatal(err)
		}
	}
	m, err := evmcompiler.Compile("good.go", `import "host";func Run()string{return host.Identity("value")}`, base)
	if err != nil {
		t.Fatal(err)
	}
	invalid := base
	invalid.Functions = []evm.ExternalFunction{{Name: "Identity", Instantiate: func(args []*values.Type) (evm.ExternalFunction, error) {
		f, err := factory(args)
		f.Direct = evm.DirectUnary(func(v uint64) uint64 { return v })
		return f, err
	}}}
	for _, call := range []string{`host.Identity(42)`, `func()int{f:=host.Identity[int];return f(42)}()`} {
		good, err := evmcompiler.Compile("direct.go", `import "host";func Run()int{return `+call+`}`, invalid)
		if err != nil {
			t.Fatal(err)
		}
		program, err := evm.Link(good, invalid)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]values.Value, 1)
		if err := evmtest.New(program).Run(context.Background(), "Run", nil, out); err != nil {
			t.Fatal(err)
		}
		if out[0].AsInt() != 42 {
			t.Fatal(out[0])
		}
	}
	if _, err := evmcompiler.Compile("bad.go", `import "host";func Run()string{return host.Identity("value")}`, invalid); err == nil || !strings.Contains(err.Error(), "invalid direct binding") {
		t.Fatal(err)
	}
	if _, err := evm.Link(m, invalid); err == nil || !strings.Contains(err.Error(), "invalid direct binding") {
		t.Fatal(err)
	}
	missingDirect := base
	missingDirect.Functions = []evm.ExternalFunction{{Name: "Identity", Instantiate: func(args []*values.Type) (evm.ExternalFunction, error) {
		f, err := factory(args)
		f.Direct.Op = evm.OpCallHostSS
		return f, err
	}}}
	if _, err := evmcompiler.Compile("bad.go", `import "host";func Run()string{return host.Identity("value")}`, missingDirect); err == nil || !strings.Contains(err.Error(), "invalid generic native binding") {
		t.Fatal(err)
	}
	if _, err := evm.Link(m, missingDirect); err == nil || !strings.Contains(err.Error(), "invalid generic binding") {
		t.Fatal(err)
	}
	invalidOwnership := base
	invalidOwnership.Functions = []evm.ExternalFunction{{Name: "Identity", Instantiate: func(args []*values.Type) (evm.ExternalFunction, error) {
		f, err := factory(args)
		f.Ownership = &evm.OwnershipEffects{NoRetain: true, NoInvalidate: true, Borrowed: []evm.BorrowSource{{Result: 9, Domain: "invalid"}}}
		return f, err
	}}}
	if _, err := evmcompiler.Compile("bad.go", `import "host";func Run()string{return host.Identity("value")}`, invalidOwnership); err == nil {
		t.Fatal("invalid ownership compiled")
	}
	m.Imports[0].TypeArgs[0] = nil
	if _, err := evm.Link(m, base); err == nil || !strings.Contains(err.Error(), "nil type argument") {
		t.Fatal(err)
	}
}

func TestGenericNativeMethodOwnership(t *testing.T) {
	data := []byte("live")
	table := values.NewType("host.Table", values.Raw)
	pkg := evm.Package{Path: "host", Source: `//go:build go1.27
package host
type Table uint64
func (t Table) Read[T any](key T) string
func End()
`, SharedTypes: []evm.SharedType{{Name: "Table", Type: table, Native: reflect.TypeFor[uint64]()}}, Functions: []evm.ExternalFunction{{Name: "End", Ownership: &evm.OwnershipEffects{NoRetain: true, Ends: []evm.ScopeEnd{{Domain: "buffer"}}}, Call: func(*evm.VM) error { copy(data, "dead"); return nil }}}, Methods: []evm.ExternalMethod{{Name: "Read", Receiver: table, Instantiate: func(args []*values.Type) (evm.ExternalFunction, error) {
		return evm.ExternalFunction{Signature: evm.Signature{Params: args, Results: []*values.Type{values.StringType}}, Ownership: &evm.OwnershipEffects{NoRetain: true, NoInvalidate: true, Borrowed: []evm.BorrowSource{{Result: 0, Domain: "buffer"}}}, Call: func(c *evm.VM) error {
			c.SetCallResult(0, values.String(unsafe.String(unsafe.SliceData(data), len(data))))
			return nil
		}}, nil
	}}}}
	for _, read := range []string{`host.Table(1).Read(7)`, `func()string{f:=host.Table(1).Read[int];return f(7)}()`, `host.Table.Read[int](1,7)`} {
		t.Run(read, func(t *testing.T) {
			copy(data, "live")
			m, err := evmcompiler.Compile("guest.go", `import "host";func Run()string{s:=`+read+`;host.End();return s}`, pkg)
			if err != nil {
				t.Fatal(err)
			}
			p, err := evm.Link(m, pkg)
			if err != nil {
				t.Fatal(err)
			}
			out := make([]values.Value, 1)
			if err := evmtest.New(p).Run(context.Background(), "Run", nil, out); err != nil {
				t.Fatal(err)
			}
			if out[0].AsString() != "live" {
				t.Fatal(out[0].AsString())
			}
		})
	}
}
