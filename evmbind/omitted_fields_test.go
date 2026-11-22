package evmbind

import (
	"context"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/nativebind"
	"math"
	"reflect"
	"testing"
	"testing/fstest"
	"unsafe"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind/internal/omittedfixture"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

type ProjectedPage struct {
	Title   string
	Private any
	Count   int
}

func TestOmittedNativeFields(t *testing.T) {
	files := fstest.MapFS{"page.go": {Data: []byte(`package page
 import "unavailable/host"
 type ProjectedPage struct { Title string; Private host.Context; Count int }
 `)}}
	schema, err := New("page", files, Options{
		Only:       []string{"ProjectedPage"},
		Types:      []reflect.Type{reflect.TypeFor[ProjectedPage]()},
		OmitFields: map[string][]string{"ProjectedPage": {"Private"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	module, err := evmcompiler.Compile("use.go", `import "page";func Update(p *page.ProjectedPage)int{p.Title="guest";p.Count++;return p.Count}`, schema.Package())
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, schema.Package())
	if err != nil {
		t.Fatal(err)
	}
	marker := &struct{ Value string }{"private"}
	page := &ProjectedPage{Title: "host", Private: marker, Count: 5}
	arg, err := schema.NativePointer(page)
	if err != nil {
		t.Fatal(err)
	}
	result := make([]values.Value, 1)
	if err := evmtest.New(program).Run(context.Background(), "Update", []values.Value{arg}, result); err != nil {
		t.Fatal(err)
	}
	if page.Title != "guest" || page.Count != 6 || page.Private != marker || result[0].AsInt() != 6 {
		t.Fatalf("native field offsets changed: %+v", page)
	}
	if _, err := evmcompiler.Compile("bad.go", `import "page";func Read(p *page.ProjectedPage)any{return p.Private}`, schema.Package()); err == nil {
		t.Fatal("private field exposed")
	}
}

type projectedPageHost struct{ page ProjectedPage }

func (h *projectedPageHost) Get(_ *evm.VM) ProjectedPage      { return h.page }
func (h *projectedPageHost) Pointer(_ *evm.VM) *ProjectedPage { return &h.page }
func (h *projectedPageHost) Check(_ *evm.VM, p *ProjectedPage) bool {
	return p.Private == h.page.Private && p.Title == "copy"
}

func TestOmittedNativeFieldsPreserveValueCopies(t *testing.T) {
	files := fstest.MapFS{"page.go": {Data: []byte(`package page
 type ProjectedPage struct {Title string;Private any;Count int}
 func Get()ProjectedPage{panic("host")}
 func Pointer()*ProjectedPage{panic("host")}
 func Check(p *ProjectedPage)bool{panic("host")}
 `)}}
	schema, err := New("page", files, Options{Host: reflect.TypeFor[*projectedPageHost](), OmitFields: map[string][]string{"ProjectedPage": {"Private"}}})
	if err != nil {
		t.Fatal(err)
	}
	module, err := evmcompiler.Compile("copy.go", `import "page"
func Run()bool{p:=page.Get();p.Title="copy";return page.Check(&p)}
func NativeCopy()bool{p:=*page.Pointer();p.Title="copy";return page.Check(&p)}
func NativeStore()bool{p:=*page.Pointer();p.Title="copy";*page.Pointer()=p;return page.Check(page.Pointer())}
func Reset(){*page.Pointer()=page.ProjectedPage{Title:"reset"}}`, schema.Package())
	if err != nil {
		t.Fatal(err)
	}
	marker := new(int)
	host := &projectedPageHost{page: ProjectedPage{Title: "original", Private: marker}}
	p, err := evm.Link(module, schema.Bind(host))
	if err != nil {
		t.Fatal(err)
	}
	result := make([]values.Value, 1)
	if err := evmtest.New(p).Run(context.Background(), "Run", nil, result); err != nil {
		t.Fatal(err)
	}
	if result[0].R != 1 || host.page.Title != "original" {
		t.Fatal("native value copy lost hidden fields or changed its source")
	}
	bound := schema.Bind(host)
	for i := range bound.Functions {
		fn := &bound.Functions[i]
		if fn.Name == "Get" {
			fn.Direct = evm.DirectBinding{}
			fn.Call = func(c *evm.VM) error {
				c.SetCallResult(0, nativebind.Load(reflect.TypeOf(host.page), unsafe.Pointer(&host.page)))
				return nil
			}
		}
	}
	borrowed, err := evm.Link(module, bound)
	if err != nil {
		t.Fatal(err)
	}
	if err := evmtest.New(borrowed).Run(context.Background(), "Run", nil, result); err != nil {
		t.Fatal(err)
	}
	if result[0].R != 1 || host.page.Title != "original" {
		t.Fatal("borrowed host aggregate lost its snapshot")
	}
	for _, name := range []string{"NativeCopy", "NativeStore"} {
		if err := evmtest.New(p).Run(context.Background(), name, nil, result); err != nil {
			t.Fatal(err)
		}
		if result[0].R != 1 {
			t.Fatalf("%s lost hidden fields", name)
		}
	}
	if err := evmtest.New(p).Run(context.Background(), "Reset", nil, nil); err != nil {
		t.Fatal(err)
	}
	if host.page.Private != marker || host.page.Title != "reset" {
		t.Fatalf("assignment lost omitted state or failed to write visible fields: %+v", host.page)
	}
}

func TestOmittedFieldValidation(t *testing.T) {
	files := fstest.MapFS{"page.go": {Data: []byte(`package page
 type Page struct { First, Second string }
 type Number int
 `)}}
	for name, fields := range map[string]map[string][]string{
		"missing type":  {"Missing": {"First"}},
		"missing field": {"Page": {"Missing"}},
		"duplicate":     {"Page": {"First", "First"}},
		"not struct":    {"Number": {"First"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseImportedFields("page", files, nil, true, nil, fields); err == nil {
				t.Fatal("invalid omission accepted")
			}
		})
	}
	src, err := parseImportedFields("page", files, []string{"Page"}, true, nil, map[string][]string{"Page": {"First"}})
	if err != nil {
		t.Fatal(err)
	}
	node := src.nodes["Page"]
	if node == nil || len(node.fields) != 1 || node.fields[0].name != "Second" {
		t.Fatal("grouped field omission changed sibling")
	}
}

// The omitted field belongs to another package with the same type name.
// It must not participate in resolving the exposed Page declaration.
type Page struct {
	Title   string
	Private *omittedfixture.Page
	private *omittedfixture.Page
	Items   []*VisibleItem
}

type VisibleItem struct{ Value string }

func TestOmittedNativeFieldsDoNotDiscoverHiddenTypes(t *testing.T) {
	files := fstest.MapFS{"page.go": {Data: []byte(`package page
 import "unavailable/host"
 type Page struct { Title string; Private *host.Page; private *host.Page; Items []*VisibleItem }
 type VisibleItem struct { Value string }
 `)}}
	schema, err := New("page", files, Options{
		Only:       []string{"Page"},
		Types:      []reflect.Type{reflect.TypeFor[Page]()},
		OmitFields: map[string][]string{"Page": {"Private", "private"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if schema.NativeType(reflect.TypeFor[Page]()) == nil {
		t.Fatal("missing exposed Page")
	}
	if schema.NativeType(reflect.TypeFor[VisibleItem]()) == nil {
		t.Fatal("missing exposed field dependency")
	}
	if schema.NativeType(reflect.TypeFor[omittedfixture.Page]()) != nil {
		t.Fatal("registered hidden Page")
	}
}

func TestOmittedNativeFieldsComparison(t *testing.T) {
	files := fstest.MapFS{"page.go": {Data: []byte(`package page
 type ProjectedPage struct {Title string;Private any;Count int}
 func Get()ProjectedPage{panic("host")}
 func Pointer()*ProjectedPage{panic("host")}
 func Check(p *ProjectedPage)bool{panic("host")}
 `)}}
	schema, err := New("page", files, Options{Host: reflect.TypeFor[*projectedPageHost](), OmitFields: map[string][]string{"ProjectedPage": {"Private"}}})
	if err != nil {
		t.Fatal(err)
	}
	module, err := evmcompiler.Compile("compare.go", `import "page"
func NaN()bool{a:=page.Get();m:=map[page.ProjectedPage]int{a:1};m[a]=2;delete(m,a);return a!=a && len(m)==2 && m[a]==0}
func Recover()(caught bool){defer func(){caught=recover()!=nil}();a:=page.Get();_=a==a;return}
func Run()bool {
 a:=page.Get(); b:=page.Get()
 if a!=b || any(a)!=any(b) { return false }
 m:=map[page.ProjectedPage]int{a:7}
 if m[b]!=7 {return false}
 b.Title="changed"
 if a==b || m[b]!=0 {return false}
 b.Title=a.Title
 if a!=b || m[b]!=7 {return false}
 z:=page.ProjectedPage{Title:a.Title,Count:a.Count}
 return a!=z && m[z]==0
}`, schema.Package())
	if err != nil {
		t.Fatal(err)
	}
	host := &projectedPageHost{page: ProjectedPage{Title: "same", Private: "hidden", Count: 3}}
	p, err := evm.Link(module, schema.Bind(host))
	if err != nil {
		t.Fatal(err)
	}
	result := make([]values.Value, 1)
	if err := evmtest.New(p).Run(context.Background(), "Run", nil, result); err != nil {
		t.Fatal(err)
	}
	if result[0].R != 1 {
		t.Fatal("native comparison or map key mismatch")
	}
	host.page.Private = []int{1}
	if err := evmtest.New(p).Run(context.Background(), "Recover", nil, result); err != nil {
		t.Fatal(err)
	}
	if result[0].R != 1 {
		t.Fatal("native comparison panic was not recoverable")
	}
	host.page.Private = math.NaN()
	if err := evmtest.New(p).Run(context.Background(), "NaN", nil, result); err != nil {
		t.Fatal(err)
	}
	if result[0].R != 1 {
		t.Fatal("native NaN map semantics differ")
	}

}

type HiddenSlice struct {
	Visible int
	hidden  []int
}

func TestOmittedNativeFieldsStaticComparability(t *testing.T) {
	files := fstest.MapFS{"native.go": {Data: []byte(`package native; type HiddenSlice struct {Visible int; hidden []int}`)}}
	schema, err := New("native", files, Options{Only: []string{"HiddenSlice"}, Types: []reflect.Type{reflect.TypeFor[HiddenSlice]()}, OmitFields: map[string][]string{"HiddenSlice": {"hidden"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{
		`import "native"; func Run(a,b native.HiddenSlice)bool{return a==b}`,
		`import "native"; func Run(){m:=make(map[native.HiddenSlice]int);_=m}`,
	} {
		if _, err := evmcompiler.Compile("bad.go", src, schema.Package()); err == nil {
			t.Fatal("accepted uncomparable native type", src)
		}
	}
}
