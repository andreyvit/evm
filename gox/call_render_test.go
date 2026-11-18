package gox_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/gox"
	"github.com/andreyvit/evm/values"
)

func TestReturnedRendererSharesCallingVM(t *testing.T) {
	tmpl, err := gox.Compile("view.gox", `package plugin

import "github.com/andreyvit/evm/gox"

var count int
 func Choose() func(*gox.Builder) {
  count=40
  name:="Alice & Bob"
  return func(b *gox.Builder){
   count++
   View(b,name)
  }
 }
 func View(b *gox.Builder,name string){
  <p>name; ":"; count; </p>
 }
 func Count()int{return count}
 `)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	vm := evm.New(p.EVM())
	var out [1]values.Value
	if err := vm.Run(context.Background(), "Choose", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	fn, err := p.EVM().FunctionValue(out[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<p>Alice &amp; Bob:41</p>", "<p>Alice &amp; Bob:42</p>"} {
		err := vm.RunNative(context.Background(), func(c *evm.Call) error {
			got, err := p.RenderFunctionInCall(c, fn, nil, gox.RenderOptions{Strict: true})
			if err != nil {
				return err
			}
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
			return nil
		}, evm.Budget{})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := vm.Run(context.Background(), "Count", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if out[0].AsInt() != 42 {
		t.Fatalf("handler globals reset: %d", out[0].AsInt())
	}
	if err := vm.RunNative(context.Background(), func(c *evm.Call) error {
		if _, err := p.RenderFunctionInCall(c, fn, nil, gox.RenderOptions{Budget: evm.Budget{Steps: 10}}); err == nil {
			t.Fatal("nested budget was silently ignored")
		}
		got, err := p.RenderFunctionInCall(c, fn, nil, gox.RenderOptions{MaxOutputBytes: 1})
		if got != "" {
			t.Fatalf("partial output escaped: %q", got)
		}
		return err
	}, evm.Budget{}); !errors.Is(err, evm.ErrOutput) {
		t.Fatalf("render output limit: %v", err)
	}
}

type nestedRenderHost struct{}

func (*nestedRenderHost) Nested(c *evm.Call, fn *evm.FunctionHandle) gox.HTML {
	html, err := gox.ProgramOf(c.Program()).RenderFunctionInCall(c, fn, nil, gox.RenderOptions{Strict: true})
	if err != nil {
		c.PanicCallback(err)
	}
	return gox.HTML(html)
}

func TestNestedRendererUsesItsOwnBuilder(t *testing.T) {
	host, err := evmbind.New("host", fstest.MapFS{"host.go": {Data: []byte(`package host
 import("github.com/andreyvit/evm";"github.com/andreyvit/evm/gox")
 func Nested(fn *evm.FunctionOf[func(*gox.Builder)]) gox.HTML
 `)}}, evmbind.Options{
		Host:    reflect.TypeFor[*nestedRenderHost](),
		Imports: map[string]*evmbind.Schema{"github.com/andreyvit/evm/gox": gox.Schema()},
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg := host.Bind(&nestedRenderHost{})
	tmpl, err := gox.Compile("outer.gox", `package plugin

import (
	"host"

	"github.com/andreyvit/evm/gox"
)

 var count int
 func Outer(b *gox.Builder){
  count=40
  <div>{host.Nested(Inner)}<span>{count}</span></div>
 }
 func OuterLiteral(b *gox.Builder){
  count=40
  html:=host.Nested(func(inner *gox.Builder){
   count++
   <b>{count}</b>
  })
  <div>{html}<span>{count}</span></div>
 }
 func Inner(b *gox.Builder){
  count++
  <b>{count}</b>
 }
 `, pkg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link(pkg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Render(context.Background(), nil, gox.RenderOptions{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := "<div><b>41</b><span>41</span></div>"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	got, err = p.RenderFunc(context.Background(), "OuterLiteral", nil, gox.RenderOptions{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := "<div><b>41</b><span>41</span></div>"; got != want {
		t.Fatalf("literal: got %q, want %q", got, want)
	}

}

func TestMarkupLiteralRejectsUnnamedBuilder(t *testing.T) {
	for _, param := range []string{"*gox.Builder", "_ *gox.Builder"} {
		_, err := gox.Compile("outer.gox", `package plugin

import "github.com/andreyvit/evm/gox"

func Outer(b *gox.Builder){
   f:=func(`+param+`){
    <b>{"inner"}</b>
   }
   f(b)
  }`)
		if err == nil || !strings.Contains(err.Error(), "markup requires a named builder parameter") {
			t.Fatalf("%s: %v", param, err)
		}
	}
}
