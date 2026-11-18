package evmbind

import (
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm/evmcompiler"
)

type SelectedFieldFixture struct {
	Visible string
	Hidden  chan int
	secret  int
}

func TestSelectedFieldsPruneDependencies(t *testing.T) {
	files := fstest.MapFS{"api.go": {Data: []byte(`package native
 import "unavailable/native"
 type SelectedFieldFixture struct { Visible string; Hidden chan int; secret native.Unsupported }
 `)}}
	for _, fields := range [][]string{{"Visible"}, {}} {
		s, err := New("native", files, Options{Only: []string{"SelectedFieldFixture"}, Types: []reflect.Type{reflect.TypeFor[SelectedFieldFixture]()}, Fields: map[string][]string{"SelectedFieldFixture": fields}})
		if err != nil {
			t.Fatal(err)
		}
		if s.NativeType(reflect.TypeFor[SelectedFieldFixture]()) == nil {
			t.Fatal("lost native identity")
		}
		for _, field := range []string{"Hidden", "secret"} {
			_, err := evmcompiler.Compile("hidden.go", `import "native";func Run(p *native.SelectedFieldFixture){_=p.`+field+`}`, s.Package())
			if err == nil {
				t.Fatalf("exposed %s", field)
			}
		}
		_, err = evmcompiler.Compile("visible.go", `import "native";func Run(p *native.SelectedFieldFixture){_=p.Visible}`, s.Package())
		if (err == nil) != (len(fields) > 0) {
			t.Fatalf("selection %v: %v", fields, err)
		}
	}
}

func TestSelectedFieldsErrors(t *testing.T) {
	files := fstest.MapFS{"api.go": {Data: []byte(`package native
 type A struct { X,Y int }
 type Alias = A
 type Scalar int
 `)}}
	for _, tc := range []struct {
		name   string
		fields map[string][]string
		omit   map[string][]string
		want   string
	}{
		{"duplicate", map[string][]string{"A": {"X", "X"}}, nil, "duplicate selected field A.X"},
		{"unknown field", map[string][]string{"A": {"Z"}}, nil, "unknown selected field A.Z"},
		{"unknown type", map[string][]string{"Missing": {}}, nil, "unknown selected-field type Missing"},
		{"scalar", map[string][]string{"Scalar": {}}, nil, "require a named struct"},
		{"alias", map[string][]string{"Alias": {}}, nil, "require a named struct"},
		{"conflict", map[string][]string{"A": {}}, map[string][]string{"A": {}}, "both Fields and OmitFields"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New("native", files, Options{Only: []string{"A"}, Fields: tc.fields, OmitFields: tc.omit})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
}
