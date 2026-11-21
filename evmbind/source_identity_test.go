package evmbind_test

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
)

type SourceLeft struct{ Name string }
type SourceRight struct{ Count int }
type SourceBoth struct {
	Left  *SourceLeft
	Right *SourceRight
}

type SourceFinal struct{ Both *SourceBoth }

func TestSameNamedNativePackages(t *testing.T) {
	left, err := evmbind.New("model", fstest.MapFS{"left.go": {Data: []byte(`package data;type SourceLeft struct{Name string}`)}}, evmbind.Options{Selective: true, Types: []reflect.Type{reflect.TypeFor[SourceLeft]()}})
	if err != nil {
		t.Fatal(err)
	}
	right, err := evmbind.New("model", fstest.MapFS{"right.go": {Data: []byte(`package data;type SourceRight struct{Count int}`)}}, evmbind.Options{Selective: true, Types: []reflect.Type{reflect.TypeFor[SourceRight]()}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := evmbind.New("host", fstest.MapFS{"host.go": {Data: []byte(`package host
 import(left "example/left";right "example/right")
 type SourceBoth struct{Left *left.SourceLeft;Right *right.SourceRight}
 `)}}, evmbind.Options{Selective: true, Types: []reflect.Type{reflect.TypeFor[SourceBoth]()}, Imports: map[string]*evmbind.Schema{"example/left": left, "example/right": right}})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := evmbind.New("entry", fstest.MapFS{"entry.go": {Data: []byte(`package entry
 import "example/host"
 type SourceFinal struct{Both *host.SourceBoth}
 `)}}, evmbind.Options{Selective: true, Types: []reflect.Type{reflect.TypeFor[SourceFinal]()}, Imports: map[string]*evmbind.Schema{"example/host": host}})
	if err != nil {
		t.Fatal(err)
	}
	model := left.Package()
	model.Types = append(model.Types, right.Package().Types...)
	pkgs := []evm.Package{model, host.Package(), entry.Package()}
	m, err := evmcompiler.Compile("same_name.go", `import("host";"model";"entry")
 func Run()bool{
 l:=&model.SourceLeft{Name:"left"};r:=&model.SourceRight{Count:7}
 both:=&host.SourceBoth{Left:l,Right:r};wrapped:=&entry.SourceFinal{Both:both};wrapped.Both.Left.Name="changed";wrapped.Both.Right.Count++
 return l.Name=="changed" && r.Count==8 && both.Left==l && both.Right==r
 }`, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evmtest.New(p).Run(context.Background(), "Run", nil, out[:]); err != nil || out[0].R != 1 {
		t.Fatal(out, err)
	}
}

type SourceCallbackBox struct{ F *evm.FunctionOf[func()] }
type sourceCallbackHost struct{}

func (*sourceCallbackHost) Echo(_ *evm.VM, box *SourceCallbackBox) *evm.FunctionOf[func()] {
	return box.F
}

func TestNativeFunctionHandleSourceImport(t *testing.T) {
	dep, err := evmbind.New("data", fstest.MapFS{"data.go": {Data: []byte(`package data
 import "github.com/andreyvit/evm"
 type SourceCallbackBox struct{F *evm.FunctionOf[func()]}
 `)}}, evmbind.Options{Selective: true, Types: []reflect.Type{reflect.TypeFor[SourceCallbackBox]()}})
	if err != nil {
		t.Fatal(err)
	}
	source := fstest.MapFS{"host.go": {Data: []byte(`package host
 import("example/data";"github.com/andreyvit/evm")
 func Echo(box *data.SourceCallbackBox)*evm.FunctionOf[func()]{return box.F}
 `)}}
	host, err := evmbind.New("host", source, evmbind.Options{Imports: map[string]*evmbind.Schema{"example/data": dep}, Host: reflect.TypeFor[*sourceCallbackHost]()})
	if err != nil {
		t.Fatal(err)
	}
	pkgs := []evm.Package{dep.Package(), host.Bind(&sourceCallbackHost{})}
	module, err := evmcompiler.Compile("handles.go", `import("data";"host");func Run()bool{return host.Echo(&data.SourceCallbackBox{})==nil}`, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evmtest.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil || out[0].R != 1 {
		t.Fatal(out, err)
	}
}

func TestConcurrentNativeSourceImports(t *testing.T) {
	dep, err := evmbind.New("data", fstest.MapFS{"data.go": {Data: []byte(`package data
 import "github.com/andreyvit/evm"
 type SourceCallbackBox struct{F *evm.FunctionOf[func()]}
 `)}}, evmbind.Options{Selective: true, Types: []reflect.Type{reflect.TypeFor[SourceCallbackBox]()}})
	if err != nil {
		t.Fatal(err)
	}
	for range 8 {
		t.Run("schema", func(t *testing.T) {
			t.Parallel()
			source := fstest.MapFS{"host.go": {Data: []byte(`package host
  import("example/data";"github.com/andreyvit/evm")
  func Echo(box *data.SourceCallbackBox)*evm.FunctionOf[func()]{return box.F}
  `)}}
			_, err := evmbind.New("host", source, evmbind.Options{Imports: map[string]*evmbind.Schema{"example/data": dep}, Host: reflect.TypeFor[*sourceCallbackHost]()})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

type SourceFlag int

func TestTransitiveConstantSourceType(t *testing.T) {
	a, err := evmbind.New("model", fstest.MapFS{"a.go": {Data: []byte(`package a;type SourceFlag int;const Active SourceFlag=3`)}}, evmbind.Options{Selective: true, Types: []reflect.Type{reflect.TypeFor[SourceFlag]()}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := evmbind.New("bridge", fstest.MapFS{"b.go": {Data: []byte(`package b;import "example/a";const Exported=a.Active`)}}, evmbind.Options{Imports: map[string]*evmbind.Schema{"example/a": a}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := evmbind.New("host", fstest.MapFS{"c.go": {Data: []byte(`package c;import "example/b";const C=b.Exported`)}}, evmbind.Options{Imports: map[string]*evmbind.Schema{"example/b": b}})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Package().Types) != 0 || c.Package().Constants[0].Type != a.Type("SourceFlag") {
		t.Fatal("transitive enum became a new local type")
	}
	if _, err := evmcompiler.Compile("constant.go", `import("model";"host");func Run()bool{var flag model.SourceFlag=host.C;return flag==model.Active}`, a.Package(), b.Package(), c.Package()); err != nil {
		t.Fatal(err)
	}
}

func TestAliasedNativeSourceGeneration(t *testing.T) {
	dep, err := evmbind.New("model", fstest.MapFS{"dep.go": {Data: []byte(`package data;type SourceLeft struct{Name string}`)}}, evmbind.Options{Selective: true, Types: []reflect.Type{reflect.TypeFor[SourceLeft]()}})
	if err != nil {
		t.Fatal(err)
	}
	imports := map[string]*evmbind.Schema{"example/left": dep, "aaa/synthetic": dep, reflect.TypeFor[SourceLeft]().PkgPath(): dep}
	source := fstest.MapFS{"host.go": {Data: []byte(`package host;import "example/left";func Echo(x *data.SourceLeft)*data.SourceLeft{return x}`)}}
	var first string
	for range 20 {
		generated, err := evmbind.Generate("host", "example/host", "bindings", source, evmbind.Options{Imports: imports})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(generated), strconv.Quote(reflect.TypeFor[SourceLeft]().PkgPath())) || strings.Contains(string(generated), strconv.Quote("aaa/synthetic")) {
			t.Fatal("generated code imports an alias instead of the native package")
		}
		if first == "" {
			first = string(generated)
		} else if first != string(generated) {
			t.Fatal("native alias changed generated output")
		}
	}
}

type SourceFunc func(int) int
type sourceFuncHost struct{}

func (*sourceFuncHost) Make(*evm.VM) SourceFunc { return func(x int) int { return x + 5 } }

func TestImportedNativeFunctionSource(t *testing.T) {
	dep, err := evmbind.New("data", fstest.MapFS{"data.go": {Data: []byte(`package data;type SourceFunc func(int)int`)}}, evmbind.Options{Selective: true, Types: []reflect.Type{reflect.TypeFor[SourceFunc]()}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := evmbind.New("host", fstest.MapFS{"host.go": {Data: []byte(`package host;import "example/data";func Make()data.SourceFunc{panic("native")}`)}}, evmbind.Options{Imports: map[string]*evmbind.Schema{"example/data": dep}, Host: reflect.TypeFor[*sourceFuncHost]()})
	if err != nil {
		t.Fatal(err)
	}
	pkgs := []evm.Package{dep.Package(), host.Bind(&sourceFuncHost{})}
	m, err := evmcompiler.Compile("function.go", `import "host";func Run()int{return host.Make()(3)}`, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evmtest.New(p).Run(context.Background(), "Run", nil, out[:]); err != nil || out[0].R != 8 {
		t.Fatal(out, err)
	}
}
