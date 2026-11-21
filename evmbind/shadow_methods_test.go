package evmbind

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
)

type ShadowInner struct{ Value int }

func (*ShadowInner) Label() string { return "inner" }

type ShadowOuter struct{ *ShadowInner }

func (s *ShadowOuter) Value() int  { return s.ShadowInner.Value + 1 }
func (*ShadowOuter) Label() string { return "outer" }

func TestNativeMethodShadowsPromotedSelector(t *testing.T) {
	source := fstest.MapFS{"shadow.go": {Data: []byte(`package native
 type ShadowInner struct{Value int}
 func(*ShadowInner)Label()string{return "inner"}
 type ShadowOuter struct{*ShadowInner}
 func(s *ShadowOuter)Value()int{return s.ShadowInner.Value+1}
 func(*ShadowOuter)Label()string{return "outer"}
 `)}}
	schema, err := New("native", source, Options{Only: []string{"ShadowOuter"}, Types: []reflect.Type{reflect.TypeFor[ShadowOuter]()}, Methods: map[string][]string{"ShadowOuter": {"Value", "Label"}, "ShadowInner": {"Label"}}})
	if err != nil {
		t.Fatal(err)
	}
	module, err := evmcompiler.Compile("run.go", `import "native"
 func Run(s *native.ShadowOuter)int{
  if s.Label()!="outer" || s.ShadowInner.Label()!="inner" {panic("method shadowing")}
  f:=s.Value
  if f()!=5 {panic("field shadowing")}
  s.ShadowInner.Value=10
  return f()
 }`, schema.Package())
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, schema.Bind(nil))
	if err != nil {
		t.Fatal(err)
	}
	host := &ShadowOuter{&ShadowInner{Value: 4}}
	arg, err := schema.NativePointer(host)
	if err != nil {
		t.Fatal(err)
	}
	var result [1]values.Value
	if err := evmtest.New(program).Run(context.Background(), "Run", []values.Value{arg}, result[:]); err != nil {
		t.Fatal(err)
	}
	if result[0].AsInt() != 11 || host.ShadowInner.Value != 10 {
		t.Fatal("native receiver identity lost", result[0], host)
	}
	for _, tc := range []struct {
		name     string
		receiver reflect.Type
	}{
		{"Value", reflect.TypeFor[*ShadowInner]()},
		{"ShadowInner", reflect.TypeFor[*ShadowOuter]()},
	} {
		pkg := schema.Package()
		pkg.Methods = append(slices.Clone(pkg.Methods), evm.ExternalMethod{Name: tc.name, Receiver: schema.NativeType(tc.receiver), Signature: evm.Signature{Results: []*values.Type{values.IntType}}})
		if _, err := evmcompiler.Compile("bad.go", `import "native";func Run(s *native.ShadowInner){_ = s}`, pkg); err == nil || !strings.Contains(err.Error(), "conflicts") {
			t.Fatal("direct field conflict accepted", tc.name, err)
		}
	}
}
