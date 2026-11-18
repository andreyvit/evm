package gox_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/gox"
	"github.com/andreyvit/evm/values"
)

func TestPlannedTagsRemainMutable(t *testing.T) {
	source := `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, extra string) {
 <section class="outer">
  b.Class(extra)
  for i:=0;(i<3);i++ {
   <button class="base" disabled title="original">
    b.Attr("disabled",i==1)
    b.Class("one")
    if i==2 {b.Attr("class", "")}
    b.Class(extra)
    b.Attr("title",extra)
    <svg><path d="M0 0L1 1" /></svg>
    "label"
   </button>
  }
 </section>
}`
	tmpl, err := gox.Compile("mutable.gox", source)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	in := []values.Value{values.String("x&y")}
	icon := `<svg><path d="M0 0L1 1"></path></svg>label</button>`
	want := `<section class="outer x&amp;y"><button class="base one x&amp;y" title="x&amp;y">` + icon +
		`<button class="base one x&amp;y" disabled title="x&amp;y">` + icon +
		`<button class="x&amp;y" title="x&amp;y">` + icon + `</section>`
	for range 3 {
		for _, limit := range []int{1, 25, len(want) - 1} {
			if got, err := p.Render(ctx, in, gox.RenderOptions{MaxOutputBytes: limit}); !errors.Is(err, evm.ErrOutput) || got != "" {
				t.Fatalf("limit %d: %q %v", limit, got, err)
			}
		}
		got, err := p.Render(ctx, in, gox.RenderOptions{MaxOutputBytes: len(want)})
		if err != nil || got != want {
			t.Fatalf("got %q %v; want %q", got, err, want)
		}
	}
}

func TestBatchedOperationsPreserveAttributeEvaluation(t *testing.T) {
	// The helper cannot edit the opening tag from inside an attribute expression.
	// Batching must retain the begin-attribute operation before evaluating it.
	tmpl, err := gox.Compile("attributes.gox", `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
 <div class="base" title={mutate()}></div>
}
func mutate(b *gox.Builder)string {b.Class("extra");return "value"}`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Render(context.Background(), nil, gox.RenderOptions{})
	if got != "" || err == nil || !strings.Contains(err.Error(), "attribute helper called while evaluating an attribute") {
		t.Fatal(got, err)
	}
}

func TestAttributeHelpersInTextOnlyElements(t *testing.T) {
	tmpl, err := gox.Compile("classes.gox", `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, value string) {
 <textarea class="base">
  addClass("field")
  value
 </textarea>
 <title>
  addClass("heading")
  value
 </title>
 <label class="parent">
  <input title={value}>
  addClass("input")
  <img />
  addClass("image")
 </label>
}
func addClass(b *gox.Builder, value string) { b.Class(value) }`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Render(context.Background(), []values.Value{values.String("</textarea><script>")}, gox.RenderOptions{})
	want := `<textarea class="base field">&lt;/textarea&gt;&lt;script&gt;</textarea><title class="heading">&lt;/textarea&gt;&lt;script&gt;</title><label class="parent"><input title="&lt;/textarea&gt;&lt;script&gt;" class="input"><img class="image"></label>`
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestTextOnlyElementsRejectRenderingHelpers(t *testing.T) {
	tmpl, err := gox.Compile("textarea.gox", `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
 <textarea>markup()</textarea>
}
func markup(b *gox.Builder) { <strong>"unsafe"</strong> }`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Render(context.Background(), nil, gox.RenderOptions{})
	if got != "" || err == nil {
		t.Fatalf("got %q, %v; want rendering context error", got, err)
	}
}
