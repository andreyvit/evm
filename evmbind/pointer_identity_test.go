package evmbind_test

import (
	"context"
	"reflect"
	"testing"
	"testing/fstest"
	"time"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

type PointerSource struct {
	Value *int64
	Next  *PointerSource
	At    *time.Time
	List  []*int64
	Map   map[string]*int64
}
type PointerTarget struct {
	Value *int64
	At    *time.Time
	List  []*int64
	Map   map[string]*int64
}

func TestNativePointerIdentityAcrossPackages(t *testing.T) {
	a, err := evmbind.NewTypes("a", fstest.MapFS{"a.go": {Data: []byte(`package a; import "time"; type PointerSource struct { Value *int64; Next *PointerSource; At *time.Time; List []*int64; Map map[string]*int64 }`)}}, nil, evmbind.Options{Types: []reflect.Type{reflect.TypeFor[PointerSource]()}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := evmbind.NewTypes("b", fstest.MapFS{"b.go": {Data: []byte(`package b; import "time"; type PointerTarget struct { Value *int64; At *time.Time; List []*int64; Map map[string]*int64 }`)}}, nil, evmbind.Options{Types: []reflect.Type{reflect.TypeFor[PointerTarget]()}})
	if err != nil {
		t.Fatal(err)
	}
	module, err := evmcompiler.Compile("share.go", `import("a";"b");func Run(a *a.PointerSource,b *b.PointerTarget){b.Value=a.Value;b.At=a.At;b.List=a.List;b.Map=a.Map;*b.Value=42;*b.List[0]=42;*b.Map["value"]=42}`, a.Package(), b.Package())
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(module, a.Package(), b.Package())
	if err != nil {
		t.Fatal(err)
	}
	var value int64
	stamp := time.Now()
	source, target := &PointerSource{Value: &value, At: &stamp, List: []*int64{&value}, Map: map[string]*int64{"value": &value}}, &PointerTarget{}
	av, err := a.NativePointer(source)
	if err != nil {
		t.Fatal(err)
	}
	bv, err := b.NativePointer(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(p).Run(context.Background(), "Run", []values.Value{av, bv}, nil); err != nil {
		t.Fatal(err)
	}
	if target.Value != &value || target.At != &stamp || target.List[0] != &value || target.Map["value"] != &value || value != 42 {
		t.Fatal("pointer identity lost", value)
	}
}
