package evmbind_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
)

type variadicHost struct{}

func (*variadicHost) Sum(_ *evm.VM, initial int, values ...int) int {
	for _, value := range values {
		initial += value
	}
	return initial
}

func TestVariadicHostFunction(t *testing.T) {
	source := fstest.MapFS{"api.go": {Data: []byte(`package host;func Sum(initial int,values ...int)int`)}}
	schema, err := evmbind.New("host", source, evmbind.Options{Host: reflect.TypeFor[*variadicHost]()})
	if err != nil {
		t.Fatal(err)
	}
	pkg := schema.Bind(&variadicHost{})
	module, err := evmcompiler.Compile("host.go", `import "host"
 func Run(){
  if host.Sum(2)!=2 || host.Sum(2,3,4)!=9{panic("host variadic arguments")}
  fn:=host.Sum
  if fn(1,[]int{2,3}...)!=6{panic("host function value")}
 }`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(module, pkg)
	if err != nil {
		t.Fatal(err)
	}
	if err := evmtest.New(p).Run(context.Background(), "Run", nil, nil); err != nil {
		t.Fatal(err)
	}
	generated, err := evmbind.Generate("host", "example/host", "bindings", source, evmbind.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generated), "Sum(*evm.VM, int, ...int)") {
		t.Fatalf("generated host signature lost variadic argument:\n%s", generated)
	}
}

func TestVariadicNativeSignatureMismatch(t *testing.T) {
	for _, decl := range []string{"[]int", "...int"} {
		source := fstest.MapFS{"api.go": {Data: []byte("package host;func Sum(values " + decl + ")int")}}
		var fn any = func(...int) int { return 0 }
		if decl == "...int" {
			fn = func([]int) int { return 0 }
		}
		_, err := evmbind.New("host", source, evmbind.Options{Selective: true, Functions: map[string]any{"Sum": fn}})
		if err == nil || !strings.Contains(err.Error(), "native signature mismatch") {
			t.Fatalf("%s: %v", decl, err)
		}
	}
}
