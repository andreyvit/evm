//go:build !evmreflect

package evmbind_test

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/aliasbindings"
	"github.com/andreyvit/evm/evmbind/internal/aliasconsumer"
	"github.com/andreyvit/evm/evmbind/internal/importbindings"
	"github.com/andreyvit/evm/evmbind/internal/importconsumer"
	"github.com/andreyvit/evm/evmbind/internal/importfixture"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
)

type WrappedOptions struct{ Options *importconsumer.Options }

type importHost struct{ options *importconsumer.Options }

func (h *importHost) Echo(_ *evm.VM, o *importconsumer.Options) importfixture.Permission {
	h.options = o
	o.Child.Permission = importfixture.Read
	return o.Permission
}

func TestSharedSchemaImports(t *testing.T) {
	dep, err := evmbind.New("data", importfixture.Source, evmbind.Options{Selective: true, Instances: []evmbind.Instance{{Type: "Box", Native: reflect.TypeFor[importfixture.Box[importfixture.Permission]](), Args: []reflect.Type{reflect.TypeFor[importfixture.Permission]()}}}, Types: []reflect.Type{reflect.TypeFor[importfixture.Permission](), reflect.TypeFor[importfixture.Child]()}})
	if err != nil {
		t.Fatal(err)
	}
	imports := map[string]*evmbind.Schema{"github.com/andreyvit/evm/evmbind/internal/importfixture": dep}
	generated, err := evmbind.Generate("host", "github.com/andreyvit/evm/evmbind/internal/importconsumer", "importbindings", importconsumer.Source, evmbind.Options{Imports: imports})
	if err != nil {
		t.Fatal(err)
	}
	checked, err := os.ReadFile("internal/importbindings/native_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(checked, generated) {
		t.Fatal("regenerate imported bindings")
	}
	for _, static := range []*evmbind.Static{nil, &importbindings.Bindings} {
		opt := evmbind.Options{Imports: imports, Host: reflect.TypeFor[*importHost](), Static: static}
		if static != nil {
			opt.Mode = evmbind.StaticOnly
		}
		schema, err := evmbind.New("host", importconsumer.Source, opt)
		if err != nil {
			t.Fatal(err)
		}
		if schema.NativeType(reflect.TypeFor[importfixture.Permission]()) != dep.Type("Permission") {
			t.Fatal("imported enum identity changed")
		}
		if schema.NativeType(reflect.TypeFor[importfixture.Child]()) != dep.Type("Child") {
			t.Fatal("imported struct identity changed")
		}
		wrapper, err := evmbind.New("wrapper", fstest.MapFS{"api.go": {Data: []byte(`package wrapper
         import "github.com/andreyvit/evm/evmbind/internal/importconsumer"
         type WrappedOptions struct { Options *importconsumer.Options }
        `)}}, evmbind.Options{Selective: true, Types: []reflect.Type{reflect.TypeFor[WrappedOptions]()}, Imports: map[string]*evmbind.Schema{"github.com/andreyvit/evm/evmbind/internal/importconsumer": schema}})
		if err != nil {
			t.Fatal(err)
		}
		if wrapper.NativeType(reflect.TypeFor[importfixture.Permission]()) != dep.Type("Permission") {
			t.Fatal("transitive enum identity changed")
		}
		host := &importHost{}
		pkgs := []evm.Package{dep.Package(), schema.Bind(host), wrapper.Package()}
		m, err := evmcompiler.Compile("imports.go", `import("host";"data")
   func Run()int {child:=&data.Child{Permission:data.Write};o:=&host.Options{Permission:data.Write,Child:child};o.Boxed.Value=data.Write;child.Value=data.Read;var p data.Permission=host.Echo(o);return int(p)*10+int(child.Permission)}`, pkgs...)
		if err != nil {
			t.Fatal(err)
		}
		p, err := evm.Link(m, pkgs...)
		if err != nil {
			t.Fatal(err)
		}
		var out [1]values.Value
		if err := evmtest.New(p).Run(context.Background(), "Run", nil, out[:]); err != nil {
			t.Fatal(err)
		}
		if out[0].R != 21 || host.options.Child.Permission != importfixture.Read || host.options.Boxed.Value != importfixture.Write || host.options.Child.Value != importfixture.Read {
			t.Fatalf("wrong imported values: %v %+v", out, host.options)
		}
	}
}

// Imported containers must resolve their nested native types in the owning
// schema, including enum map keys and pointers reached through nested slices.
type ImportCollections struct {
	Items [][]*ImportItem
	Named map[ImportKey]*ImportItem
}
type ImportItem struct{ Name string }
type ImportKey uint16
type ImportEnvelope struct{ Collections *ImportCollections }

func TestImportedNativeCollections(t *testing.T) {
	dep, err := evmbind.New("data", fstest.MapFS{"api.go": {Data: []byte(`package data
 type ImportKey uint16
 type ImportItem struct { Name string }
 type ImportCollections struct { Items [][]*ImportItem; Named map[ImportKey]*ImportItem }
 `)}}, evmbind.Options{Only: []string{"ImportCollections"}, Types: []reflect.Type{reflect.TypeFor[ImportCollections]()}})
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := evmbind.New("host", fstest.MapFS{"api.go": {Data: []byte(`package host
 import "example/data"
 type ImportEnvelope struct { Collections *data.ImportCollections }
 `)}}, evmbind.Options{Selective: true, Types: []reflect.Type{reflect.TypeFor[ImportEnvelope]()}, Imports: map[string]*evmbind.Schema{"example/data": dep}})
	if err != nil {
		t.Fatal(err)
	}
	pkgs := []evm.Package{dep.Package(), consumer.Package()}
	module, err := evmcompiler.Compile("collections.go", `import("data";"host")
 func Run()bool {
 item:=&data.ImportItem{Name:"before"}
 c:=&data.ImportCollections{Items:[][]*data.ImportItem{{item}},Named:map[data.ImportKey]*data.ImportItem{data.ImportKey(3):item}}
 e:=&host.ImportEnvelope{Collections:c}
 e.Collections.Items[0][0].Name="after"
 return c.Named[data.ImportKey(3)].Name=="after" && e.Collections==c
 }`, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evmtest.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if out[0].R != 1 {
		t.Fatal("imported collection lost native aliases")
	}
}

type ConsolidatedEnvelope struct{ Envelope *ImportEnvelope }

func TestConsolidatedSchemaImports(t *testing.T) {
	dep, err := evmbind.New("model", fstest.MapFS{"api.go": {Data: []byte(`package data
 type ImportKey uint16
 type ImportItem struct { Name string }
 type ImportCollections struct { Items [][]*ImportItem; Named map[ImportKey]*ImportItem }
 `)}}, evmbind.Options{Only: []string{"ImportCollections"}, Types: []reflect.Type{reflect.TypeFor[ImportCollections]()}})
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := evmbind.New("model", fstest.MapFS{"api.go": {Data: []byte(`package host
 import "example/data"
 type ImportEnvelope struct { Collections *data.ImportCollections }
 `)}}, evmbind.Options{Selective: true, Types: []reflect.Type{reflect.TypeFor[ImportEnvelope]()}, Imports: map[string]*evmbind.Schema{"example/data": dep}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = evmbind.New("entry", fstest.MapFS{"api.go": {Data: []byte(`package entry
 import "example/host"
 type ConsolidatedEnvelope struct { Envelope *host.ImportEnvelope }
 `)}}, evmbind.Options{Selective: true, Types: []reflect.Type{reflect.TypeFor[ConsolidatedEnvelope]()}, Imports: map[string]*evmbind.Schema{"example/host": consumer}})
	if err != nil {
		t.Fatal(err)
	}
}

type aliasHost struct{}

func (*aliasHost) Echo(_ *evm.VM, child *importfixture.Child) *importfixture.Child { return child }

func (*aliasHost) Many(_ *evm.VM, children []*importfixture.Child) map[string]*importfixture.Child {
	return map[string]*importfixture.Child{"first": children[0]}
}

func TestConsolidatedNativeTypeAliases(t *testing.T) {
	dep, err := evmbind.New("bh", importfixture.Source, evmbind.Options{Selective: true, Instances: []evmbind.Instance{{Type: "Box", Native: reflect.TypeFor[importfixture.Box[importfixture.Permission]](), Args: []reflect.Type{reflect.TypeFor[importfixture.Permission]()}}}, Types: []reflect.Type{reflect.TypeFor[importfixture.Child](), reflect.TypeFor[importfixture.Permission]()}})
	if err != nil {
		t.Fatal(err)
	}
	src := aliasconsumer.Source
	imports := map[string]*evmbind.Schema{"github.com/andreyvit/evm/evmbind/internal/importfixture": dep}
	for _, static := range []*evmbind.Static{nil, &aliasbindings.Bindings} {
		opt := evmbind.Options{Imports: imports, Host: reflect.TypeFor[*aliasHost](), Static: static}
		if static != nil {
			opt.Mode = evmbind.StaticOnly
		}
		schema, err := evmbind.New("bh", src, opt)
		if err != nil {
			t.Fatal(err)
		}
		if schema.NativeType(reflect.TypeFor[importfixture.Child]()) != dep.Type("Child") {
			t.Fatal("alias changed native type identity")
		}
		if schema.Type("Child") != dep.Type("Child") || schema.Type("*Child") != dep.Type("*Child") {
			t.Fatal("alias lookup changed type identity")
		}
		generated, err := evmbind.Generate("bh", "github.com/andreyvit/evm/evmbind/internal/aliasconsumer", "aliasbindings", src, evmbind.Options{Imports: imports})
		if err != nil {
			t.Fatal(err)
		}
		checked, err := os.ReadFile("internal/aliasbindings/native_gen.go")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(checked, generated) {
			t.Fatal("regenerate alias bindings")
		}
		if bytes.Contains(generated, []byte("api.Child")) {
			t.Fatal("alias generated a duplicate native type")
		}
		wrapper, err := evmbind.New("wrapper", fstest.MapFS{"wrapper.go": {Data: []byte(`package wrapper;import "native/aliasconsumer";type AliasEnvelope struct{Child *aliasconsumer.Child}`)}}, evmbind.Options{Selective: true, Imports: map[string]*evmbind.Schema{"native/aliasconsumer": schema}, Types: []reflect.Type{reflect.TypeFor[AliasEnvelope]()}})
		if err != nil {
			t.Fatal(err)
		}
		if wrapper.NativeType(reflect.TypeFor[importfixture.Child]()) != dep.Type("Child") {
			t.Fatal("transitive alias identity changed")
		}
		bound := schema.Bind(&aliasHost{})
		if len(bound.Constants) != 0 {
			t.Fatal("duplicate re-exported constants")
		}
		for _, typ := range bound.Types {
			if typ.Name == "Child" || typ.Name == "Permission" {
				t.Fatal("duplicate aliased declaration")
			}
		}
		merged := dep.Package()
		merged.Types = append(merged.Types, bound.Types...)
		merged.Functions = append(merged.Functions, bound.Functions...)
		merged.Methods = append(merged.Methods, bound.Methods...)
		merged.Constants = append(merged.Constants, bound.Constants...)
		module, err := evmcompiler.Compile("alias.go", `import "bh";func Run()bool{c:=&bh.Child{Permission:bh.Write};return bh.Echo(c)==c && bh.Many([]*bh.Child{c})["first"]==c}`, merged)
		if err != nil {
			t.Fatal(err)
		}
		program, err := evm.Link(module, merged)
		if err != nil {
			t.Fatal(err)
		}
		var out [1]values.Value
		if err := evmtest.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil {
			t.Fatal(err)
		}
		if !out[0].AsBool() {
			t.Fatal("alias handoff lost native identity")
		}
	}

}

type AliasEnvelope struct{ Child *importfixture.Child }

func TestConsolidatedConstantReexports(t *testing.T) {
	dep, err := evmbind.New("bh", importfixture.Source, evmbind.Options{Selective: true, Instances: []evmbind.Instance{{Type: "Box", Native: reflect.TypeFor[importfixture.Box[importfixture.Permission]](), Args: []reflect.Type{reflect.TypeFor[importfixture.Permission]()}}}, Types: []reflect.Type{reflect.TypeFor[importfixture.Child](), reflect.TypeFor[importfixture.Permission]()}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, decl string
		conflict   bool
		count      int
	}{
		{"same", "const Read = importfixture.Read", false, 0},
		{"conflict", "const Read = importfixture.Write", true, 0},
		{"different type", "const Read uint16 = 1", true, 0},
		{"new name", "const Extra = importfixture.Read", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := fstest.MapFS{"host.go": {Data: []byte("package bh;import \"github.com/andreyvit/evm/evmbind/internal/importfixture\";type Permission = importfixture.Permission;" + tc.decl)}}
			schema, err := evmbind.New("bh", source, evmbind.Options{Imports: map[string]*evmbind.Schema{"github.com/andreyvit/evm/evmbind/internal/importfixture": dep}})
			if tc.conflict {
				if err == nil {
					t.Fatal("conflicting constant accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(schema.Package().Constants) != tc.count {
				t.Fatal("wrong constant declarations", schema.Package().Constants)
			}
		})
	}
}

func TestConsolidatedUntypedConstant(t *testing.T) {
	dep, err := evmbind.New("bh", fstest.MapFS{"source.go": {Data: []byte("package constants;const Max=12")}}, evmbind.Options{})
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := evmbind.New("bh", fstest.MapFS{"source.go": {Data: []byte(`package bh;import "native/constants";const Max=constants.Max`)}}, evmbind.Options{Imports: map[string]*evmbind.Schema{"native/constants": dep}})
	if err != nil {
		t.Fatal(err)
	}
	if len(consumer.Package().Constants) != 0 {
		t.Fatal("duplicate untyped constant")
	}
}
