package gox_test

import (
	"context"
	"github.com/andreyvit/evm/gox"
	"strings"
	"testing"
)

func TestImplicitAndOptionalArguments_share_call_rules(t *testing.T) {
	source := `package plugin

import (
	"strconv"

	"github.com/andreyvit/evm/gox"
)

type Context struct { Name string }
//gox:arg value optional
func Pointer(b *gox.Builder, value *int) { <i>strconv.FormatBool(value == nil)</i> }
//gox:arg suffix optional
//gox:arg enabled optional
func Card(b *gox.Builder, ctx *Context, label string, suffix string, enabled gox.Optional[bool]) { <p>ctx.Name;label;suffix;strconv.FormatBool(enabled.Present);strconv.FormatBool(enabled.Value)</p> }
//gox:arg label implicit
//gox:arg suffix optional
func Text(label, suffix string) string { return label+suffix }
func Page(b *gox.Builder) {
 ctx:=&Context{Name:"C"}
 label:="local"
 text:=Text()
 <div>
  Card("A")
  <Card label="A" />
  Card("B", "!", false)
  <Card label="B" suffix="!" enabled={false} />
  Pointer()
  Pointer(3)
  <Pointer />
  <Pointer value={3} />
  text
 </div>
}`
	tmpl, err := (gox.Options{ImplicitTypes: []string{"*plugin.Context"}}).CompileSources(map[string]string{"page.gox": source})
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.RenderFunc(context.Background(), "Page", nil, gox.RenderOptions{})
	want := "<div><p>CAfalsefalse</p><p>CAfalsefalse</p><p>CB!truefalse</p><p>CB!truefalse</p><i>true</i><i>false</i><i>true</i><i>false</i>local</div>"
	if err != nil || got != want {
		t.Fatalf("%q: %v", got, err)
	}
	if !p.ParametersFor("Text")[0].Implicit {
		t.Fatal("missing implicit metadata")
	}
}

func TestImplicitArguments_require_permission(t *testing.T) {
	for _, call := range []string{`Card()`, `<Card />`} {
		source := `package plugin

import "github.com/andreyvit/evm/gox"

func Card(b *gox.Builder,label string){<p>label</p>}
func Page(b *gox.Builder){ label:="must not inherit";_ = label;<div>` + call + `</div>}`
		_, err := gox.CompileSources(map[string]string{"page.gox": source})
		if err == nil {
			t.Fatal("inherited unmarked label", call)
		}
	}
	source := `package plugin

import "github.com/andreyvit/evm/gox"

//gox:arg label implicit=false
func Card(b *gox.Builder,label string){<p>label</p>}
func Page(b *gox.Builder){label:="no";_ = label;<Card />}`
	_, err := gox.CompileSources(map[string]string{"page.gox": source})
	if err == nil || !strings.Contains(err.Error(), "label") {
		t.Fatal(err)
	}
}
