package evmbind_test

import (
	"context"
	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
	"reflect"
	"testing"
)

type DiscoveredBar struct {
	Name   string
	Next   *DiscoveredBar
	hidden int
}

func (b *DiscoveredBar) Rename(s string) { b.Name = s }
func TestDiscoverNativeGraph(t *testing.T) {
	b := &DiscoveredBar{Name: "Ada", hidden: 7}
	b.Next = b
	_, packages, err := evmbind.Discover("api", evmbind.Discovery{Options: evmbind.Options{Functions: map[string]any{"Foo": func() *DiscoveredBar { return b }}}, Packages: []evmbind.PackageExposure{{Pattern: reflect.TypeFor[DiscoveredBar]().PkgPath(), Package: "data", Methods: true}}})
	if err != nil {
		t.Fatal(err)
	}
	module, err := evmcompiler.Compile("test.go", `import "api"; import "data";func Run()string { var b *data.DiscoveredBar=api.Foo();b.Next.Rename("Grace");return b.Name }`, packages...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(module, packages...)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(p).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if b.Name != "Grace" || b.hidden != 7 {
		t.Fatal(b)
	}
}

func TestDiscoverPackagePolicy(t *testing.T) {
	path := reflect.TypeFor[DiscoveredBar]().PkgPath()
	for _, tc := range []struct {
		name      string
		rules     []evmbind.PackageExposure
		wantError bool
		method    bool
	}{
		{"original", nil, false, false},
		{"descendants", []evmbind.PackageExposure{{Pattern: "github.com/andreyvit/evm/...", Methods: true}}, false, true},
		{"exact overrides", []evmbind.PackageExposure{{Pattern: path, Methods: false}, {Pattern: "github.com/andreyvit/evm/...", Methods: true}}, false, false},
		{"excluded", []evmbind.PackageExposure{{Pattern: "github.com/andreyvit/evm/...", Exclude: true}}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, pkgs, err := evmbind.Discover("api", evmbind.Discovery{Options: evmbind.Options{Functions: map[string]any{"Foo": func() *DiscoveredBar { return &DiscoveredBar{} }}}, Packages: tc.rules})
			if tc.wantError {
				if err == nil {
					t.Fatal("expected exclusion")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = evmcompiler.Compile("check.go", `import data "`+path+`";import "api";func Run()string{var b *data.DiscoveredBar=api.Foo();return b.Name}`, pkgs...)
			if err != nil {
				t.Fatal(err)
			}
			_, err = evmcompiler.Compile("method.go", `import "api";func Run(){api.Foo().Rename("Grace")}`, pkgs...)
			if (err == nil) != tc.method {
				t.Fatalf("method access: %v", err)
			}
			_, err = evmcompiler.Compile("private.go", `import "api";func Run()int{return api.Foo().hidden}`, pkgs...)
			if err == nil {
				t.Fatal("private field exposed")
			}
		})
	}
}

func TestDiscoverDynamicRoot(t *testing.T) {
	b := &DiscoveredBar{Name: "Ada"}
	path := reflect.TypeFor[DiscoveredBar]().PkgPath()
	_, pkgs, err := evmbind.Discover("api", evmbind.Discovery{Options: evmbind.Options{Types: []reflect.Type{reflect.TypeFor[DiscoveredBar]()}, Functions: map[string]any{"Data": func() any { return b }}}, Packages: []evmbind.PackageExposure{{Pattern: path, Package: "data"}}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := evmcompiler.Compile("dynamic.go", `import "api";import "data";func Run()bool{b:=api.Data().(*data.DiscoveredBar);b.Name="Grace";return b==api.Data().(*data.DiscoveredBar)}`, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(p).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if out[0].R != 1 || b.Name != "Grace" {
		t.Fatal(out, b)
	}
}
