//go:build !evmreflect

package evmbind_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/nativebind"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/modeldata"
	"github.com/andreyvit/evm/evmbind/internal/mutablebindings"
	"github.com/andreyvit/evm/evmbind/internal/mutablehost"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/kvo/codec/row2"
	"github.com/andreyvit/evm/values"
)

type mutableHost struct {
	row   *modeldata.BoundModel
	other *modeldata.Alternate
	sink  *mutablehost.SliceSink
}

func (h *mutableHost) Get(*evm.VM) *modeldata.BoundModel                               { return h.row }
func (h *mutableHost) Ordinary(*evm.VM) *modeldata.BoundModel                          { return h.row }
func (h *mutableHost) Other(*evm.VM) *modeldata.Alternate                              { return h.other }
func (h *mutableHost) Echo(_ *evm.VM, row *modeldata.BoundModel) *modeldata.BoundModel { return row }
func (h *mutableHost) EchoAny(_ *evm.VM, v any) any                                    { return v }

func (*mutableHost) ServiceInstance(*evm.VM) *mutablehost.Service { return &mutablehost.Service{} }

func (*mutableHost) CheckSlices(_ *evm.VM, tags []string, data []byte, numbers []int64) bool {
	if len(tags) != 2 || tags[1] != "b" || data[1] != 2 || numbers[1] != 4 {
		return false
	}
	tags[0] = "changed"
	data[0] = 9
	numbers[0] = 8
	return true
}

func (*mutableHost) InterfaceService(*evm.VM) mutablehost.RowService { return &mutablehost.Service{} }

func (h *mutableHost) Sink(*evm.VM) *mutablehost.SliceSink { return h.sink }

func TestMutableModelAnnotations(t *testing.T) {
	models, dep, err := modeldata.Declarations()
	if err != nil {
		t.Fatal(err)
	}
	imports := map[string]*evmbind.Schema{"github.com/andreyvit/evm/evmbind/internal/modeldata": dep}
	opt := evmbind.Options{Imports: imports, Host: reflect.TypeFor[*mutableHost](), Mutable: mutablehost.Annotations()}
	generated, err := evmbind.Generate("host", "github.com/andreyvit/evm/evmbind/internal/mutablehost", "mutablebindings", mutablehost.Source, opt)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := os.ReadFile("internal/mutablebindings/native_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, checked) {
		t.Fatal("regenerate mutable bindings")
	}
	for _, static := range []*evmbind.Static{nil, &mutablebindings.Bindings} {
		opt.Static = static
		if static != nil {
			opt.Mode = evmbind.StaticOnly
		}
		host, err := evmbind.New("host", mutablehost.Source, opt)
		if err != nil {
			t.Fatal(err)
		}
		h := &mutableHost{row: &modeldata.BoundModel{Name: "original", Hidden: 42}, other: &modeldata.Alternate{Label: "other"}}
		model := models.Records[0].Model
		edit := kvo.EditEncoded(row2.Edit(row2.Empty(row2.Compact2))).WithModel(model)
		dbx := evm.Package{Path: "dbx", Functions: []evm.ExternalFunction{{Name: "Edit", Signature: evm.Signature{Results: []*values.Type{models.Record(model, true).Pointer}}, Call: func(c *evm.VM) error { return c.SetEditRecordResult(model, edit) }}}}
		pkgs := []evm.Package{models, dep.Package(), host.Bind(h), dbx}
		mod, err := evmcompiler.Compile("mutable.go", `import("db";"host";"dbx")
func Run()bool{
 a:=host.Get(); b:=host.Get(); if a!=b{panic("pointer identity")}
 var boxed any=a; if boxed.(*db.CustomerEdit)!=a{panic("boxing")}
 keys:=map[*db.CustomerEdit]int{a:42};if keys[b]!=42{panic("hash identity")}
 if host.Echo(a)!=a{panic("native round trip")}
 if host.ServiceInstance().Replace(a)!=a{panic("method annotation")}
 if host.InterfaceService().Replace(a)!=a{panic("interface annotation")}
 a.name="changed";if host.Ordinary().name!="changed"{panic("not live")}
 if host.EchoAny(a).(*db.Customer).name!="changed"{panic("any defaults to immutable")}
 other:=host.Other();other.name="alternate";if other==a{panic("different objects")}
 if a.ToJSON()["name"]!="changed"{panic("json")}
 return true
}
func Nil()bool{var p *db.CustomerEdit;var a any=p;return host.Get()==nil&&host.Echo(nil)==nil&&a.(*db.CustomerEdit)==nil}
func RejectEditor(){host.Echo(dbx.Edit())}
func Slices(tags []string,data []byte,numbers []int64)bool{
 sink:=host.Sink();sink.Tags=tags;sink.Bytes=data;sink.Numbers=numbers;sink.Dict["tags"]=tags
 return host.CheckSlices(tags,data,numbers)
}
`, pkgs...)
		if err != nil {
			t.Fatal(err)
		}
		p, err := evm.Link(mod, pkgs...)
		if err != nil {
			t.Fatal(err)
		}
		vm := evmtest.New(p)
		var out [1]values.Value
		if err := vm.Run(context.Background(), "Run", nil, out[:]); err != nil || out[0].R != 1 {
			t.Fatal(out, err)
		}
		if h.row.Name != "changed" || h.row.Hidden != 42 || h.other.Label != "alternate" {
			t.Fatal("native changes lost", h)
		}

		h.sink = &mutablehost.SliceSink{Dict: map[string][]string{}}
		tags := []string{"a", "b"}
		data := []byte{1, 2}
		numbers := []int64{3, 4}
		view := func(x any) values.Value {
			rv := reflect.ValueOf(x)
			layout := nativebind.Layout(rv.Type(), nil, false)
			return values.NativeSliceValue(&values.NativeSliceView{Data: rv.UnsafePointer(), Layout: layout, Element: nativebind.Layout(rv.Type().Elem(), nil, false), Length: 2, Capacity: 2})
		}
		if err := vm.Run(context.Background(), "Slices", []values.Value{view(tags), view(data), view(numbers)}, out[:]); err != nil || out[0].R != 1 {
			t.Fatal("native slices", err)
		}
		if tags[0] != "changed" || data[0] != 9 || numbers[0] != 8 || h.sink.Tags[0] != "changed" || h.sink.Bytes[0] != 9 || h.sink.Numbers[0] != 8 || h.sink.Dict["tags"][0] != "changed" {
			t.Fatal("native slice backing lost")
		}
		h.row = nil
		if err := vm.Run(context.Background(), "Nil", nil, out[:]); err != nil || out[0].R != 1 {
			t.Fatal(out, err)
		}
		if err := vm.Run(context.Background(), "RejectEditor", nil, nil); err == nil || !strings.Contains(err.Error(), "requires native backing") {
			t.Fatal("editor accepted", err)
		}
		vm.Reset()
		if _, err := evmcompiler.Compile("immutable.go", `import "host";func Run(){host.Ordinary().name="bad"}`, pkgs...); err == nil {
			t.Fatal("default became mutable")
		}
	}

	opt.Static = &mutablebindings.Bindings
	opt.Mode = evmbind.StaticOnly
	opt.Mutable = mutablehost.Annotations()
	delete(opt.Mutable, "Echo")
	func() {
		defer func() {
			if x := recover(); x == nil || !strings.Contains(fmt.Sprint(x), "stale") {
				t.Fatal("stale annotation bindings accepted", x)
			}
		}()
		_, _ = evmbind.New("host", mutablehost.Source, opt)
	}()
	for _, bad := range []map[string]evmbind.MutableSlots{
		{"Missing": {Results: []int{0}}}, {"Get": {}}, {"Get": {Results: []int{-1}}}, {"Get": {Results: []int{1}}}, {"Echo": {Params: []int{0, 0}}}, {"EchoAny": {Params: []int{0}}},
	} {
		_, err := evmbind.New("host", mutablehost.Source, evmbind.Options{Imports: imports, Host: reflect.TypeFor[*mutableHost](), Mutable: bad})
		if err == nil {
			t.Fatalf("invalid annotation accepted: %v", bad)
		}
	}
}
