package gox_test

import (
	"github.com/andreyvit/evm/gox"
	"testing"
)

func TestRendererParameterMetadata(t *testing.T) {
	tmpl, err := gox.CompileSources(map[string]string{"page.gox": `package plugin
func Page(b *gox.Builder) { <Title title={gox.Text("Hello")} /> }
func Title(b *gox.Builder, title gox.Content, text gox.Optional[string]) { <h1>title</h1> }`})
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	params := p.ParametersFor("Title")
	if len(params) != 2 || params[0].Name != "title" || params[1].Name != "text" {
		t.Fatalf("%+v", params)
	}
	params[0].Name = "changed"
	if p.ParametersFor("Title")[0].Name != "title" {
		t.Fatal("parameter metadata aliases internal state")
	}
}
