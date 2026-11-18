//go:build !evmreflect

package evmbind_test

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/embeddedbindings"
	"github.com/andreyvit/evm/evmbind/internal/embeddedfixture"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

func TestNativeEmbeddedFields(t *testing.T) {
	source := embeddedfixture.Source
	generated, err := evmbind.Generate("page", "github.com/andreyvit/evm/evmbind/internal/embeddedfixture", "embeddedbindings", source)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := os.ReadFile("internal/embeddedbindings/native_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(checked, generated) {
		t.Fatal("regenerate embedded bindings")
	}
	for _, static := range []*evmbind.Static{nil, &embeddedbindings.Bindings} {
		opt := evmbind.Options{Types: []reflect.Type{reflect.TypeFor[embeddedfixture.Pointer](), reflect.TypeFor[embeddedfixture.Value]()}, Static: static}
		if static != nil {
			opt.Mode = evmbind.StaticOnly
		}
		schema, err := evmbind.NewTypes("page", source, nil, opt)
		if err != nil {
			t.Fatal(err)
		}
		module, err := evmcompiler.Compile("test.go", `import "page"
 func Run(p *page.Pointer,v *page.Value)bool {
  p.Title="pointer"
  v.Title="value"
  copy:=*p
  copy.Title="alias"
  separate:=*v
  separate.Title="copy"
  return p.Title=="alias" && v.Title=="value" && separate.Title=="copy"
 }`, schema.Package())
		if err != nil {
			t.Fatal(err)
		}
		program, err := evm.Link(module, schema.Package())
		if err != nil {
			t.Fatal(err)
		}
		pointer := &embeddedfixture.Pointer{Base: &embeddedfixture.Base{Title: "before"}}
		value := &embeddedfixture.Value{Base: embeddedfixture.Base{Title: "before"}}
		p, err := schema.NativePointer(pointer)
		if err != nil {
			t.Fatal(err)
		}
		v, err := schema.NativePointer(value)
		if err != nil {
			t.Fatal(err)
		}
		var result [1]values.Value
		if err := evm.New(program).Run(context.Background(), "Run", []values.Value{p, v}, result[:]); err != nil {
			t.Fatal(err)
		}
		if result[0].R != 1 || pointer.Title != "alias" || value.Title != "value" {
			t.Fatalf("embedding semantics changed: %+v %+v", pointer, value)
		}
	}
}
