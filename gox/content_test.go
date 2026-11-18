package gox_test

import (
	"context"
	"github.com/andreyvit/evm/gox"
	"testing"
)

func TestContentRendering(t *testing.T) {
	tmpl, err := gox.CompileSources(map[string]string{"page.gox": `package plugin

func Title(b *gox.Builder, title gox.Content) { <h1>title</h1> }
func Page(b *gox.Builder) {
 <Title title={gox.Text("<b>hello</b>")} />
 <Title title={gox.TrustedHTML(gox.HTML("<b>hello</b>"))} />
 <Title title={gox.Content{}} />
 <p title={gox.Text("a\"<b>")}>gox.Text("&")</p>
 <a title={gox.TrustedHTML(gox.HTML("<b>x</b>\""))}></a>
 b.Text("<end>")
}`})
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.RenderFunc(context.Background(), "Page", nil, gox.RenderOptions{})
	want := `<h1>&lt;b&gt;hello&lt;/b&gt;</h1><h1><b>hello</b></h1><h1></h1><p title="a&#34;&lt;b&gt;">&amp;</p><a title="x&#34;"></a>&lt;end&gt;`
	if err != nil || got != want {
		t.Fatalf("got %q, want %q: %v", got, want, err)
	}
}

func TestContentValueSemantics(t *testing.T) {
	tmpl, err := gox.CompileSources(map[string]string{"page.gox": `package plugin

type Other gox.Content
func Copy(x gox.Content) gox.Content { return x }
func Page(b *gox.Builder) {
 var zero gox.Content
 if zero != gox.Text("") { panic("zero") }
 x := gox.Text("hello")
 y := Copy(x)
 converted := Other(x)
 converted.TrustedHTML=true
 if gox.Content(converted).Content!="hello" || !gox.Content(converted).TrustedHTML {panic("conversion")}
 p := &x.Content
 *p = "<b>changed</b>"
 trusted := &x.TrustedHTML
 *trusted = true
 if y.Content != "hello" || y.TrustedHTML { panic("copy") }
 var boxed any = x
 recovered := boxed.(gox.Content)
 if recovered != x { panic("interface") }
 m := map[gox.Content]int{x: 1, y: 2, gox.TrustedHTML(gox.HTML("")): 3}
 if m[gox.Content{Content:"<b>changed</b>",TrustedHTML:true}] != 1 { panic("map") }
 if m[gox.Text("")] != 0 { panic("trust") }
 var z any = zero
 if z.(gox.Content) != zero { panic("boxed zero") }
 <p>recovered</p>
 <p>y</p>
}`})
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.RenderFunc(context.Background(), "Page", nil, gox.RenderOptions{})
	if err != nil || got != "<p><b>changed</b></p><p>hello</p>" {
		t.Fatalf("%q: %v", got, err)
	}
}
