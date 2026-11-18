package gox_test

import (
	"context"
	"strings"
	"testing"

	"github.com/andreyvit/evm/gox"
	"github.com/andreyvit/evm/values"
)

func TestDataAttributesPreserveActionsAndEscapeText(t *testing.T) {
	source := `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, text string) {
 b.Open("input")
 b.Attr("type", "file")
 b.Data("action", text)
 b.Data("SRC", "javascript:plain-data")
 }`
	got := render(t, source, values.String(`change->upload#select "<script>&`))
	want := `<input type="file" data-action="change-&gt;upload#select &#34;&lt;script&gt;&amp;" data-src="javascript:plain-data">`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDataAttributesRejectInvalidContextsAndNames(t *testing.T) {
	for _, source := range []string{
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) { b.Data("action", "x") }`,
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) { <div>"child"; b.Data("action", "x")</div> }`,
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) { <div>b.Data("", "x")</div> }`,
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) { <div>b.Data("x\" onclick", "x")</div> }`,
	} {
		tmpl, err := gox.Compile("data.gox", source)
		if err != nil {
			t.Fatal(err)
		}
		p, err := tmpl.Link()
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Render(context.Background(), nil, gox.RenderOptions{})
		if err == nil {
			t.Fatalf("accepted %s", source)
		}
	}
}

func TestDataAttributesRespectOutputLimit(t *testing.T) {
	tmpl, err := gox.Compile("data.gox", `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, text string) { <div>b.Data("value", text)</div> }`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Render(context.Background(), []values.Value{values.String(strings.Repeat("<", 32))}, gox.RenderOptions{MaxOutputBytes: 64})
	if err == nil {
		t.Fatal("accepted expanded data beyond output limit")
	}
}

func TestAttrOrOmit(t *testing.T) {
	source := `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, anchor string) {
 <a title="existing">
  b.AttrOrOmit("id", anchor)
  b.AttrOrOmit("title", "")
  b.AttrOrOmit("data-nil", nil)
  b.AttrOrOmit("data-zero", 0)
  b.AttrOrOmit("data-false", false)
  b.AttrOrOmit("href", "javascript:alert(1)")
  b.AttrOrOmit("data-space", " ")
 </a>
 }`
	for _, tc := range []struct{ value, id string }{
		{"", ""},
		{`a"<&`, ` id="a&#34;&lt;&amp;"`},
	} {
		got := render(t, source, values.String(tc.value))
		want := `<a title="existing"` + tc.id + ` data-zero="0" data-false="false" href="#ZgotmplZ" data-space=" "></a>`
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestAttrOrOmitPreservesTrustedCSS(t *testing.T) {
	got := render(t, `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
  <div>
   b.AttrOrOmit("style", gox.CSS("color:red;display:block"))
   b.AttrOrOmit("style", gox.CSS(""))
  </div>
 }`)
	want := `<div style="color:red;display:block"></div>`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
