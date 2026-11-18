//go:build !evmreflect

package evmbind_test

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/interfacebindings"
	"github.com/andreyvit/evm/evmbind/internal/interfaceconsumer"
	"github.com/andreyvit/evm/evmbind/internal/interfacedep"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

type importedNamedValue struct{ label string }

func (v *importedNamedValue) Label() string {
	if v == nil {
		return "nil"
	}
	return v.label
}

type importedNamedHost struct{ value interfacedep.Named }

func (h *importedNamedHost) Get(*evm.Call) interfacedep.Named                          { return h.value }
func (h *importedNamedHost) Echo(_ *evm.Call, x interfacedep.Named) interfacedep.Named { return x }

func (h *importedNamedHost) EchoLocal(_ *evm.Call, x interfaceconsumer.LocalNamed) interfaceconsumer.LocalNamed {
	return x
}

func (h *importedNamedHost) Any(*evm.Call) any { return h.value }

func TestImportedInterfaces(t *testing.T) {
	dep, err := evmbind.New("domain", interfacedep.Source, evmbind.Options{Selective: true, Types: []reflect.Type{reflect.TypeFor[interfacedep.Named](), reflect.TypeFor[interfacedep.NamedValue]()}, Methods: map[string][]string{"NamedValue": {"Label"}}, Implementations: []evmbind.Implementation{evmbind.Pointer[importedNamedValue]()}})
	if err != nil {
		t.Fatal(err)
	}
	imports := map[string]*evmbind.Schema{"github.com/andreyvit/evm/evmbind/internal/interfacedep": dep}
	generated, err := evmbind.Generate("host", "github.com/andreyvit/evm/evmbind/internal/interfaceconsumer", "interfacebindings", interfaceconsumer.Source, evmbind.Options{Imports: imports})
	if err != nil {
		t.Fatal(err)
	}
	checked, err := os.ReadFile("internal/interfacebindings/native_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, checked) {
		t.Fatal("regenerate imported interface bindings")
	}
	for _, static := range []*evmbind.Static{nil, &interfacebindings.Bindings} {
		opt := evmbind.Options{Imports: imports, Host: reflect.TypeFor[*importedNamedHost](), Static: static}
		if static != nil {
			opt.Mode = evmbind.StaticOnly
		}
		schema, err := evmbind.New("host", interfaceconsumer.Source, opt)
		if err != nil {
			t.Fatal(err)
		}
		host := &importedNamedHost{value: &importedNamedValue{label: "native"}}
		packages := []evm.Package{dep.Bind(nil), schema.Bind(host)}
		module, err := evmcompiler.Compile("test.go", `import "host";func Run()bool{x:=host.Get();return host.Echo(x)==x && host.EchoLocal(x)==x && x.Label()=="native" && host.Echo(nil)==nil && host.EchoLocal(nil)==nil};func Recover()(s string){defer func(){s=recover().(error).Error()}();panic(host.Any());return};func TypedNil()bool{x:=host.Get();return x!=nil && host.Echo(x).Label()=="nil" && host.EchoLocal(x).Label()=="nil"}`, packages...)
		if err != nil {
			t.Fatal(err)
		}
		program, err := evm.Link(module, packages...)
		if err != nil {
			t.Fatal(err)
		}
		var out [1]values.Value
		if err := evm.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil {
			t.Fatal(err)
		}
		if out[0].R != 1 {
			t.Fatal("imported interface identity or nil changed")
		}
		host.value = (*importedNamedValue)(nil)
		if err := evm.New(program).Run(context.Background(), "TypedNil", nil, out[:]); err != nil {
			t.Fatal(err)
		}
		if out[0].R != 1 {
			t.Fatal("typed nil changed")
		}
		host.value = &interfacedep.NamedValue{Text: "native"}
		if err := evm.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil {
			t.Fatal(err)
		}
		if out[0].R != 1 {
			t.Fatal("declared native interface identity changed")
		}
		if err := evm.New(program).Run(context.Background(), "Recover", nil, out[:]); err != nil {
			t.Fatal(err)
		}
		if out[0].AsString() != "native" {
			t.Fatal(out[0].AsString())
		}
		host.value = (*interfacedep.NamedValue)(nil)
		if err := evm.New(program).Run(context.Background(), "TypedNil", nil, out[:]); err != nil {
			t.Fatal(err)
		}
		if out[0].R != 1 {
			t.Fatal("declared native typed nil changed")
		}

	}
}
