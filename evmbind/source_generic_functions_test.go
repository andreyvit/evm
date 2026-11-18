package evmbind_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/interfacedep"
	"github.com/andreyvit/evm/evmcompiler"
)

type GenericBinding[T any] struct{ Pointer *T }

func genericVar[T any](p *T) *GenericBinding[T] { return &GenericBinding[T]{Pointer: p} }

func TestNativeGenericPackageFunctions(t *testing.T) {
	source := fstest.MapFS{"api.go": {Data: []byte(`package forms
 type GenericBinding[T any]struct{Pointer *T}
 func Var[T any](p *T)*GenericBinding[T]{panic("native")}
 `)}}
	s, err := evmbind.New("forms", source, evmbind.Options{
		Selective: true,
		Instances: []evmbind.Instance{
			{Type: "GenericBinding", Native: reflect.TypeFor[GenericBinding[string]](), Args: []reflect.Type{reflect.TypeFor[string]()}},
			{Type: "GenericBinding", Native: reflect.TypeFor[GenericBinding[int]](), Args: []reflect.Type{reflect.TypeFor[int]()}},
		},
		GenericFunctions: map[string][]evmbind.FunctionInstance{"Var": {
			{Args: []reflect.Type{reflect.TypeFor[string]()}, Function: genericVar[string]},
			{Args: []reflect.Type{reflect.TypeFor[int]()}, Function: genericVar[int]},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg := s.Bind(nil)
	m, err := evmcompiler.Compile("forms.go", `import "forms"
 func Run(){
  text:="first";binding:=forms.Var(&text)
  *binding.Pointer="changed";if text!="changed"{panic("native pointer copied")}
  n:=3;f:=forms.Var[int];ref:=f(&n)
  *ref.Pointer=7;if n!=7{panic("generic function value")}
 }`, pkg)
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
	_, err = evmcompiler.Compile("unsupported.go", `import "forms";func Run(){value:=true;forms.Var(&value)}`, pkg)
	if err == nil || !strings.Contains(err.Error(), "native instance") {
		t.Fatalf("unsupported native instance: %v", err)
	}
}

func genericFirst[T comparable](a, b T) T { return a }

func TestNativeGenericFunctionValidation(t *testing.T) {
	source := fstest.MapFS{"api.go": {Data: []byte(`package host
 func First[T comparable](a,b T)T{panic("native")}
 func Plain(a int)int{panic("native")}
 `)}}
	good := evmbind.FunctionInstance{Args: []reflect.Type{reflect.TypeFor[int]()}, Function: genericFirst[int]}
	for _, tc := range []struct {
		name      string
		key       string
		instances []evmbind.FunctionInstance
		want      string
	}{
		{"empty", "First", nil, "requires native instances"},
		{"duplicate", "First", []evmbind.FunctionInstance{good, good}, "duplicate native instance"},
		{"nongeneric", "Plain", []evmbind.FunctionInstance{good}, "not a generic function"},
		{"missing", "Missing", []evmbind.FunctionInstance{good}, "not declared"},
		{"nil function", "First", []evmbind.FunctionInstance{{Args: good.Args}}, "invalid native function"},
		{"wrong signature", "First", []evmbind.FunctionInstance{{Args: good.Args, Function: genericFirst[string]}}, "native argument mismatch"},
		{"no arguments", "First", []evmbind.FunctionInstance{{Function: genericFirst[int]}}, "instantiate First"},
		{"constraint", "First", []evmbind.FunctionInstance{{Args: []reflect.Type{reflect.TypeFor[[]int]()}, Function: func(a, b []int) []int { return a }}}, "comparable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := evmbind.New("host", source, evmbind.Options{Only: []string{}, GenericFunctions: map[string][]evmbind.FunctionInstance{tc.key: tc.instances}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
	_, err := evmbind.New("host", source, evmbind.Options{Only: []string{}, Mode: evmbind.StaticOnly, GenericFunctions: map[string][]evmbind.FunctionInstance{"First": {good}}})
	if err == nil || !strings.Contains(err.Error(), "StaticOnly") {
		t.Fatalf("generic StaticOnly: %v", err)
	}
}

type GenericChild interface{ Touch() }
type GenericChildAdder interface{ AddChild(...GenericChild) }
type GenericControl struct{ Count int }

func (c *GenericControl) Touch() { c.Count++ }

type GenericGroup struct{ Children []GenericChild }

func (g *GenericGroup) AddChild(children ...GenericChild) {
	g.Children = append(g.Children, children...)
}
func genericAdd[T GenericChild](group GenericChildAdder, child T) T {
	group.AddChild(child)
	return child
}

func TestNativeGenericConstrainedFunction(t *testing.T) {
	source := fstest.MapFS{"api.go": {Data: []byte(`package forms
 type GenericChild interface{Touch()}
 type GenericChildAdder interface{AddChild(...GenericChild)}
 type GenericControl struct{Count int}
 func(c *GenericControl)Touch(){}
 type GenericGroup struct{Children []GenericChild}
 func(g *GenericGroup)AddChild(children ...GenericChild){}
 func Add[T GenericChild](group GenericChildAdder,child T)T{panic("native")}
 `)}}
	s, err := evmbind.New("forms", source, evmbind.Options{
		Only:             []string{"GenericGroup"},
		Types:            []reflect.Type{reflect.TypeFor[GenericGroup](), reflect.TypeFor[GenericControl]()},
		Methods:          map[string][]string{"GenericGroup": {"AddChild"}, "GenericControl": {"Touch"}},
		GenericFunctions: map[string][]evmbind.FunctionInstance{"Add": {{Args: []reflect.Type{reflect.TypeFor[*GenericControl]()}, Function: genericAdd[*GenericControl]}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg := s.Bind(nil)
	m, err := evmcompiler.Compile("forms.go", `import "forms"
 func Run(){
  group:=&forms.GenericGroup{}
  child:=&forms.GenericControl{}
  if forms.Add(group,child)!=child || group.Children[0]!=child{panic("identity")}
  group.Children[0].Touch();if child.Count!=1{panic("native method")}
 }`, pkg)
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

func TestNativeGenericFunctionImportedArgument(t *testing.T) {
	dep, err := evmbind.New("domain", interfacedep.Source, evmbind.Options{Only: []string{"NamedValue"}, Types: []reflect.Type{reflect.TypeFor[interfacedep.NamedValue]()}})
	if err != nil {
		t.Fatal(err)
	}
	source := fstest.MapFS{"api.go": {Data: []byte(`package host;func First[T comparable](a,b T)T{panic("native")}`)}}
	s, err := evmbind.New("host", source, evmbind.Options{
		Only:             []string{},
		Imports:          map[string]*evmbind.Schema{"github.com/andreyvit/evm/evmbind/internal/interfacedep": dep},
		GenericFunctions: map[string][]evmbind.FunctionInstance{"First": {{Args: []reflect.Type{reflect.TypeFor[*interfacedep.NamedValue]()}, Function: genericFirst[*interfacedep.NamedValue]}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	pkgs := []evm.Package{dep.Bind(nil), s.Bind(nil)}
	m, err := evmcompiler.Compile("imported.go", `import("host";"domain");func Run(){a:=&domain.NamedValue{Text:"a"};if host.First(a,nil)!=a{panic("imported type identity")}}`, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(p).Run(context.Background(), "Run", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestNativeGenericNamedConstraints(t *testing.T) {
	dependency, err := evmbind.New("constraints", fstest.MapFS{"api.go": {Data: []byte(`package cmp;type Ordered interface{~int|~float64}`)}}, evmbind.Options{Selective: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, constraint := range []string{"Number", "cmp.Ordered"} {
		source := fstest.MapFS{"api.go": {Data: []byte(`package host
 import "cmp"
 type Number interface{~int|~float64}
 type T struct{Unsupported chan int}
 func First[T ` + constraint + `](a,b T)T{panic("native")}
 `)}}
		s, err := evmbind.New("host", source, evmbind.Options{Only: []string{}, Imports: map[string]*evmbind.Schema{"cmp": dependency}, GenericFunctions: map[string][]evmbind.FunctionInstance{"First": {{Args: []reflect.Type{reflect.TypeFor[int]()}, Function: genericFirst[int]}}}})
		if err != nil {
			t.Fatalf("%s: %v", constraint, err)
		}
		pkg := s.Bind(nil)
		m, err := evmcompiler.Compile("constraints.go", `import "host";func Run(){if host.First(1,2)!=1{panic("constraint")}}`, pkg)
		if err != nil {
			t.Fatalf("%s: %v", constraint, err)
		}
		p, err := evm.Link(m, pkg)
		if err != nil {
			t.Fatal(err)
		}
		if err := evm.New(p).Run(context.Background(), "Run", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
}
