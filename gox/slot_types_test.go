package gox_test

import (
	"context"
	"testing"

	"github.com/andreyvit/evm/gox"
)

func TestSlot_local_record_parameter(t *testing.T) {
	tmpl, err := gox.Compile("page.gox", `package plugin
type Item struct { Name string }
func Page(b *gox.Builder) {
 <Rows><slot-row><p>item.Name</p></slot-row></Rows>
}
func Rows(b *gox.Builder, row func(item Item)) {
 <>row(Item{Name: "A&B"})</>
}`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Render(context.Background(), nil, gox.RenderOptions{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	if got != "<p>A&amp;B</p>" {
		t.Fatal(got)
	}
}
