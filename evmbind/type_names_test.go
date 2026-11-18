package evmbind

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

func TestNativeTypeExportNames(t *testing.T) {
	service := &selectedService{count: 7}
	data := &EntryData{Count: 9}
	source := fstest.MapFS{"api.go": {Data: []byte(`package api
 type SelectedService interface{Read()int;Add(int);Hidden()chan int}
 type EntryData struct{Count int}
 func Service()SelectedService{panic("native")}
 func Data()*EntryData{panic("native")}
 `)}}
	options := Options{
		NativePackage:   "native/api",
		Types:           []reflect.Type{reflect.TypeFor[SelectedService](), reflect.TypeFor[EntryData]()},
		Methods:         map[string][]string{"SelectedService": {"Read", "Add"}},
		TypeNames:       map[string]string{"SelectedService": "RequestContext", "EntryData": "Data"},
		Implementations: []Implementation{Pointer[selectedService]()},
		Functions:       map[string]any{"Service": func() SelectedService { return service }, "Data": func() *EntryData { return data }},
	}
	// A type and function cannot share the same exported name.
	if _, err := NewTypes("api", source, nil, options); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("export collision: %v", err)
	}
	options.TypeNames["EntryData"] = "ViewData"
	schema, err := NewTypes("api", source, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := NewTypes("consumer", fstest.MapFS{"consumer.go": {Data: []byte(`package consumer
 import "native/api"
 func Roundtrip(v api.SelectedService)api.SelectedService{panic("native")}
 `)}}, nil, Options{Imports: map[string]*Schema{"native/api": schema}, Functions: map[string]any{"Roundtrip": func(v SelectedService) SelectedService {
		if v != service {
			panic("native pointer changed")
		}
		return v
	}}})
	if err != nil {
		t.Fatal(err)
	}
	module, err := evmcompiler.Compile("run.go", `import("api";"consumer")
 func Run()int {var c api.RequestContext=api.Service(); var v any=c;c=consumer.Roundtrip(v.(api.RequestContext));c.Add(3);d:=api.Data();var p *api.ViewData=d;p.Count++;return c.Read()+d.Count}
 `, schema.Package(), consumer.Package())
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(module, schema.Bind(nil), consumer.Bind(nil))
	if err != nil {
		t.Fatal(err)
	}
	result := make([]values.Value, 1)
	if err := evm.New(p).Run(context.Background(), "Run", nil, result); err != nil {
		t.Fatal(err)
	}
	if service.count != 10 || data.Count != 10 || result[0].AsInt() != 20 {
		t.Fatal("native mutation or dispatch changed")
	}
	if _, err := evmcompiler.Compile("old.go", `import "api";func Run(v api.SelectedService){}`, schema.Package()); err == nil {
		t.Fatal("old guest name retained")
	}
	renamedFingerprint := schema.src.fingerprint
	options.TypeNames = nil
	original, err := NewTypes("api", source, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	if original.src.fingerprint == renamedFingerprint {
		t.Fatal("renaming did not invalidate generated bindings")
	}
}

func TestNativeTypeExportNameValidation(t *testing.T) {
	source := fstest.MapFS{"types.go": {Data: []byte(`package api;type EntryData struct{Count int}`)}}
	for _, names := range []map[string]string{{"Missing": "Other"}, {"EntryData": "private"}, {"EntryData": "X.Y"}, {"EntryData": ""}} {
		if _, err := NewTypes("api", source, nil, Options{Types: []reflect.Type{reflect.TypeFor[EntryData]()}, TypeNames: names}); err == nil {
			t.Fatalf("invalid mapping accepted: %v", names)
		}
	}
}

func TestSamePackageAliasOfRenamedType(t *testing.T) {
	source := fstest.MapFS{"types.go": {Data: []byte(`package native;type EntryData struct{Count int}`)}}
	owner, err := NewTypes("model", source, nil, Options{Types: []reflect.Type{reflect.TypeFor[EntryData]()}, TypeNames: map[string]string{"EntryData": "Data"}})
	if err != nil {
		t.Fatal(err)
	}
	alias, err := NewTypes("model", fstest.MapFS{"alias.go": {Data: []byte(`package other;import "native";type EntryData=native.EntryData;type Data=native.EntryData`)}}, nil, Options{Imports: map[string]*Schema{"native": owner}})
	if err != nil {
		t.Fatal(err)
	}
	pkg := owner.Bind(nil)
	pkg.Aliases = append(pkg.Aliases, alias.Package().Aliases...)
	if _, err := evmcompiler.Compile("alias.go", `import "model";func Run(p *model.EntryData)*model.Data{return p}`, pkg); err != nil {
		t.Fatal(err)
	}
}

type selectedOtherService struct{ selectedService }

func TestSamePackageOpaqueImplementations(t *testing.T) {
	source := fstest.MapFS{"api.go": {Data: []byte(`package api;type SelectedService interface{Read()int}`)}}
	makeSchema := func(name string, impl Implementation) *Schema {
		s, err := NewTypes("model", source, nil, Options{Types: []reflect.Type{reflect.TypeFor[SelectedService]()}, TypeNames: map[string]string{"SelectedService": name}, Implementations: []Implementation{impl}})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	a := makeSchema("First", Pointer[selectedService]()).Bind(nil)
	b := makeSchema("Second", Pointer[selectedOtherService]()).Bind(nil)
	a.Types = append(a.Types, b.Types...)
	a.Methods = append(a.Methods, b.Methods...)
	a.EntryTypes = append(a.EntryTypes, b.EntryTypes...)
	if _, err := evmcompiler.Compile("merged.go", `import "model";func Run(a model.First,b model.Second)int{return a.Read()+b.Read()}`, a); err != nil {
		t.Fatal(err)
	}
}

type ExportBox[T any] struct {
	Value   T
	Context SelectedService
}

func TestRenamedTypeInGenericSourceRejected(t *testing.T) {
	_, err := NewTypes("api", fstest.MapFS{"api.go": {Data: []byte(`package api
 type SelectedService interface{Read()int}
 type ExportBox[T any]struct{Value T;Context SelectedService}
 `)}}, nil, Options{Types: []reflect.Type{reflect.TypeFor[SelectedService]()}, TypeNames: map[string]string{"SelectedService": "RequestContext"}, Instances: []Instance{{Type: "ExportBox", Native: reflect.TypeFor[ExportBox[int]](), Args: []reflect.Type{reflect.TypeFor[int]()}}}})
	if err == nil || !strings.Contains(err.Error(), "generic source references renamed native type") {
		t.Fatalf("unsupported generic reference: %v", err)
	}
}

type ExportEnum int

func TestRenamedTypeIgnoresUnexposedConstants(t *testing.T) {
	schema, err := NewTypes("enums", fstest.MapFS{"enum.go": {Data: []byte(`package enums
 type ExportEnum int
 const (Good ExportEnum=1; Collision=99)
 `)}}, nil, Options{Types: []reflect.Type{reflect.TypeFor[ExportEnum]()}, TypeNames: map[string]string{"ExportEnum": "Collision"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evmcompiler.Compile("enum.go", `import "enums";func Run()enums.Collision{return enums.Good}`, schema.Package()); err != nil {
		t.Fatal(err)
	}
}
