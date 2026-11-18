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
	"github.com/andreyvit/evm/evmbind/internal/omittedfixture"
	"github.com/andreyvit/evm/evmbind/internal/renamedbindings"
	"github.com/andreyvit/evm/evmcompiler"
)

func TestGeneratedNativeTypeExportName(t *testing.T) {
	opt := evmbind.Options{Host: reflect.TypeFor[*omittedHost](), TypeNames: map[string]string{"Page": "ViewData"}, OmitFields: map[string][]string{"Page": {"Private"}}}
	generated, err := evmbind.GenerateWithOptions("page", "github.com/andreyvit/evm/evmbind/internal/omittedfixture", "renamedbindings", omittedfixture.Source, opt)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := os.ReadFile("internal/renamedbindings/native_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, checked) {
		t.Fatal("regenerate renamed bindings")
	}
	opt.Static = &renamedbindings.Bindings
	opt.Mode = evmbind.StaticOnly
	schema, err := evmbind.New("page", omittedfixture.Source, opt)
	if err != nil {
		t.Fatal(err)
	}
	module, err := evmcompiler.Compile("run.go", `import "page";func Run(){var v *page.ViewData=page.Pointer();v.Count++;v.Title="changed"}`, schema.Package())
	if err != nil {
		t.Fatal(err)
	}
	host := &omittedHost{page: omittedfixture.Page{Count: 7, Private: "untouched"}}
	p, err := evm.Link(module, schema.Bind(host))
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(p).Run(context.Background(), "Run", nil, nil); err != nil {
		t.Fatal(err)
	}
	if host.page.Count != 8 || host.page.Title != "changed" || host.page.Private != "untouched" {
		t.Fatal("renamed native pointer lost storage identity")
	}
}
