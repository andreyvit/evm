//go:build !evmreflect

package gox_test

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/gox"
	"github.com/andreyvit/evm/gox/internal/contentbindings"
	"github.com/andreyvit/evm/gox/internal/contentfixture"
)

func TestContentGeneratedBindings(t *testing.T) {
	opt := evmbind.Options{
		Types:     []reflect.Type{reflect.TypeFor[contentfixture.Envelope]()},
		Imports:   map[string]*evmbind.Schema{"github.com/andreyvit/evm/gox": gox.Schema()},
		Functions: map[string]any{"Echo": contentfixture.Echo, "Wrap": contentfixture.Wrap},
	}
	generated, err := evmbind.GenerateTypesWithOptions("host", "github.com/andreyvit/evm/gox/internal/contentfixture", "contentbindings", contentfixture.Source, []string{"Envelope"}, opt)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := os.ReadFile("internal/contentbindings/native_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, checked) {
		t.Fatal("regenerate content bindings")
	}
	opt.Static = &contentbindings.Bindings
	opt.Mode = evmbind.StaticOnly
	schema, err := evmbind.NewTypes("host", contentfixture.Source, []string{"Envelope"}, opt)
	if err != nil {
		t.Fatal(err)
	}
	pkg := schema.Bind(nil)
	tmpl, err := gox.Compile("native.gox", `package plugin
import "host"
func Page(b *gox.Builder) {
 text:=gox.Text("<plain>")
 if host.Echo(text)!=text || host.Wrap(text).Title!=text {panic("round trip")}
 rich:=gox.TrustedHTML(gox.HTML("<b>trusted</b>"))
 <p>host.Wrap(rich).Title</p>
}`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link(pkg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Render(context.Background(), nil, gox.RenderOptions{})
	if err != nil || got != "<p><b>trusted</b></p>" {
		t.Fatalf("%q: %v", got, err)
	}
}
