package evmbind

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
)

type SelectedThing struct{ Value int }

type SelectedAmount struct{ N int }

func (a SelectedAmount) Double() int { return a.N * 2 }

func (s *SelectedThing) Amount() SelectedAmount { return SelectedAmount{s.Value} }
func (s *SelectedThing) Self() *SelectedThing   { return s }
func (s *SelectedThing) Add(n int) int          { s.Value += n; return s.Value }
func (s *SelectedThing) Unselected() string     { return "hidden" }

func TestSelectedNativeMethods(t *testing.T) {
	files := fstest.MapFS{"thing.go": {Data: []byte(`package native
 import "unavailable/implementation"
 type SelectedThing struct {Value int}
 type SelectedAmount struct {N int}
 func (a SelectedAmount) Double()int{return a.N*2}
 func (s *SelectedThing) Amount()SelectedAmount{return SelectedAmount{s.Value}}
 func (s *SelectedThing) Self()*SelectedThing{return s}
 func (s *SelectedThing) Add(n int)int {implementation.Do();return 0}
 func (s *SelectedThing) Unselected()string{return "hidden"}
 `)}}
	schema, err := New("native", files, Options{Only: []string{"SelectedThing"}, Types: []reflect.Type{reflect.TypeFor[SelectedThing]()}, Methods: map[string][]string{"SelectedThing": {"Self", "Add", "Amount"}, "SelectedAmount": {"Double"}}})
	if err != nil {
		t.Fatal(err)
	}
	module, err := evmcompiler.Compile("run.go", `import "native";func Run(s *native.SelectedThing)int {if s.Self()!=s{panic("identity")};s.Add(3);return s.Amount().Double()}`, schema.Package())
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, schema.Bind(nil))
	if err != nil {
		t.Fatal(err)
	}
	host := &SelectedThing{Value: 4}
	arg, err := schema.NativePointer(host)
	if err != nil {
		t.Fatal(err)
	}
	var result [1]values.Value
	if err := evmtest.New(program).Run(context.Background(), "Run", []values.Value{arg}, result[:]); err != nil {
		t.Fatal(err)
	}
	if host.Value != 7 || result[0].AsInt() != 14 {
		t.Fatal(host, result)
	}
	if _, err := evmcompiler.Compile("bad.go", `import "native";func Run(s *native.SelectedThing){s.Unselected()}`, schema.Package()); err == nil {
		t.Fatal("unselected method exposed")
	}
	for _, methods := range []map[string][]string{{"SelectedThing": {"Missing"}}, {"SelectedThing": {"Add", "Add"}}, {"Missing": {"Add"}}} {
		_, err := New("native", files, Options{Only: []string{"SelectedThing"}, Types: []reflect.Type{reflect.TypeFor[SelectedThing]()}, Methods: methods})
		if err == nil || !strings.Contains(err.Error(), "method") {
			t.Fatal("invalid method selection", err)
		}
	}
}
