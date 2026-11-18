package gox_test

import (
	"context"
	"strings"
	"testing"

	"github.com/andreyvit/evm/gox"
)

// Content/optional component inputs exercise host constructors, any rendering,
// named optional arguments, and repeated calls within a single render.
func BenchmarkContentOptional(b *testing.B) {
	tmpl, err := gox.Compile("content.gox", `package plugin

func Item(b *gox.Builder, title gox.Content, subtitle gox.Optional[string]) {
 <article><h2>title</h2>
  if subtitle.Present { <p>subtitle.Value</p> }
 </article>
}
func Page(b *gox.Builder) {
 for i := 0; (i < 48); i++ {
  <Item title={gox.Text("<title>")} subtitle="subtitle" />
  <Item title={gox.TrustedHTML(gox.HTML("<b>trusted</b>"))} />
 }
}`)
	if err != nil {
		b.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	want := strings.Repeat("<article><h2>&lt;title&gt;</h2><p>subtitle</p></article><article><h2><b>trusted</b></h2></article>", 48)
	if got, err := p.RenderFunc(ctx, "Page", nil, gox.RenderOptions{}); err != nil || got != want {
		b.Fatalf("unexpected content render: %v", err)
	}
	b.ReportAllocs()
	for b.Loop() {
		benchmarkOutput, err = p.RenderFunc(ctx, "Page", nil, gox.RenderOptions{})
		if err != nil {
			b.Fatal(err)
		}
	}
}
