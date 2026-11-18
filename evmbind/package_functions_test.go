package evmbind_test

import (
	"context"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/rangefixture"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

func TestNativePackageFunctionsFromSource(t *testing.T) {
	schema, err := evmbind.New("ranges", rangefixture.Source, evmbind.Options{
		Only:      []string{},
		Methods:   map[string][]string{"Range": {"Reversed"}},
		Functions: map[string]any{"Inclusive": rangefixture.Inclusive, "WithPrefix": rangefixture.WithPrefix, "Open": rangefixture.Open},
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg := schema.Bind(nil)
	module, err := evmcompiler.Compile("ranges.go", `import "ranges"
 func Run() bool {
  lower:=[]byte("a");r:=ranges.Inclusive(lower,[]byte("z")).Reversed()
  lower[0]='b'
  p:=ranges.WithPrefix(lower)
  return string(r.Lower)=="b" && string(r.Upper)=="z" && r.LowerInc && r.UpperInc && r.Reverse && string(p.Prefix)=="b" && ranges.Open().Lower==nil
 }`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, pkg)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil || out[0].R != 1 {
		t.Fatal(out, err)
	}
}

func TestNativePackageFunctionValidation(t *testing.T) {
	files := fstest.MapFS{"api.go": {Data: []byte(`package native
 func Add(n int) int { panic("body is not executed") }
 func private(n int) int { return n }
 `)}}
	var nilFunction func(int) int
	for _, tc := range []struct {
		name      string
		functions map[string]any
		want      string
	}{
		{"missing", map[string]any{"Absent": func() {}}, "not declared in source"},
		{"private", map[string]any{"private": func(n int) int { return n }}, "not exported"},
		{"nil", map[string]any{"Add": nil}, "invalid native function Add"},
		{"typed nil", map[string]any{"Add": nilFunction}, "invalid native function Add"},
		{"value", map[string]any{"Add": 42}, "invalid native function Add"},
		{"signature", map[string]any{"Add": func() int { return 1 }}, "native signature mismatch"},
		{"argument", map[string]any{"Add": func(string) int { return 1 }}, "native argument mismatch"},
		{"result", map[string]any{"Add": func(int) string { return "x" }}, "native result mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := evmbind.New("native", files, evmbind.Options{Only: []string{}, Functions: tc.functions})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
	calls := 0
	schema, err := evmbind.New("native", files, evmbind.Options{Only: []string{}, Functions: map[string]any{"Add": func(n int) int { calls++; return n + 7 }}})
	if err != nil {
		t.Fatal(err)
	}
	pkg := schema.Bind(nil)
	module, err := evmcompiler.Compile("call.go", `import "native";func Run()int{return native.Add(5)}`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, pkg)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil || out[0].R != 12 || calls != 1 {
		t.Fatal(out, calls, err)
	}
}

type packageFunctionHost struct{}

func (*packageFunctionHost) ViaHost(_ *evm.Call, n int) int      { return n + 4 }
func (*packageFunctionHost) Direct(_ *evm.Call, s string) string { return s }

func TestNativePackageFunctionAndHostMethods(t *testing.T) {
	files := fstest.MapFS{"api.go": {Data: []byte(`package native
 func ViaHost(n int)int{panic("host")}
 func Direct(n int)int{panic("native")}
 `)}}
	schema, err := evmbind.New("native", files, evmbind.Options{Host: reflect.TypeFor[*packageFunctionHost](), Functions: map[string]any{"Direct": func(n int) int { return n * 3 }}})
	if err != nil {
		t.Fatal(err)
	}
	pkg := schema.Bind(&packageFunctionHost{})
	module, err := evmcompiler.Compile("mixed.go", `import "native";func Run()int{return native.ViaHost(native.Direct(5))}`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, pkg)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil || out[0].R != 19 {
		t.Fatal(out, err)
	}
}

func TestNativePackageFunctionRejectsHostBindingFingerprint(t *testing.T) {
	files := fstest.MapFS{"api.go": {Data: []byte(`package native;func Add(n int)int{panic("native")}`)}}
	old, err := evmbind.Generate("native", "example/native", "bindings", files, evmbind.Options{})
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := regexp.MustCompile(`Fingerprint: "([^"]+)"`).FindSubmatch(old)
	if len(fingerprint) != 2 {
		t.Fatal("missing generated fingerprint")
	}
	defer func() {
		if value := recover(); value == nil || !strings.Contains(value.(string), "stale generated bindings") {
			t.Fatalf("got %v, want stale bindings panic", value)
		}
	}()
	_, err = evmbind.New("native", files, evmbind.Options{
		Functions: map[string]any{"Add": func(n int) int { return n + 1 }},
		Mode:      evmbind.StaticOnly,
		Static:    &evmbind.Static{Fingerprint: string(fingerprint[1]), Calls: map[string]evmbind.Wrapper{"Add": func(*evmbind.Frame) { panic("stale wrapper") }}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

type SourceRangeBox struct{ Range rangefixture.Range }

func TestNativePackageFunctionImportedTypes(t *testing.T) {
	const rangePath = "github.com/andreyvit/evm/evmbind/internal/rangefixture"
	dep, err := evmbind.New("ranges", rangefixture.Source, evmbind.Options{Only: []string{}, Functions: map[string]any{"Inclusive": rangefixture.Inclusive}})
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{"api.go": {Data: []byte(`package consumer
 import "` + rangePath + `"
 type SourceRangeBox struct{Range rangefixture.Range}
 func Make()SourceRangeBox{panic("native")}
 `)}}
	schema, err := evmbind.New("consumer", files, evmbind.Options{
		Only:      []string{},
		Imports:   map[string]*evmbind.Schema{rangePath: dep},
		Functions: map[string]any{"Make": func() SourceRangeBox { return SourceRangeBox{rangefixture.Inclusive([]byte("a"), []byte("z"))} }},
	})
	if err != nil {
		t.Fatal(err)
	}
	packages := []evm.Package{dep.Bind(nil), schema.Bind(nil)}
	module, err := evmcompiler.Compile("imports.go", `import("ranges";"consumer");func Run()bool{var r ranges.Range=consumer.Make().Range;return string(r.Lower)=="a" && r.LowerInc && r.UpperInc}`, packages...)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, packages...)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil || out[0].R != 1 {
		t.Fatal(out, err)
	}
}
