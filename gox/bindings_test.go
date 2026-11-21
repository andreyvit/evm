package gox_test

import (
	"context"
	"html/template"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/gox"
	"github.com/andreyvit/evm/values"
)

type NativeViewHolder struct {
	View *evm.FunctionOf[func(*gox.Builder)]
}

func TestNativeBuilderRetainedView(t *testing.T) {
	var received *gox.Builder
	calls := 0
	host, err := evmbind.New("host", fstest.MapFS{"host.go": {Data: []byte(`package host
 import("github.com/andreyvit/evm";"github.com/andreyvit/evm/gox";"html/template")
 type NativeViewHolder struct { View *evm.FunctionOf[func(*gox.Builder)] }
 func Native(b *gox.Builder, text template.HTML) {}
 func Content() template.HTML { return "" }
 `)}}, evmbind.Options{
		Only:    []string{"NativeViewHolder"},
		Types:   []reflect.Type{reflect.TypeFor[NativeViewHolder]()},
		Imports: map[string]*evmbind.Schema{"github.com/andreyvit/evm/gox": gox.Schema(), "html/template": gox.TrustedContentSchema()},
		Functions: map[string]any{"Content": func() template.HTML { return template.HTML("<b>native form</b>") }, "Native": func(b *gox.Builder, text template.HTML) {
			if received != nil && received != b {
				t.Fatal("builder pointer changed during render")
			}
			received = b
			calls++
			if err := b.HTML(text); err != nil {
				panic(err)
			}
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg := host.Bind(nil)
	tpl, err := gox.CompileSources(map[string]string{
		"main.go": `package main; import("host";"github.com/andreyvit/evm/gox"); func Initialize(h *host.NativeViewHolder){suffix:=gox.HTML("<em>native</em>");h.View=func(b *gox.Builder){View(b);host.Native(b,suffix)}}`,
		"view.gox": `package main

import "github.com/andreyvit/evm/gox"

 import "host"
 func View(b *gox.Builder) {
 <p>{"<escaped>"}</p>
 <div>{host.Content()}</div>
 host.Native(b,gox.HTML("<strong>shared</strong>"))
 }
 `,
	}, pkg)
	if err != nil {
		t.Fatal(err)
	}
	program, err := tpl.Link(pkg)
	if err != nil {
		t.Fatal(err)
	}
	holder := &NativeViewHolder{}
	arg, err := host.NativePointer(holder)
	if err != nil {
		t.Fatal(err)
	}
	vm := evmtest.New(program.EVM())
	if err := vm.Run(context.Background(), "Initialize", []values.Value{arg}, nil); err != nil {
		t.Fatal(err)
	}
	if err := vm.Reset(); err != nil {
		t.Fatal(err)
	}
	got, err := program.RenderFunction(context.Background(), holder.View, nil, gox.RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if want := "<p>&lt;escaped&gt;</p><div><b>native form</b></div><strong>shared</strong><em>native</em>"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if calls != 2 {
		t.Fatalf("native calls: %d", calls)
	}
}

func TestNativeHTMLAttrKeepsAttributeValueEscaping(t *testing.T) {
	host, err := evmbind.New("host", fstest.MapFS{"host.go": {Data: []byte(`package host
 import "html/template"
 func Attr() template.HTMLAttr {return ""}
 `)}}, evmbind.Options{Selective: true, Imports: map[string]*evmbind.Schema{"html/template": gox.TrustedContentSchema()}, Functions: map[string]any{"Attr": func() template.HTMLAttr { return template.HTMLAttr(`x" onclick="bad`) }}})
	if err != nil {
		t.Fatal(err)
	}
	pkg := host.Bind(nil)
	tpl, err := gox.CompileSources(map[string]string{"view.gox": `package main

import "github.com/andreyvit/evm/gox"

 import "host"
 func View(b *gox.Builder) {
 <p title={host.Attr()}>{host.Attr()}</p>
 }
 `}, pkg)
	if err != nil {
		t.Fatal(err)
	}
	program, err := tpl.Link(pkg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := program.RenderFunc(context.Background(), "View", nil, gox.RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if want := `<p title="x&#34; onclick=&#34;bad">x&#34; onclick=&#34;bad</p>`; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestNativeComponentArgumentDirectives(t *testing.T) {
	host, err := evmbind.New("host", fstest.MapFS{"host.go": {Data: []byte(`package host
import "github.com/andreyvit/evm/gox"
//gox:arg suffix optional=true legacyName="old_suffix"
func Class(b *gox.Builder, element, suffix string) {}
`)}}, evmbind.Options{
		Selective: true,
		Imports:   map[string]*evmbind.Schema{"github.com/andreyvit/evm/gox": gox.Schema()},
		Functions: map[string]any{"Class": func(b *gox.Builder, element, suffix string) { b.Class(element + suffix) }},
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg := host.Bind(nil)
	printed, err := gox.PackageSource("host", pkg, gox.Package())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(printed, `//gox:arg suffix optional=true legacyName="old_suffix"`) {
		t.Fatal(printed)
	}

	source := `package plugin

import (
	"host"

	"github.com/andreyvit/evm/gox"
)

func Page(b *gox.Builder) {
 suffix := "must-not-inherit"
 _ = suffix
 <div><host.Class element="header" /></div>
 <div><host.Class element="header" suffix="-child" /></div>
}`
	tmpl, err := gox.CompileSources(map[string]string{"page.gox": source}, pkg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link(pkg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.RenderFunc(context.Background(), "Page", nil, gox.RenderOptions{})
	if err != nil || got != `<div class="header"></div><div class="header-child"></div>` {
		t.Fatalf("%q: %v", got, err)
	}
}

func TestNativeContentRoundTrip(t *testing.T) {
	source := fstest.MapFS{"host.go": {Data: []byte(`package host
import "github.com/andreyvit/evm/gox"
func Echo(x gox.Content) gox.Content { return x }
func Any(x any) any { return x }
func Change(x *gox.Content) {}
`)}}
	host, err := evmbind.New("host", source, evmbind.Options{
		Selective: true,
		Imports:   map[string]*evmbind.Schema{"github.com/andreyvit/evm/gox": gox.Schema()},
		Functions: map[string]any{
			"Echo":   func(x gox.Content) gox.Content { return x },
			"Any":    func(x any) any { return x },
			"Change": func(x *gox.Content) { x.Content = "<em>native</em>"; x.TrustedHTML = true },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg := host.Bind(nil)
	tmpl, err := gox.Compile("native.gox", `package plugin

import "github.com/andreyvit/evm/gox"

import("host";"fmt")
func Page(b *gox.Builder) {
 x:=gox.Text("<plain>")
 if host.Echo(x)!=x || host.Any(x).(gox.Content)!=x {panic("round trip")}
 if fmt.Sprint(x)!="{<plain> false}" {panic("format")}
 host.Change(&x)
 y:=host.Echo(x)
 if !y.TrustedHTML {panic("trust")}
 <p>host.Any(y).(gox.Content)</p>
}`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link(pkg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Render(context.Background(), nil, gox.RenderOptions{})
	if err != nil || got != "<p><em>native</em></p>" {
		t.Fatalf("%q: %v", got, err)
	}
}
