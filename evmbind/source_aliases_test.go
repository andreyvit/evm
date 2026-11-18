package evmbind_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/importfixture"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

type AliasFields struct {
	Key    *string
	Plain  *string
	Visit  func(importfixture.Permission) *importfixture.Child
	Direct func(importfixture.Permission) *importfixture.Child
}

type LocalAliasTarget struct{ Value int }

func TestNativeSourceAliases(t *testing.T) {
	dep, err := evmbind.NewTypes("data", importfixture.Source, nil, evmbind.Options{Instances: []evmbind.Instance{{Type: "Box", Native: reflect.TypeFor[importfixture.Box[importfixture.Permission]](), Args: []reflect.Type{reflect.TypeFor[importfixture.Permission]()}}}, Types: []reflect.Type{reflect.TypeFor[importfixture.Permission](), reflect.TypeFor[importfixture.Child]()}})
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{"api.go": {Data: []byte(`package aliases
 import "github.com/andreyvit/evm/evmbind/internal/importfixture"
 type Key = string
 type PromoID = importfixture.Permission
 type VendibleID = PromoID
 type Child = importfixture.Child
 type Children = []*Child
 type Visit = func(PromoID) *Child
 type AliasFields struct {
  Key *Key
  Plain *string
  Visit Visit
  Direct func(importfixture.Permission) *importfixture.Child
 }
 type Forward = LocalAliasTarget
 type LocalAliasTarget struct { Value int }
 `)}}
	s, err := evmbind.NewTypes("aliases", files, nil, evmbind.Options{Types: []reflect.Type{reflect.TypeFor[AliasFields](), reflect.TypeFor[LocalAliasTarget]()}, Imports: map[string]*evmbind.Schema{"github.com/andreyvit/evm/evmbind/internal/importfixture": dep}})
	if err != nil {
		t.Fatal(err)
	}
	if s.Type("PromoID") != dep.Type("Permission") || s.Type("VendibleID") != dep.Type("Permission") || s.Type("Key") != values.StringType || s.Type("*Key") != s.Type("*string") {
		t.Fatal("alias changed runtime identity")
	}
	shape := s.Package().Types
	for _, d := range shape {
		if d.Name == "AliasFields" && (d.Shape.Fields[0].Shape.Type != d.Shape.Fields[1].Shape.Type || d.Shape.Fields[2].Shape.Type != d.Shape.Fields[3].Shape.Type) {
			t.Fatal("composite aliases changed runtime identity")
		}
	}
	reexport, err := evmbind.NewTypes("aliases", fstest.MapFS{"api.go": {Data: []byte(`package reexport;import "example/aliases";type PromoID = aliases.PromoID`)}}, nil, evmbind.Options{Imports: map[string]*evmbind.Schema{"example/aliases": s}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reexport.Package().Aliases) != 0 {
		t.Fatal("same-package alias reexport emitted twice")
	}
	pkgs := []evm.Package{dep.Package(), s.Package()}
	m, err := evmcompiler.Compile("use.go", `import("data";"aliases")
 func Run(callbacks *aliases.AliasFields)bool {
 var id aliases.PromoID=data.Write
 var original data.Permission=id
 var next aliases.VendibleID=original
 key:=aliases.Key("name")
 var p *string=&key
 var back *aliases.Key=p
 child:=&aliases.Child{Permission:next}
 var originalChild *data.Child=child
 items:=aliases.Children{child}
 f:=aliases.AliasFields{Key:back,Plain:p}
 callbacks.Direct=callbacks.Visit
 local:=aliases.Forward{Value:7}
 var same aliases.LocalAliasTarget=local
 return callbacks.Direct(data.Read).Permission==data.Read && originalChild==items[0] && same.Value==7 && *f.Key=="name"
 }`, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	arg, err := s.NativePointer(&AliasFields{Visit: func(p importfixture.Permission) *importfixture.Child { return &importfixture.Child{Permission: p} }})
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(p).Run(context.Background(), "Run", []values.Value{arg}, out[:]); err != nil || !out[0].AsBool() {
		t.Fatal(out, err)
	}
	printed, err := evmcompiler.PackageSource("aliases", pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"type Key = string", "type PromoID = data.Permission", "type Forward = LocalAliasTarget"} {
		if !strings.Contains(printed, want) {
			t.Fatalf("missing %s:\n%s", want, printed)
		}
	}
	if _, err := evmcompiler.Compile("roundtrip.go", `import("aliases";"data");func Run(x aliases.PromoID)data.Permission{return x}`, dep.Package(), evm.Package{Path: "aliases", Source: printed}); err != nil {
		t.Fatal("printed aliases:", err)
	}
	if _, err := evmcompiler.Compile("missing.go", `import "aliases";func Run(){var x aliases.PromoID;_=x}`, s.Package()); err == nil {
		t.Fatal("missing target package accepted")
	}
}

func TestSelectNativeAlias(t *testing.T) {
	files := fstest.MapFS{"api.go": {Data: []byte(`package aliases;type Key = string;type Unused chan int`)}}
	s, err := evmbind.NewTypes("aliases", files, []string{"Key"}, evmbind.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Type("Key") != values.StringType {
		t.Fatal("basic alias lost identity")
	}
	for _, d := range s.Package().Types {
		if d.Name == "Key" {
			t.Fatal("alias created a named runtime type")
		}
	}
}

func TestNativeAliasErrors(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{`package aliases;type G[T any] = []T`, "generic native alias"},
		{`package aliases;import "github.com/andreyvit/evm";type Callback = evm.FunctionOf[func()]`, "EVM function handle"},
		{`package aliases;import "github.com/andreyvit/evm";type Callback = *evm.FunctionOf[func()]`, "EVM function handle"},
	} {
		_, err := evmbind.NewTypes("aliases", fstest.MapFS{"api.go": {Data: []byte(tc.src)}}, nil, evmbind.Options{})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("got %v, want %s", err, tc.want)
		}
	}
}

type aliasFunctionHost struct{ a, b *evm.NamedFunction }

func (h *aliasFunctionHost) A(_ *evm.Call, fn *evm.NamedFunctionOf[func([]int)]) { h.a = fn }
func (h *aliasFunctionHost) B(_ *evm.Call, fn *evm.NamedFunctionOf[func([]int)]) { h.b = fn }

func TestAliasInsideFunctionHandleSignature(t *testing.T) {
	src := fstest.MapFS{"api.go": {Data: []byte(`package h
 import "github.com/andreyvit/evm"
 type L = []int
 func A(fn *evm.NamedFunctionOf[func([]int)])
 func B(fn *evm.NamedFunctionOf[func(L)])
 `)}}
	s, err := evmbind.New("h", src, evmbind.Options{Host: reflect.TypeFor[*aliasFunctionHost]()})
	if err != nil {
		t.Fatal(err)
	}
	m, err := evmcompiler.Compile("use.go", `import "h";func F(values h.L){};func Run(){h.A(F);h.B(F)}`, s.Package())
	if err != nil {
		t.Fatal(err)
	}
	host := &aliasFunctionHost{}
	p, err := evm.Link(m, s.Bind(host))
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(p).Run(context.Background(), "Run", nil, nil); err != nil {
		t.Fatal(err)
	}
	if host.a == nil || host.b == nil || host.a.Signature.Params[0] != host.b.Signature.Params[0] {
		t.Fatal("alias split function signatures")
	}
}
