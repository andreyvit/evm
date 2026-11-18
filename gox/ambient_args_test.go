package gox_test

import (
	"context"
	"strings"
	"testing"

	"github.com/andreyvit/evm/gox"
)

func TestAmbientArguments(t *testing.T) {
	source := `package plugin
//gox:arg color ambient="color"
func Badge(b *gox.Builder, label string, color string, ambient *evmruntime.Ambient) {
 <p>label;color;ambient.Get("other")</p>
}
func Page(b *gox.Builder) {
 calls:=0
 next:=func()string{calls++;return "blue"}
 var ambient *evmruntime.Ambient
 ambient = ambient.CloneWith(map[string]any{"color":"red", "other":"!"})
 <Badge label="XML" />
 <Badge label="Child" ambient:color={next()} />
 <div>Badge("Go")</div>
 <p>ambient.Get("color");calls</p>
}`
	tmpl, err := gox.Compile("ambient.gox", source)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.RenderFunc(context.Background(), "Page", nil, gox.RenderOptions{})
	if err != nil || got != "<p>XMLred!</p><p>Childblue!</p><div><p>Gored!</p></div><p>red1</p>" {
		t.Fatalf("%q: %v", got, err)
	}
	if p.ParametersFor("Badge")[1].Ambient != "color" {
		t.Fatal("ambient metadata missing")
	}
	for _, call := range []string{`<Badge label="x" color="no" />`, `Badge(b,"x","no",ambient)`} {
		_, err := gox.Compile("bad.gox", strings.Replace(source, `<Badge label="XML" />`, call, 1))
		if err == nil || !strings.Contains(err.Error(), "cannot be passed explicitly") {
			t.Fatalf("%s: %v", call, err)
		}
	}
}

func TestAmbientArguments_missing_and_invalid(t *testing.T) {
	source := `package plugin
//gox:arg count ambient="count"
func Number(b *gox.Builder,count int,ambient *evmruntime.Ambient){<p>count</p>}
func Page(b *gox.Builder){var ambient *evmruntime.Ambient;<Number />}`
	tmpl, err := gox.Compile("ambient.gox", source)
	if err != nil {
		t.Fatal(err)
	}
	program, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := program.RenderFunc(context.Background(), "Page", nil, gox.RenderOptions{})
	if err != nil || got != "<p>0</p>" {
		t.Fatalf("%q: %v", got, err)
	}
	for _, source := range []string{
		"package plugin\n//gox:arg count ambient=\"count\"\nfunc Page(b *gox.Builder,count int){<p>count</p>}",
		"package plugin\n//gox:arg count ambient=\"count\" optional\nfunc Page(b *gox.Builder,count int,ambient *evmruntime.Ambient){<p>count</p>}",
		strings.Replace(source, "<Number />", "<Number ambient:count=\"wrong type\" />", 1),
	} {
		tmpl, err := gox.Compile("bad.gox", source)
		if err == nil {
			program, e := tmpl.Link()
			if e != nil {
				err = e
			} else {
				_, err = program.RenderFunc(context.Background(), "Page", nil, gox.RenderOptions{})
			}
		}
		if err == nil {
			t.Fatalf("accepted invalid ambient binding: %s", source)
		}
	}
}
