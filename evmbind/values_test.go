package evmbind_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

type ValueZone struct {
	Name   string
	offset int
}

func (z *ValueZone) Offset() int { return z.offset }

var (
	valueUTC      = &ValueZone{Name: "UTC"}
	valueErrLimit = errors.New("limit reached")
)

const valueSource = `package zones

import "errors"

type ValueZone struct {
	Name   string
	offset int
}

func (z *ValueZone) Offset() int { return z.offset }

var UTC *ValueZone = &ValueZone{Name: "UTC"}
var ErrLimit error = errors.New("limit reached")
var Retries, Workers int = 3, 8
var Unbound = 1
var hidden = 2
`

// Bound package variables are readable from scripts with their declared types.
func TestBoundPackageValues(t *testing.T) {
	schema, err := evmbind.New("zones", fstest.MapFS{"zones.go": {Data: []byte(valueSource)}}, evmbind.Options{
		Values: map[string]any{"UTC": valueUTC, "ErrLimit": valueErrLimit, "Retries": 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg := schema.Bind(nil)
	module, err := evmcompiler.Compile("use.go", `import ("zones"; "errors"; "fmt")
func fail() error { return fmt.Errorf("wrapped: %w", zones.ErrLimit) }
func Run() string {
	zone := zones.UTC
	zone.Name = "Zulu"
	out := fmt.Sprint(zones.UTC.Name, " ", zones.UTC.Offset(), " ", zones.Retries+1, " ", zones.ErrLimit.Error())
	if errors.Is(fail(), zones.ErrLimit) && zones.UTC == zone {
		out += " same"
	}
	return out
}`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, pkg)
	if err != nil {
		t.Fatal(err)
	}
	valueUTC.offset = 7
	t.Cleanup(func() { valueUTC.Name, valueUTC.offset = "UTC", 0 })
	var out [1]values.Value
	if err := evm.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if got, want := out[0].AsString(), "Zulu 7 4 limit reached same"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if valueUTC.Name != "Zulu" {
		t.Error("the script did not share the native object")
	}
	for name, body := range map[string]string{
		"unbound variable":     `var _ = zones.Unbound`,
		"unbound in a spec":    `var _ = zones.Workers`,
		"unexported variable":  `var _ = zones.hidden`,
		"assigning a variable": `func Run() { zones.Retries = 5 }`,
	} {
		if _, err := evmcompiler.Compile("use.go", "import \"zones\"\n"+body, pkg); err == nil {
			t.Errorf("%s compiled", name)
		}
	}
}

func TestBoundPackageValueErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		values map[string]any
		funcs  map[string]any
		want   string
	}{
		"not declared":     {map[string]any{"Missing": 1}, nil, "bound value Missing is not declared as a variable in source"},
		"no declared type": {map[string]any{"Unbound": 1}, nil, "bound value Unbound needs a declared type in source"},
		"unexported":       {map[string]any{"hidden": 2}, nil, "hidden"},
		"wrong type":       {map[string]any{"Retries": "three"}, nil, "native value Retries does not have the declared type int"},
		"also a function":  {map[string]any{"Retries": 3}, map[string]any{"Retries": func() int { return 3 }}, "Retries is bound both as a value and as a function"},
	} {
		_, err := evmbind.New("zones", fstest.MapFS{"zones.go": {Data: []byte(valueSource)}}, evmbind.Options{Values: tc.values, Functions: tc.funcs, Types: []reflect.Type{reflect.TypeFor[ValueZone]()}})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want error containing %q, got %v", name, tc.want, err)
		}
	}
}
