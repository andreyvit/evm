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

type SelectedBase interface{ Read() int }
type SelectedService interface {
	SelectedBase
	Add(int)
	Hidden() chan int
}
type selectedService struct{ count int }

func (s *selectedService) Read() int        { return s.count }
func (s *selectedService) Add(n int)        { s.count += n }
func (s *selectedService) Hidden() chan int { return nil }

func TestSelectedNativeInterface(t *testing.T) {
	service := &selectedService{count: 7}
	files := fstest.MapFS{"service.go": {Data: []byte(`package service
 import "unavailable/internal"
 type SelectedBase interface { Read() int }
 type SelectedService interface { SelectedBase; Add(int); Hidden() internal.State }
 func Get() SelectedService { panic("native") }
 func Check(v SelectedService) bool { panic("native") }
 `)}}
	options := Options{
		Implementations: []Implementation{Pointer[selectedService]()},
		Types:           []reflect.Type{reflect.TypeFor[SelectedBase](), reflect.TypeFor[SelectedService]()},
		Methods:         map[string][]string{"SelectedService": {"SelectedBase", "Add"}},
		Functions: map[string]any{
			"Get":   func() SelectedService { return service },
			"Check": func(v SelectedService) bool { return v == service },
		},
	}
	schema, err := NewTypes("service", files, []string{"SelectedService"}, options)
	if err != nil {
		t.Fatal(err)
	}
	module, err := evmcompiler.Compile("use.go", `import "service"
 type Guest struct{}
 func(g *Guest)Read()int{return 0}
 func(g *Guest)Add(n int){}
 func RejectGuest(){service.Check(&Guest{})}
 func Run() int { var a any = service.Get(); v:=a.(service.SelectedService); v.Add(5); if !service.Check(v) {panic("identity")};return v.Read() }
 `, schema.Package())
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(module, schema.Bind(nil))
	if err != nil {
		t.Fatal(err)
	}
	result := make([]values.Value, 1)
	if err := evm.New(p).Run(context.Background(), "Run", nil, result); err != nil {
		t.Fatal(err)
	}
	if err := evm.New(p).Run(context.Background(), "RejectGuest", nil, nil); err == nil || !strings.Contains(err.Error(), "does not implement native interface") {
		t.Fatalf("guest subset handoff: %v", err)
	}
	if service.count != 12 || result[0].AsInt() != 12 {
		t.Fatal("lost native interface identity or dispatch")
	}
	if _, err := evmcompiler.Compile("hidden.go", `import "service";func Run(){service.Get().Hidden()}`, schema.Package()); err == nil {
		t.Fatal("hidden method exposed")
	}
	for _, name := range []string{"Missing", "Read"} {
		options.Methods = map[string][]string{"SelectedService": {name}}
		if _, err := NewTypes("service", files, []string{"SelectedService"}, options); err == nil || !strings.Contains(err.Error(), "not declared") {
			t.Fatalf("selection %s: %v", name, err)
		}
	}
	options.Methods = map[string][]string{"SelectedService": {"Add", "Add"}}
	if _, err := NewTypes("service", files, []string{"SelectedService"}, options); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate: %v", err)
	}
}

func TestSelectedNativeInterfaceValidation(t *testing.T) {
	for _, tc := range []struct {
		source  string
		methods []string
		want    string
	}{
		{`type SelectedService interface{ Add(string) }`, []string{"Add"}, "native argument mismatch"},
		{`type SelectedService interface{ Missing() }`, []string{"Missing"}, "missing native method"},
		{`type SelectedService interface{ hidden() }`, []string{"hidden"}, "not exported"},
		{`type SelectedService interface{ a.Reader; b.Reader }`, []string{"Reader"}, "ambiguous interface member"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			_, err := NewTypes("service", fstest.MapFS{"service.go": {Data: []byte("package service\n" + tc.source)}}, []string{"SelectedService"}, Options{
				Types: []reflect.Type{reflect.TypeFor[SelectedService]()}, Methods: map[string][]string{"SelectedService": tc.methods},
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s, got %v", tc.want, err)
			}
		})
	}
}

func TestUnselectedAmbiguousInterfaceMembers(t *testing.T) {
	_, err := NewTypes("service", fstest.MapFS{"service.go": {Data: []byte(`package service
 type SelectedService interface { a.Reader; b.Reader; Add(int) }
 `)}}, []string{"SelectedService"}, Options{
		Types: []reflect.Type{reflect.TypeFor[SelectedService]()}, Methods: map[string][]string{"SelectedService": {"Add"}},
	})
	if err != nil {
		t.Fatal(err)
	}
}
