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
	"github.com/andreyvit/evm/evmbind/internal/collectionbindings"
	"github.com/andreyvit/evm/evmbind/internal/collectionfixture"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

type collectionHost struct{ panel collectionfixture.Panel }

func (h *collectionHost) GetAny(_ *evm.Call) any { return uint8(37) }

func (h *collectionHost) Roundtrip(_ *evm.Call, p collectionfixture.Panel) collectionfixture.Panel {
	if p.Metadata["item"] != p.Items[0] {
		panic("lost map any identity")
	}
	p.Nested[0]["host"] = uint64(1<<63) + 7
	if p.Metadata["host"] != uint64(1<<63)+7 {
		panic("lost nested map alias")
	}
	if p.Payload != p.Items[0] || p.Values[0] != p.Items[0] {
		panic("lost native any identity")
	}
	if p.Items[0] != p.Named["first"] {
		panic("lost guest aliases")
	}
	p.Items[0].Title = "host"
	p.Tags[0] = "host tag"
	if p.Groups[0][0] != "host tag" {
		panic("lost nested slice alias")
	}
	p.Named["second"] = p.Items[0]
	h.panel = p
	return p
}
func TestNativeCollectionCallRoundtrip(t *testing.T) {
	for _, mode := range []string{"reflection", "generated"} {
		t.Run(mode, func(t *testing.T) {
			options := evmbind.Options{Host: reflect.TypeFor[*collectionHost]()}
			if mode == "generated" {
				options.Mode = evmbind.StaticOnly
				options.Static = &collectionbindings.Bindings
			}
			schema, err := evmbind.New("collections", os.DirFS("internal/collectionfixture"), options)
			if err != nil {
				t.Fatal(err)
			}
			host := &collectionHost{}
			pkg := schema.Bind(host)
			module, err := evmcompiler.Compile("check.go", `import ("collections";"strings")
   func Get()any{return collections.GetAny()}
   type Local struct { N int }
   func Run() string {
    item:=&collections.Item{Title:"guest"}
    tags:=strings.Split("guest tag/second", "/")
    if strings.Join(tags,"/")!="guest tag/second" {panic("string slice primitive layout")}
    metadata:=map[string]any{"item":item}
    p:=collections.Panel{Metadata:metadata,Nested:[]map[string]any{metadata},Payload:item,Values:[]any{item},Tags:tags,Groups:[][]string{tags},Items:[]*collections.Item{item},Named:map[string]*collections.Item{"first":item}}
    result:=collections.Roundtrip(p)
    if result.Items[0]!=item || result.Named["second"]!=item {panic("lost host aliases")}
    result.Named["third"]=item
    if metadata["host"].(uint64)!=uint64(1<<63)+7 {panic("lost map value type")}
    if result.Payload.(*collections.Item)!=item || result.Values[0].(*collections.Item)!=item {panic("lost any result identity")}
    local:=&Local{N:9};result.Payload=local
    if result.Payload.(*Local)!=local {panic("lost guest-only type in native field")}
    if tags[0]!="host tag" || result.Groups[0][0]!="host tag" {panic("lost string slice writes")}
    return result.Items[0].Title
   }`, pkg)
			if err != nil {
				t.Fatal(err)
			}
			program, err := evm.Link(module, pkg)
			if err != nil {
				t.Fatal(err)
			}
			var out [1]values.Value
			if err := evm.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil {
				t.Fatal(err)
			}
			if err := evm.New(program).Run(context.Background(), "Get", nil, out[:]); err != nil {
				t.Fatal(err)
			}
			shape, value, err := program.UnpackInterface(out[0])
			if err != nil || shape.Type != values.Uint8 || value.R != 37 {
				t.Fatal("unmentioned native scalar", shape, value, err)
			}
			if err := evm.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil {
				t.Fatal(err)
			}
			if out[0].AsString() != "host" || host.panel.Named["third"] != host.panel.Items[0] {
				t.Fatal("lost native collection writes")
			}
		})
	}
}

func TestNativeCollectionGeneratedBindings(t *testing.T) {
	generated, err := evmbind.Generate("collections", "github.com/andreyvit/evm/evmbind/internal/collectionfixture", "collectionbindings", os.DirFS("internal/collectionfixture"))
	if err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile("internal/collectionbindings/bindings_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, saved) {
		t.Fatal("regenerate native collection bindings")
	}
}
