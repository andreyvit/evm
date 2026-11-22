//go:build !evmreflect

package evmbind_test

import (
	"bytes"
	"context"
	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/omittedbindings"
	"github.com/andreyvit/evm/evmbind/internal/omittedfixture"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
	"os"
	"reflect"
	"testing"
)

type omittedHost struct {
	page   omittedfixture.Page
	marker any
}

func (h *omittedHost) Get(*evm.VM) omittedfixture.Page      { return h.page }
func (h *omittedHost) Pointer(*evm.VM) *omittedfixture.Page { return &h.page }
func (h *omittedHost) Check(_ *evm.VM, p *omittedfixture.Page) bool {
	return p.Private == h.marker && p.Title == "copy"
}

func TestGeneratedOmittedFields(t *testing.T) {
	testGeneratedFieldSelection(t, evmbind.Options{OmitFields: map[string][]string{"Page": {"Private"}}})
}
func TestGeneratedSelectedFields(t *testing.T) {
	testGeneratedFieldSelection(t, evmbind.Options{Fields: map[string][]string{"Page": {"Title", "Count"}}})
}
func testGeneratedFieldSelection(t *testing.T, opt evmbind.Options) {
	t.Helper()
	opt.Host = reflect.TypeFor[*omittedHost]()
	generated, err := evmbind.Generate("page", "github.com/andreyvit/evm/evmbind/internal/omittedfixture", "omittedbindings", omittedfixture.Source, opt)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := os.ReadFile("internal/omittedbindings/native_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, checked) {
		t.Fatal("regenerate omitted bindings")
	}
	for _, static := range []*evmbind.Static{nil, &omittedbindings.Bindings} {
		opt.Static = static
		if static != nil {
			opt.Mode = evmbind.StaticOnly
		}
		schema, err := evmbind.New("page", omittedfixture.Source, opt)
		if err != nil {
			t.Fatal(err)
		}
		module, err := evmcompiler.Compile("copy.go", `import "page"
func Compare()bool{a:=page.Get();b:=page.Get();m:=map[page.Page]int{a:1};z:=page.Page{Title:a.Title,Count:a.Count};return a==b && any(a)==any(b) && a!=z && m[b]==1 && m[z]==0}
func Run()bool{p:=page.Get();p.Title="copy";return page.Check(&p)}
func Copy()bool{p:=*page.Pointer();p.Title="copy";return page.Check(&p)}
func Store()bool{p:=*page.Pointer();p.Title="copy";*page.Pointer()=p;return page.Check(page.Pointer())}
func Reset(){*page.Pointer()=page.Page{Title:"reset"}}
`, schema.Package())
		if err != nil {
			t.Fatal(err)
		}
		marker := new(int)
		host := &omittedHost{page: omittedfixture.Page{Title: "original", Private: marker}, marker: marker}
		program, err := evm.Link(module, schema.Bind(host))
		if err != nil {
			t.Fatal(err)
		}
		var result [1]values.Value
		for _, name := range []string{"Compare", "Run", "Copy", "Store"} {
			if err := evmtest.New(program).Run(context.Background(), name, nil, result[:]); err != nil {
				t.Fatal(err)
			}
			if result[0].R != 1 {
				t.Fatalf("%s lost native fields", name)
			}
			if name != "Store" && host.page.Title != "original" {
				t.Fatal("copy modified original")
			}
		}
		if err := evmtest.New(program).Run(context.Background(), "Reset", nil, nil); err != nil {
			t.Fatal(err)
		}
		if host.page.Private != marker || host.page.Title != "reset" {
			t.Fatalf("reset: %+v", host.page)
		}
	}
}
