package gox_test

import (
	"context"
	"strings"
	"testing"

	"github.com/andreyvit/evm/gox"
	"github.com/andreyvit/evm/values"
)

func TestShowcase_recompile_and_render(t *testing.T) {
	data, err := examples.ReadFile("testdata/examples/showcase.gox")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	const want = `<section><h1>What shipped</h1><p>Hello, &lt;Ada &amp; Bob&gt;!</p><ul><li><strong>LIVE TEMPLATES</strong> <span class="badge" title="Accent: teal">shipped</span></li><li><strong>NATIVE BUILDS</strong> <span class="badge" title="Accent: teal">on the roadmap</span></li></ul><footer>2 updates. No host rebuild.</footer></section><style>.badge { color: teal; }</style><script>window.viewer = "\u003cAda \u0026 Bob\u003e";</script>`
	render := func(source string) string {
		t.Helper()
		tmpl, err := gox.Compile("showcase.gox", source)
		if err != nil {
			t.Fatal(err)
		}
		p, err := tmpl.Link()
		if err != nil {
			t.Fatal(err)
		}
		out, err := p.Render(context.Background(), []values.Value{values.String("<Ada & Bob>"), values.String("teal")}, gox.RenderOptions{Strict: true})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := render(source); got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
	formatted, err := gox.FormatSource("showcase.gox", source)
	if err != nil {
		t.Fatal(err)
	}
	if got := render(formatted); got != want {
		t.Fatal(got)
	}
	if got := render(strings.Replace(source, "What shipped", "Just shipped", 1)); got != strings.Replace(want, "What shipped", "Just shipped", 1) {
		t.Fatal(got)
	}
}
