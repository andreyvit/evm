package evmbind_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

type GenericBox[T any] struct{ Value T }

func (b *GenericBox[T]) Get() T { return b.Value }

func TestNativeGenericStructInstances(t *testing.T) {
	source := fstest.MapFS{"types.go": {Data: []byte(`package native
 type GenericBox[T any] struct{Value T}
 func(b *GenericBox[T])Get()T{return b.Value}
 `)}}
	schema, err := evmbind.NewTypes("host", source, []string{"GenericBox"}, evmbind.Options{
		Methods: map[string][]string{"GenericBox": {"Get"}},
		Instances: []evmbind.Instance{
			{Type: "GenericBox", Native: reflect.TypeFor[GenericBox[int]](), Args: []reflect.Type{reflect.TypeFor[int]()}},
			{Type: "GenericBox", Native: reflect.TypeFor[GenericBox[string]](), Args: []reflect.Type{reflect.TypeFor[string]()}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	consumerSource := fstest.MapFS{"api.go": {Data: []byte(`package consumer
 import "example/native"
 func Echo(p *native.GenericBox[int])*native.GenericBox[int]
 `)}}
	if _, err := evmbind.GenerateWithImports("consumer", "example/consumer", "consumerbindings", consumerSource, map[string]*evmbind.Schema{"example/native": schema}); err != nil {
		t.Fatal("generate consumer of imported native generic", err)
	}
	consumer, err := evmbind.NewTypes("consumer", consumerSource, nil, evmbind.Options{Imports: map[string]*evmbind.Schema{"example/native": schema}, Functions: map[string]any{"Echo": func(p *GenericBox[int]) *GenericBox[int] { return p }}})
	if err != nil {
		t.Fatal(err)
	}
	dep := consumer.Bind(nil)
	host := schema.Bind(nil)
	module, err := evmcompiler.Compile("generic.go", `import("host";"consumer")
 func Run(n *host.GenericBox[int],s *host.GenericBox[string])bool{
 n.Value=42
 var r interface{Get()string}=s
 return n.Get()==42 && r.Get()=="native" && consumer.Echo(n)==n
 }`, host, dep)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, host, dep)
	if err != nil {
		t.Fatal(err)
	}
	n := &GenericBox[int]{}
	s := &GenericBox[string]{Value: "native"}
	nv, err := schema.NativePointer(n)
	if err != nil {
		t.Fatal(err)
	}
	sv, err := schema.NativePointer(s)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(program).Run(context.Background(), "Run", []values.Value{nv, sv}, out[:]); err != nil {
		t.Fatal(err)
	}
	if out[0].R != 1 || n.Value != 42 {
		t.Fatal(out, n)
	}
}

func TestNativeGenericStructInvalidInstances(t *testing.T) {
	source := fstest.MapFS{"types.go": {Data: []byte(`package native;type GenericBox[T any] struct{Value T}`)}}
	good := evmbind.Instance{Type: "GenericBox", Native: reflect.TypeFor[GenericBox[int]](), Args: []reflect.Type{reflect.TypeFor[int]()}}
	for _, tc := range []struct {
		name      string
		instances []evmbind.Instance
		want      string
	}{
		{"wrong args", []evmbind.Instance{{Type: "GenericBox", Native: good.Native, Args: []reflect.Type{reflect.TypeFor[string]()}}}, "does not match"},
		{"duplicate", []evmbind.Instance{good, good}, "duplicate native instance"},
		{"missing template", []evmbind.Instance{{Type: "Missing", Native: good.Native, Args: good.Args}}, "not declared"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := evmbind.NewTypes("host", source, []string{"GenericBox"}, evmbind.Options{Instances: tc.instances})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
	badSource := fstest.MapFS{"types.go": {Data: []byte(`package native;type GenericBox[T any] struct{Value string}`)}}
	_, err := evmbind.NewTypes("host", badSource, []string{"GenericBox"}, evmbind.Options{Instances: []evmbind.Instance{good}})
	if err == nil || !strings.Contains(err.Error(), "field mismatch") {
		t.Fatal(err)
	}
	_, err = evmbind.NewTypes("host", source, []string{"GenericBox"}, evmbind.Options{Instances: []evmbind.Instance{good}, Mode: evmbind.StaticOnly})
	if err == nil || !strings.Contains(err.Error(), "StaticOnly") {
		t.Fatal(err)
	}
	_, err = evmbind.GenerateTypesWithOptions("host", good.Native.PkgPath(), "bindings", source, []string{"GenericBox"}, evmbind.Options{Instances: []evmbind.Instance{good}})
	if err == nil || !strings.Contains(err.Error(), "generated bindings") {
		t.Fatal(err)
	}
}

type GenericPrivate[T any] struct {
	Value  T
	hidden string
}

func TestNativeGenericStructOmittedField(t *testing.T) {
	source := fstest.MapFS{"types.go": {Data: []byte(`package native;type GenericPrivate[T any] struct{Value T;hidden string}`)}}
	schema, err := evmbind.NewTypes("host", source, nil, evmbind.Options{
		OmitFields: map[string][]string{"GenericPrivate": {"hidden"}},
		Instances:  []evmbind.Instance{{Type: "GenericPrivate", Native: reflect.TypeFor[GenericPrivate[int]](), Args: []reflect.Type{reflect.TypeFor[int]()}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	host := schema.Bind(nil)
	m, err := evmcompiler.Compile("private.go", `import "host";func Run(p *host.GenericPrivate[int]) {copy:=*p;copy.Value=42;*p=copy}`, host)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, host)
	if err != nil {
		t.Fatal(err)
	}
	native := &GenericPrivate[int]{hidden: "retained"}
	arg, err := schema.NativePointer(native)
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(p).Run(context.Background(), "Run", []values.Value{arg}, nil); err != nil {
		t.Fatal(err)
	}
	if native.Value != 42 || native.hidden != "retained" {
		t.Fatal(native)
	}
}

type GenericArgument struct{ N int }

func TestNativeGenericStructArguments(t *testing.T) {
	source := fstest.MapFS{"types.go": {Data: []byte(`package native
 type GenericArgument struct{N int}
 type GenericBox[T any] struct{Value T}
 func Echo(v *GenericBox[byte])*GenericBox[uint8]
 `)}}
	instances := []evmbind.Instance{
		{Type: "GenericBox", Native: reflect.TypeFor[GenericBox[GenericBox[int]]](), Args: []reflect.Type{reflect.TypeFor[GenericBox[int]]()}},
		{Type: "GenericBox", Native: reflect.TypeFor[GenericBox[GenericArgument]](), Args: []reflect.Type{reflect.TypeFor[GenericArgument]()}},
		{Type: "GenericBox", Native: reflect.TypeFor[GenericBox[int]](), Args: []reflect.Type{reflect.TypeFor[int]()}},
		{Type: "GenericBox", Native: reflect.TypeFor[GenericBox[uint8]](), Args: []reflect.Type{reflect.TypeFor[uint8]()}},
		{Type: "GenericBox", Native: reflect.TypeFor[GenericBox[time.Time]](), Args: []reflect.Type{reflect.TypeFor[time.Time]()}},
	}
	schema, err := evmbind.NewTypes("host", source, []string{"GenericBox"}, evmbind.Options{Instances: instances, Functions: map[string]any{"Echo": func(v *GenericBox[uint8]) *GenericBox[uint8] { return v }}})
	if err != nil {
		t.Fatal(err)
	}
	host := schema.Bind(nil)
	m, err := evmcompiler.Compile("args.go", `import("host";"time")
 func Run()bool{
 p:=&host.GenericBox[byte]{Value:7}
 nested:=host.GenericBox[host.GenericBox[int]]{Value:host.GenericBox[int]{Value:42}}
 local:=host.GenericBox[host.GenericArgument]{Value:host.GenericArgument{N:9}}
 stamp:=host.GenericBox[time.Time]{}
 return host.Echo(p)==p && nested.Value.Value==42 && local.Value.N==9 && stamp.Value.IsZero()
 }`, host)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(m, host)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if out[0].R != 1 {
		t.Fatal(out)
	}
}

func TestNativeGenericStructImportedArgument(t *testing.T) {
	dep, err := evmbind.NewTypes("args", fstest.MapFS{"args.go": {Data: []byte(`package original;type GenericArgument struct{N int}`)}}, nil, evmbind.Options{Types: []reflect.Type{reflect.TypeFor[GenericArgument]()}})
	if err != nil {
		t.Fatal(err)
	}
	schema, err := evmbind.NewTypes("host", fstest.MapFS{"box.go": {Data: []byte(`package native;type GenericBox[T any] struct{Value T}`)}}, []string{"GenericBox"}, evmbind.Options{
		Imports:   map[string]*evmbind.Schema{"example/original": dep},
		Instances: []evmbind.Instance{{Type: "GenericBox", Native: reflect.TypeFor[GenericBox[GenericArgument]](), Args: []reflect.Type{reflect.TypeFor[GenericArgument]()}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	host := schema.Bind(nil)
	m, err := evmcompiler.Compile("imported.go", `import("host";"args");func Run(p *host.GenericBox[args.GenericArgument])int{return p.Value.N}`, host, dep.Package())
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(m, host, dep.Package())
	if err != nil {
		t.Fatal(err)
	}
	native := &GenericBox[GenericArgument]{Value: GenericArgument{N: 42}}
	arg, err := schema.NativePointer(native)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(program).Run(context.Background(), "Run", []values.Value{arg}, out[:]); err != nil {
		t.Fatal(err)
	}
	if out[0].AsInt() != 42 {
		t.Fatal(out)
	}
}

type GenericEmbedded[T comparable] struct{ Hidden bool }

func (e *GenericEmbedded[T]) Hide() { e.Hidden = true }

type GenericWidget struct {
	GenericEmbedded[GenericWidget]
	Value int
}

func TestNativeGenericEmbeddedSelfReference(t *testing.T) {
	source := fstest.MapFS{"types.go": {Data: []byte(`package native
 type GenericEmbedded[T comparable]struct{Hidden bool}
 func(e *GenericEmbedded[T])Hide(){e.Hidden=true}
 type GenericWidget struct{GenericEmbedded[GenericWidget];Value int}
 `)}}
	s, err := evmbind.NewTypes("native", source, []string{"GenericWidget"}, evmbind.Options{
		Types:     []reflect.Type{reflect.TypeFor[GenericWidget]()},
		Methods:   map[string][]string{"GenericEmbedded": {"Hide"}},
		Instances: []evmbind.Instance{{Type: "GenericEmbedded", Native: reflect.TypeFor[GenericEmbedded[GenericWidget]](), Args: []reflect.Type{reflect.TypeFor[GenericWidget]()}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg := s.Bind(nil)
	m, err := evmcompiler.Compile("widget.go", `import "native";func Run(){w:=&native.GenericWidget{Value:3};w.Hide();if !w.GenericEmbedded.Hidden || w.Value!=3{panic("embedded native layout")}}`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, pkg)
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(p).Run(context.Background(), "Run", nil, nil); err != nil {
		t.Fatal(err)
	}
}

type GenericSetter[T any] struct{ Set func(T) error }

func TestNativeGenericFunctionFieldErrorIdentity(t *testing.T) {
	source := fstest.MapFS{"setter.go": {Data: []byte(`package native;type GenericSetter[T any]struct{Set func(T)error}`)}}
	s, err := evmbind.NewTypes("native", source, nil, evmbind.Options{Instances: []evmbind.Instance{{Type: "GenericSetter", Native: reflect.TypeFor[GenericSetter[string]](), Args: []reflect.Type{reflect.TypeFor[string]()}}}})
	if err != nil {
		t.Fatal(err)
	}
	pkg := s.Bind(nil)
	m, err := evmcompiler.Compile("setter.go", `import "native";func Run(s *native.GenericSetter[string]){var set func(string)error=s.Set;if set("updated")!=nil{panic("setter failed")}}`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, pkg)
	if err != nil {
		t.Fatal(err)
	}
	value := "before"
	arg, err := s.NativePointer(&GenericSetter[string]{Set: func(v string) error { value = v; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(p).Run(context.Background(), "Run", []values.Value{arg}, nil); err != nil {
		t.Fatal(err)
	}
	if value != "updated" {
		t.Fatal(value)
	}
}

type GenericWidgetOf[T comparable] struct {
	GenericEmbedded[*GenericWidgetOf[T]]
	Value T
}

func TestNativeGenericEmbeddedInstance(t *testing.T) {
	source := fstest.MapFS{"types.go": {Data: []byte(`package native
 type GenericEmbedded[T comparable]struct{Hidden bool}
 type GenericWidgetOf[T comparable]struct{GenericEmbedded[*GenericWidgetOf[T]];Value T}
 `)}}
	s, err := evmbind.NewTypes("native", source, []string{"GenericWidgetOf"}, evmbind.Options{Instances: []evmbind.Instance{
		{Type: "GenericEmbedded", Native: reflect.TypeFor[GenericEmbedded[*GenericWidgetOf[string]]](), Args: []reflect.Type{reflect.TypeFor[*GenericWidgetOf[string]]()}},
		{Type: "GenericWidgetOf", Native: reflect.TypeFor[GenericWidgetOf[string]](), Args: []reflect.Type{reflect.TypeFor[string]()}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	pkg := s.Bind(nil)
	m, err := evmcompiler.Compile("widget.go", `import "native";func Run(){w:=&native.GenericWidgetOf[string]{Value:"text"};w.Hidden=true;if !w.Hidden || w.Value!="text"{panic("generic embedded layout")}}`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, pkg)
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(p).Run(context.Background(), "Run", nil, nil); err != nil {
		t.Fatal(err)
	}
}

type GenericLabelHolder struct{ Labels map[string]string }
type GenericConsumerLabels struct{ Values map[string]string }

func TestNativeGenericImportsDoNotOwnStructuralTypes(t *testing.T) {
	host, err := evmbind.NewTypes("host", fstest.MapFS{"host.go": {Data: []byte(`package native
 type GenericBox[T any] struct {Value T}
 type GenericLabelHolder struct {Labels map[string]string}
 `)}}, []string{"GenericBox", "GenericLabelHolder"}, evmbind.Options{
		Types:     []reflect.Type{reflect.TypeFor[GenericLabelHolder]()},
		Instances: []evmbind.Instance{{Type: "GenericBox", Native: reflect.TypeFor[GenericBox[int]](), Args: []reflect.Type{reflect.TypeFor[int]()}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := evmbind.NewTypes("consumer", fstest.MapFS{"consumer.go": {Data: []byte(`package consumer
 import host "example/host"
 type GenericConsumerLabels struct {Values map[string]string}
 func Accept(p *host.GenericBox[int])int
 `)}}, []string{"GenericConsumerLabels"}, evmbind.Options{
		Types:         []reflect.Type{reflect.TypeFor[GenericConsumerLabels]()},
		NativePackage: reflect.TypeFor[GenericConsumerLabels]().PkgPath(),
		Imports:       map[string]*evmbind.Schema{"example/host": host},
		Functions:     map[string]any{"Accept": func(p *GenericBox[int]) int { return p.Value }},
	})
	if err != nil {
		t.Fatal(err)
	}
	packages := []evm.Package{host.Bind(nil), consumer.Bind(nil)}
	// Either schema may be the first declaration of a shared map type.
	for _, d := range packages[0].Types {
		if d.Name == "GenericLabelHolder" {
			packages[1].Types = append(packages[1].Types, evm.TypeDecl{Shape: d.Shape.Fields[0].Shape})
		}
	}
	packages[0], packages[1] = packages[1], packages[0]
	module, err := evmcompiler.Compile("test.go", `import("host";"consumer")
 func Run(){h:=&host.GenericLabelHolder{Labels:map[string]string{"a":"b"}};if h.Labels["a"]!="b" || consumer.Accept(&host.GenericBox[int]{Value:42})!=42 {panic("bad native handoff")}}`, packages...)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, packages...)
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(program).Run(context.Background(), "Run", nil, nil); err != nil {
		t.Fatal(err)
	}
}

type GenericImportAmount int64

func (a GenericImportAmount) Mark() int64 { return int64(a) }

type GenericAmountControl struct {
	Binding *GenericBox[GenericImportAmount]
}

func TestNativeGenericScalarArgumentWithConsumerInSamePackage(t *testing.T) {
	model, err := evmbind.NewTypes("model", fstest.MapFS{"model.go": {Data: []byte(`package native; type GenericImportAmount int64;func(GenericImportAmount)Mark()int64`)}}, []string{"GenericImportAmount"}, evmbind.Options{Methods: map[string][]string{"GenericImportAmount": {"Mark"}}, Types: []reflect.Type{reflect.TypeFor[GenericImportAmount]()}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := evmbind.NewTypes("host", fstest.MapFS{"host.go": {Data: []byte(`package native; import model "example/model"; type GenericBox[T interface{Mark()int64}] struct{Value T}; type GenericAmountControl struct{Binding *GenericBox[model.GenericImportAmount]}`)}}, []string{"GenericBox", "GenericAmountControl"}, evmbind.Options{
		Types:     []reflect.Type{reflect.TypeFor[GenericAmountControl]()},
		Imports:   map[string]*evmbind.Schema{"example/model": model, reflect.TypeFor[GenericImportAmount]().PkgPath(): model},
		Instances: []evmbind.Instance{{Type: "GenericBox", Native: reflect.TypeFor[GenericBox[GenericImportAmount]](), Args: []reflect.Type{reflect.TypeFor[GenericImportAmount]()}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	helper, err := evmbind.NewTypes("helper", fstest.MapFS{"helper.go": {Data: []byte(`package helper
 import host "example/host"
 import model "example/model"
 func Accept(p *host.GenericBox[model.GenericImportAmount])*host.GenericAmountControl
 `)}}, []string{}, evmbind.Options{NativePackage: "example/helper", Imports: map[string]*evmbind.Schema{"example/host": host, "example/model": model}, Functions: map[string]any{"Accept": func(p *GenericBox[GenericImportAmount]) *GenericAmountControl {
		return &GenericAmountControl{Binding: p}
	}}})
	if err != nil {
		t.Fatal(err)
	}
	m := model.Bind(nil)
	m.Functions = append(m.Functions, helper.Bind(nil).Functions...)
	packages := []evm.Package{host.Bind(nil), m}
	module, err := evmcompiler.Compile("test.go", `import("host";"model")
 func Run(){if model.Accept(&host.GenericBox[model.GenericImportAmount]{Value:42}).Binding.Value.Mark()!=42{panic("bad amount")}}`, packages...)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, packages...)
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(program).Run(context.Background(), "Run", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestNativeGenericNestedScalarArgument(t *testing.T) {
	for _, tc := range []struct {
		name, arg, expression string
		native, parameter     reflect.Type
		accept                any
	}{
		{"slice", "[]model.GenericImportAmount", "[]model.GenericImportAmount{42}", reflect.TypeFor[GenericBox[[]GenericImportAmount]](), reflect.TypeFor[[]GenericImportAmount](), func(p *GenericBox[[]GenericImportAmount]) int64 { return int64(p.Value[0]) }},
		{"pointer", "*model.GenericImportAmount", "&amount", reflect.TypeFor[GenericBox[*GenericImportAmount]](), reflect.TypeFor[*GenericImportAmount](), func(p *GenericBox[*GenericImportAmount]) int64 { return int64(*p.Value) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model, err := evmbind.NewTypes("model", fstest.MapFS{"model.go": {Data: []byte(`package native; type GenericImportAmount int64`)}}, []string{"GenericImportAmount"}, evmbind.Options{Types: []reflect.Type{reflect.TypeFor[GenericImportAmount]()}})
			if err != nil {
				t.Fatal(err)
			}
			host, err := evmbind.NewTypes("host", fstest.MapFS{"host.go": {Data: []byte(`package native; type GenericBox[T any] struct{Value T}`)}}, []string{"GenericBox"}, evmbind.Options{Imports: map[string]*evmbind.Schema{reflect.TypeFor[GenericImportAmount]().PkgPath(): model}, Instances: []evmbind.Instance{{Type: "GenericBox", Native: tc.native, Args: []reflect.Type{tc.parameter}}}})
			if err != nil {
				t.Fatal(err)
			}
			helper, err := evmbind.NewTypes("helper", fstest.MapFS{"helper.go": {Data: []byte(`package helper;import host "example/host";import model "example/model";func Accept(p *host.GenericBox[` + tc.arg + `])int64`)}}, []string{}, evmbind.Options{NativePackage: "example/helper", Imports: map[string]*evmbind.Schema{"example/host": host, "example/model": model}, Functions: map[string]any{"Accept": tc.accept}})
			if err != nil {
				t.Fatal(err)
			}
			m := model.Bind(nil)
			m.Functions = append(m.Functions, helper.Bind(nil).Functions...)
			packages := []evm.Package{host.Bind(nil), m}
			module, err := evmcompiler.Compile("test.go", `import("host";"model");func Run(){amount:=model.GenericImportAmount(42);_=amount;if model.Accept(&host.GenericBox[`+tc.arg+`]{Value:`+tc.expression+`})!=42{panic("bad amount")}}`, packages...)
			if err != nil {
				t.Fatal(err)
			}
			program, err := evm.Link(module, packages...)
			if err != nil {
				t.Fatal(err)
			}
			if err := evm.New(program).Run(context.Background(), "Run", nil, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}
