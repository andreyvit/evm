package evmbind_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/emptydata"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

func TestNativeEmptyStructCollections(t *testing.T) { testNativeEmptyStructCollections(t, nil) }
func testNativeEmptyStructCollections(t *testing.T, static *evmbind.Static) {
	t.Helper()
	opt := evmbind.Options{Types: []reflect.Type{reflect.TypeFor[emptydata.SetHolder]()}, Methods: map[string][]string{"SetHolder": {"Has"}}, Static: static}
	if static != nil {
		opt.Mode = evmbind.StaticOnly
	}
	schema, err := evmbind.NewTypes("sets", emptydata.Source, []string{"SetHolder"}, opt)
	if err != nil {
		t.Fatal(err)
	}
	pkg := schema.Bind(nil)
	consumer, err := evmbind.NewTypes("other", emptydata.Source, []string{"Consumer"}, evmbind.Options{Types: []reflect.Type{reflect.TypeFor[emptydata.Consumer]()}})
	if err != nil {
		t.Fatal(err)
	}
	other := consumer.Bind(nil)
	mod, err := evmcompiler.Compile("sets.go", `import("sets";"other")
 func Run(h *sets.SetHolder){
 if !h.Has("old") {panic("native map")}
 h.Set["new"]=struct{}{}
 delete(h.Set,"old")
 h.Items=append(h.Items,struct{}{})
 if len(h.Items)!=3 || h.Items[0]!=h.Items[2]{panic("empty values")}
 }
 func Share(h *sets.SetHolder,c *other.Consumer){c.Set=h.Set;c.Items=h.Items;c.Marker=h.Marker;c.Set["shared"]=struct{}{}}
func New()*sets.SetHolder{return &sets.SetHolder{Set:map[string]struct{}{"guest":{}},Items:make([]struct{},2),Marker:&struct{}{}}}
 `, pkg, other)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(mod, pkg, other)
	if err != nil {
		t.Fatal(err)
	}
	host := &emptydata.SetHolder{Set: map[string]struct{}{"old": {}}, Items: make([]struct{}, 2)}
	arg, err := schema.NativePointer(host)
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(program).Run(context.Background(), "Run", []values.Value{arg}, nil); err != nil {
		t.Fatal(err)
	}
	if !host.Has("new") || host.Has("old") || len(host.Items) != 3 {
		t.Fatal("native collection mutation lost", host)
	}
	out := make([]values.Value, 1)
	if err := evm.New(program).Run(context.Background(), "New", nil, out); err != nil {
		t.Fatal(err)
	}
	if out[0].R != values.NativeAddress {
		t.Fatal("guest struct lacks native storage", out[0])
	}
	created := (*emptydata.SetHolder)(out[0].P)
	c := &emptydata.Consumer{}
	carg, err := consumer.NativePointer(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(program).Run(context.Background(), "Share", []values.Value{out[0], carg}, nil); err != nil {
		t.Fatal(err)
	}
	if !created.Has("shared") || c.Marker != created.Marker || len(c.Items) != 2 {
		t.Fatal("cross-package structural identity lost", c)
	}
	if !created.Has("guest") || len(created.Items) != 2 {
		t.Fatal("guest empty collections lost native storage", created)
	}
}
