package gox_test

import (
	"context"
	"errors"
	"html/template"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/gox"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/kvo/codec/row2"
	"github.com/andreyvit/evm/values"
)

func TestGoXPreparationAndHelper(t *testing.T) {
	source := `package plugin

import (
	"fmt"
	"strconv"

	"github.com/andreyvit/evm/gox"
)

func Page(b *gox.Builder,
 name string,
 count int,
) {
 title := heading(count)
 <section>
  <h1>{title}</h1>
  <p>
   "Hello, "
   name
   "!"
  </p>
  for i := 0; (i < count); i++ {
   <span>{strconv.Itoa(i)}</span>
  }
 </section>
}
func heading(n int) string { return fmt.Sprintf("Customers: %d", n) }
`
	tmpl, err := gox.Compile("customers.gox", source)
	if err != nil {
		t.Fatal(err)
	}
	program, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := program.Render(context.Background(), []values.Value{values.String("<Alice>"), values.Int(2)}, gox.RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := `<section><h1>Customers: 2</h1><p>Hello, &lt;Alice&gt;!</p><span>0</span><span>1</span></section>`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func render(t *testing.T, source string, args ...values.Value) string {
	t.Helper()
	tmpl, err := gox.Compile("test.gox", source)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Render(context.Background(), args, gox.RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return got
}
func TestContextualEscaping(t *testing.T) {
	source := `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, value string) {
 <a href={value} title={value}>{value}</a>
 <img srcset={value}>
 <div style="color: {value}"></div>
 <script>const v = {=value}; const s = "{=value}"; const r = /{=value}/; const t = ` + "`" + `{=value}` + "`" + `;</script>
 <style>.x { color: {=value}; background: url('{=value}'); }</style>
 <textarea>{value}</textarea><title>{value}</title>
 <a href="/search?q={value}"></a>
 <button onclick="return f({value})"></button>
 <meta http-equiv="refresh" content="0; url={value}">
 }`
	cases := []string{`javascript:alert(1)`, `</script><script>alert(1)</script>`, `" onmouseover="alert(1)`, "${alert(1)}", "O'Reilly & <x>", "\x00"}
	for _, value := range cases {
		got := render(t, source, values.String(value))
		reference := `<a href="{{.}}" title="{{.}}">{{.}}</a><img srcset="{{.}}"><div style="color: {{.}}"></div><script>const v = {{.}}; const s = "{{.}}"; const r = /{{.}}/; const t = ` + "`" + `{{.}}` + "`" + `;</script><style>.x { color: {{.}}; background: url('{{.}}'); }</style>`
		reference += `<textarea>{{.}}</textarea><title>{{.}}</title><a href="/search?q={{.}}"></a><button onclick="return f({{.}})"></button><meta http-equiv="refresh" content="0; url={{.}}">`
		tmpl, err := template.New("reference").Parse(reference)
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		if err = tmpl.Execute(&b, value); err != nil {
			t.Fatal(err)
		}
		if got != b.String() {
			t.Errorf("%q:\ngot  %s\nwant %s", value, got, b.String())
		}
	}
}
func TestNilOutputAndStrict(t *testing.T) {
	source := `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
 var customer *Customer
 <p>
  "before"
  customer.Name
  "after"
 </p>
 }
 type Customer struct { Name string }
 `
	tmpl, err := gox.Compile("nil.gox", source)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics []gox.Diagnostic
	got, err := p.Render(context.Background(), nil, gox.RenderOptions{Report: func(d gox.Diagnostic) { diagnostics = append(diagnostics, d) }})
	if err != nil || got != "<p>beforeafter</p>" || len(diagnostics) != 1 {
		t.Fatalf("%q %v %v", got, err, diagnostics)
	}
	if diagnostics[0].Position.File != "nil.gox" || diagnostics[0].Position.Line != 9 {
		t.Fatalf("position: %+v", diagnostics)
	}
	_, err = p.Render(context.Background(), nil, gox.RenderOptions{Strict: true})
	if err == nil {
		t.Fatal("strict render succeeded")
	}
}
func TestAttributesFromHelper(t *testing.T) {
	source := `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, disabled bool) {
 <button class="base">
  decorate(disabled)
  "Save"
 </button>
 }
 func decorate(b *gox.Builder, disabled bool) {
  b.Class("button")
  b.Attr("aria-disabled", disabled)
  b.Attr("disabled", disabled)
 }
 `
	got := render(t, source, values.Bool(false))
	if got != `<button class="base button" aria-disabled="false">Save</button>` {
		t.Fatal(got)
	}
}
func TestRejectsBrokenStructureAndTypes(t *testing.T) {
	for _, source := range []string{
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {` + "\n" + `<div>` + "\n" + `if true {` + "\n" + `</div>` + "\n" + `} }`,
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {` + "\n" + `<p>{unknown}</p>` + "\n" + `}`,
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {` + "\n" + `<script type={"x"}>const x=1;</script>` + "\n" + `}`,
	} {
		_, err := gox.Compile("bad.gox", source)
		if err == nil {
			t.Errorf("accepted %s", source)
		}
	}
}

func TestComponentsSlotsAndImplicitInputs(t *testing.T) {
	sources := map[string]string{
		"page.gox": `package plugin

import (
	"fmt"
	"strconv"

	"github.com/andreyvit/evm/gox"
)

func Page(b *gox.Builder, page string) {
 <main>
  for i:=0;(i<2);i++ {
   <Wrapper label={fmt.Sprintf("item %d",i)}>
    <slot-body>
     page
     strconv.Itoa(i)
    </slot-body>
   </Wrapper>
  }
 </main>
 }`,
		"wrapper.gox": `package plugin

import "github.com/andreyvit/evm/gox"

//gox:arg page implicit
func Wrapper(b *gox.Builder, page string, label string, body func()) {
 <Button label={label}>
  <slot-body>
   body()
  </slot-body>
 </Button>
 }`,
		"button.gox": `package plugin

import "github.com/andreyvit/evm/gox"

//gox:arg page implicit
func Button(b *gox.Builder, page string,label string,body func()) {
 <button title={page}>
  label
  ": "
  body()
 </button>
 }`,
	}
	tmpl, err := gox.CompileFiles("page.gox", sources)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Render(context.Background(), []values.Value{values.String("Root")}, gox.RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := `<main><button title="Root">item 0: Root0</button><button title="Root">item 1: Root1</button></main>`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
func TestLocalAndPolymorphicComponents(t *testing.T) {
	source := `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, prefix string) {
 item := func(value string) {
  <b>
   prefix
   value
  </b>
 }
 chosen := item
 <div>
  <chosen value="one" />
  item("two")
 </div>
 }`
	got := render(t, source, values.String("@"))
	if got != `<div><b>@one</b><b>@two</b></div>` {
		t.Fatal(got)
	}
}
func TestLexicalAndWhitespaceBoundaries(t *testing.T) {
	source := `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
 text := ` + "`" + `
 <div>not markup</div>
 ` + "`" + `
 // <span>not markup</span>
 /*
 <em>not markup</em>
 */
 <pre>{text}</pre>
 <p>{"{"}</p>
 }`
	got := render(t, source)
	want := "<pre>\n &lt;div&gt;not markup&lt;/div&gt;\n </pre><p>{</p>"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRenderHelpersCanChangeImplementation(t *testing.T) {
	for _, helper := range []string{
		`func greeting(name string) gox.HTML { return gox.HTML("<b>" + name + "</b>") }`,
		`func greeting(b *gox.Builder,name string) { b.HTML(gox.HTML("<b>" + name + "</b>")) }`,
	} {
		source := `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, name string) {
 <p>
  greeting(name)
 </p>
 }
 ` + helper
		if got := render(t, source, values.String("Alice")); got != `<p><b>Alice</b></p>` {
			t.Fatal(got)
		}
	}
}
func TestControlFlowAndClasses(t *testing.T) {
	source := `package plugin

import (
	"strconv"

	"github.com/andreyvit/evm/gox"
)

func Page(b *gox.Builder) {
 <ul class="list">
  for i:=0;(i<4);i++ {
   if i==0 {continue}
   <li>
    b.Class("row active")
    if i==2 {continue}
    strconv.Itoa(i)
    if i==3 {break}
   </li>
  }
 </ul>
 }
 `
	tmpl, err := gox.Compile("loop.gox", source)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Render(context.Background(), nil, gox.RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got != `<ul class="list"><li class="row active">1</li><li class="row active"></li><li class="row active">3</li></ul>` {
		t.Fatal(got)
	}
	names := map[string]bool{}
	for _, cl := range tmpl.Classes {
		names[cl.Name] = true
	}
	if !names["list"] || !names["row"] || !names["active"] {
		t.Fatal(tmpl.Classes)
	}
	if got := render(t, `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
 <div />
 <p> {"ok"} </p>
 }`); got != `<div></div><p>ok</p>` {
		t.Fatal(got)
	}
}
func TestPipelinesAndTypedSlots(t *testing.T) {
	source := `package plugin

import (
	"strings"

	"github.com/andreyvit/evm/gox"
)

func Page(b *gox.Builder, name string) {
 <section>
  name | upper
  name | withPrefix("!")
  <rows>
   <slot-row>
    <b>{item}</b>
   </slot-row>
  </rows>
 </section>
 }
 func upper(s string)string{return strings.ToUpper(s)}
 func withPrefix(prefix, s string)string{return prefix+s}
 func rows(b *gox.Builder, row func(item string)) {
  <div>
   row("first")
   row("second")
  </div>
 }
 `
	got := render(t, source, values.String("alice"))
	if got != `<section>ALICE!alice<div><b>first</b><b>second</b></div></section>` {
		t.Fatal(got)
	}
}
func TestPipelinePlaceholder(t *testing.T) {
	source := `package plugin

import (
	"fmt"
	"strings"

	"github.com/andreyvit/evm/gox"
)

func Page(b *gox.Builder, name string, n int) {
 <p title="{n | fmt.Sprintf("%d items", _)}">
  name | strings.TrimPrefix(_, "al")
  name | strings.ReplaceAll(_, "a", "o") | strings.ToUpper
  n | add(_, 1) | fmt.Sprint
  n | fmt.Sprintf("%s has %d", name, _)
  name | join("<", _, ">")
 </p>
 }
 func add(a, b int) int { return a + b }
 func join(left, s, right string) string { return left + s + right }
 `
	got := render(t, source, values.String("alice"), values.Int(7))
	if want := `<p title="7 items">iceOLICE8alice has 7&lt;alice&gt;</p>`; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}

	_, err := gox.Compile("twice.gox", `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, name string) {
 name | join(_, _)
 }
 func join(a, b string) string { return a + b }
 `)
	if err == nil || !strings.Contains(err.Error(), "twice.gox:6:") || !strings.Contains(err.Error(), "cannot use _ as value") {
		t.Fatalf("want an error for two placeholders, got: %v", err)
	}
}
func TestKVOInputs(t *testing.T) {
	schema := kvo.NewSchema()
	entity := kvo.NewEntityType(schema, "customer")
	name := kvo.NewProp(schema, 1, "name", kvo.TMachineString, nil)
	model := kvo.NewModel(schema, entity, func(b *kvo.ModelBuilder) { b.Prop(schema.MustPropByCode(name)) })
	pkg := evm.Package{Path: "db", Records: []*evm.RecordType{evm.NewRecordType("Customer", model, false)}}
	tmpl, err := gox.Compile("record.gox", `package plugin

import (
	"db"

	"github.com/andreyvit/evm/gox"
)

func Page(b *gox.Builder, customer *db.Customer) {
 <p>{customer.name}</p>
 }`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link(pkg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, scope := evm.WithRecords(context.Background())
	defer scope.Close()
	edit := row2.Edit(row2.Empty(row2.Compact2))
	edit.Set([]row2.Step{{Key: name}}, values.Bytes([]byte("<Alice>")), row2.Data)
	got, err := p.Render(ctx, []values.Value{scope.Read(model, edit.Snapshot())}, gox.RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got != `<p>&lt;Alice&gt;</p>` {
		t.Fatal(got)
	}
}
func TestLinkingLimitsAndConcurrency(t *testing.T) {
	pkg := evm.Package{Path: "host", Functions: []evm.ExternalFunction{{Name: "Label", Signature: evm.Signature{Results: []*values.Type{values.StringType}}}}}
	tmpl, err := gox.Compile("host.gox", `package plugin

import (
	"host"

	"github.com/andreyvit/evm/gox"
)

func Page(b *gox.Builder) {
 <p>{host.Label()}</p>
 }`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"one", "two"} {
		pkg.Functions[0].Call = func(c *evm.Call) error { c.SetResult(0, values.String(label)); return nil }
		p, err := tmpl.Link(pkg)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got, err := p.Render(context.Background(), nil, gox.RenderOptions{})
				if err != nil || got != "<p>"+label+"</p>" {
					t.Errorf("%s %v", got, err)
				}
			}()
		}
		wg.Wait()
		_, err = p.Render(context.Background(), nil, gox.RenderOptions{MaxOutputBytes: 3})
		if !errors.Is(err, evm.ErrOutput) {
			t.Fatalf("limit: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = p.Render(ctx, nil, gox.RenderOptions{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	}
}
func BenchmarkRender(b *testing.B) {
	source := `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, rows []string) {
 <ul>
  for _, name := range rows {
   <li class="row">
    name
   </li>
  }
 </ul>
 }`
	tmpl, err := gox.Compile("bench.gox", source)
	if err != nil {
		b.Fatal(err)
	}
	program, err := tmpl.Link()
	if err != nil {
		b.Fatal(err)
	}
	helperTemplate, err := gox.Compile("helper.gox", strings.ReplaceAll(source, `<li class="row">`, "<li>\n b.Class(\"row\")"))
	if err != nil {
		b.Fatal(err)
	}
	helperProgram, err := helperTemplate.Link()
	if err != nil {
		b.Fatal(err)
	}
	old := template.Must(template.New("old").Parse(`<ul>{{range .Rows}}<li class="row">{{.}}</li>{{end}}</ul>`))
	rows := make([]string, 100)
	for i := range rows {
		rows[i] = "Alice & Bob"
	}
	data := struct{ Rows []string }{rows}
	cells := make([]values.Value, len(rows))
	for i, row := range rows {
		cells[i] = values.String(row)
	}
	args := []values.Value{values.SliceValue(unsafe.Pointer(unsafe.SliceData(cells)), uint32(len(cells)), uint32(len(cells)))}
	b.Run("GoX", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			_, err := program.Render(context.Background(), args, gox.RenderOptions{})
			if err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("GoTemplate", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			var out strings.Builder
			if err := old.Execute(&out, data); err != nil {
				b.Fatal(err)
			}
			_ = out.String()
		}
	})
	b.Run("GoXClassHelper", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := helperProgram.Render(context.Background(), args, gox.RenderOptions{}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func TestInlineContextsNilAndSourceErrors(t *testing.T) {
	source := `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
 var customer *Customer
 <a title="prefix {customer.Name} suffix">{"ok"}</a>
 <script>const name={=customer.Name};const text="{=customer.Name}";</script>
 }
 type Customer struct{Name string}`
	got := render(t, source)
	if got != `<a>ok</a><script>const name= null ;const text="";</script>` {
		t.Fatal(got)
	}
	source = `package plugin

import (
	"strconv"

	"github.com/andreyvit/evm/gox"
)

// Header comment
 func Page(b *gox.Builder,
  n int,
 ) {
 <div>{strconv.Itoa(100 / n)}</div>
 }`
	tmpl, err := gox.Compile("division.gox", source)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Render(context.Background(), []values.Value{values.Int(0)}, gox.RenderOptions{})
	if !errors.Is(err, evm.ErrDivideByZero) || !strings.Contains(err.Error(), "division.gox:13") {
		t.Log(tmpl.GeneratedSource)
		var execution *evm.ExecutionError
		if errors.As(err, &execution) {
			t.Logf("position: %+v", execution.Position)
		}
		t.Fatal(err)
	}
}
func TestLocalShadowsPackageNameAndTypeSwitch(t *testing.T) {
	source := `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
 fmt := struct{Title string}{"local"}
 var value any = "hello"
 <div>
  fmt.Title
  switch v := value.(type) {
  case string:
   <b>{v}</b>
  default:
   "other"
  }
 </div>
 }`
	if got := render(t, source); got != `<div>local<b>hello</b></div>` {
		t.Fatal(got)
	}
}

func TestTextAndCSSFormats(t *testing.T) {
	text, err := (gox.Options{Format: gox.TextFormat}).Compile("message.gox", `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, name string) {
 "Hello, "
 name
 "!\n"
 }`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := text.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Render(context.Background(), []values.Value{values.String("<Alice>")}, gox.RenderOptions{})
	if err != nil || got != "Hello, <Alice>!\n" {
		t.Fatalf("%q %v", got, err)
	}
	css, err := (gox.Options{Format: gox.CSSFormat}).Compile("theme.gox", `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, color string) {
 ".button { color: "
 color
 "; }"
 }`)
	if err != nil {
		t.Fatal(err)
	}
	p, err = css.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err = p.Render(context.Background(), []values.Value{values.String("expression(alert(1))")}, gox.RenderOptions{})
	if err != nil || got != ".button { color: ZgotmplZ; }" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestNilGuardsPreserveEvaluationOrder(t *testing.T) {
	source := `package plugin

import (
	"fmt"
	"strconv"

	"github.com/andreyvit/evm/gox"
)

func Page(b *gox.Builder) {
 var customer *Customer
 calls:=0
 next:=func() *Customer {calls++;return nil}
 <div>
  fmt.Sprintf("name: %s",next().Name)
  strconv.FormatBool(false && next().Active)
  strconv.FormatBool(true || next().Active)
  strconv.FormatBool(customer == nil || customer.Active)
  strconv.Itoa(calls)
 </div>
 }
 type Customer struct{Name string;Active bool}`
	got := render(t, source)
	if got != `<div>falsetruetrue1</div>` {
		t.Fatal(got)
	}
	got = render(t, `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
 var customer *Customer
 <div>
  greet(customer.Name)
  "after"
 </div>
 }
 type Customer struct{Name string}
 func greet(b *gox.Builder,name string) { b.Text(name) }`)
	if got != `<div>after</div>` {
		t.Fatal(got)
	}
}

func TestBuilderHelperEscapingAndTagLifetime(t *testing.T) {
	source := `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, name, url string) {
 <div>
  link(name,url)
 </div>
 }
 func link(b *gox.Builder,name,url string) {
  b.Open("a")
  b.Attr("href",url)
  b.Text(name)
  b.Close()
 }`
	if got := render(t, source, values.String("<Alice>"), values.String("javascript:alert(1)")); got != `<div><a href="#ZgotmplZ">&lt;Alice&gt;</a></div>` {
		t.Fatal(got)
	}
	got := render(t, `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
 <button disabled>
  b.Attr("disabled",false)
  icon()
 </button>
 }
 func icon(b *gox.Builder) {
  b.Open("img")
  b.Attr("src","https://example.com/icon.png")
 }`)
	if got != `<button><img src="https://example.com/icon.png"></button>` {
		t.Fatal(got)
	}
	tmpl, err := gox.Compile("late.gox", `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
 <p>
  "text"
  b.Class("late")
 </p>
 }`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Render(context.Background(), nil, gox.RenderOptions{}); err == nil {
		t.Fatal("late attribute accepted")
	}
}

func FuzzCompileGoX(f *testing.F) {
	for _, source := range []string{
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {}`, `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {` + "\n" + `<p>{"hello"}</p>` + "\n" + `}`, `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, x *X) {` + "\n" + `<p>{x.Name}</p>` + "\n" + `} type X struct{Name string}`,
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {` + "\n" + `<Unknown />` + "\n" + `}`, `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, x int) {` + "\n" + `<div>{x + "bad"}</div>` + "\n" + `}`,
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {` + "\n" + `<script>const x={=unknown};</script>` + "\n" + `}`,
	} {
		f.Add(source)
	}
	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 2048 {
			t.Skip()
		}
		_, _ = gox.Compile("fuzz.gox", source)
	})
}

func TestRenderingEffectsCannotBypassContextualEscaping(t *testing.T) {
	for _, source := range []string{
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, payload string) {
 <script>{=hidden(payload)}</script>
 }
 func hidden(b *gox.Builder,payload string)int {b.Text(payload);return 0}`,
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, payload string) {
 <a title={hidden(payload)}>{"ok"}</a>
 }
 func hidden(b *gox.Builder,payload string)string {b.Text(payload);return "value"}`,
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, payload string) {
 body := func() {
  <p>{payload}</p>
 }
 <script>{=invoke(body)}</script>
 }
 func invoke(body func())int {body();return 0}`,
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, payload string) {
 <host>
  <slot-body>
   payload
  </slot-body>
 </host>
 }
 func host(b *gox.Builder, body func()) {
  <script>{=invoke(body)}</script>
 }
 func invoke(body func())int {body();return 0}`,
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, payload string) {
 <host>
  <slot-body>
   "alert(1);"
  </slot-body>
 </host>
 }
 func host(b *gox.Builder, body func()) {
  <script>{=invoke(body)}</script>
 }
 func invoke(body func())int {body();return 0}`,
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, payload string) {
 <host>
  <slot-body>
   payload
  </slot-body>
 </host>
 }
 func host(b *gox.Builder, body func()) {
  <a title={invoke(body)}>{"ok"}</a>
 }
 func invoke(body func())int {body();return 0}`,
	} {
		tmpl, err := gox.Compile("effect.gox", source)
		if err != nil {
			t.Fatal(err)
		}
		p, err := tmpl.Link()
		if err != nil {
			t.Fatal(err)
		}
		got, err := p.Render(context.Background(), []values.Value{values.String("alert(1);")}, gox.RenderOptions{})
		if err == nil || got != "" {
			t.Fatalf("unsafe rendering effect: %q %v", got, err)
		}
	}
}

func TestRenderingClosureCannotReturnValue(t *testing.T) {
	_, err := gox.Compile("effect.gox", `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
 body := func()int {
  <p>{"unsafe"}</p>
  return 0
 }
 <script>{=body()}</script>
 }`)
	if err == nil || !strings.Contains(err.Error(), "rendering functions must not return values") {
		t.Fatal(err)
	}
}

func TestUnicodeAndNativeScriptDelimiters(t *testing.T) {
	source := `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
 <p title={"Привет 🌍"}>{"Здравствуйте"}</p>
 <script>const k="K";const s={="</script>"};const z="</scriptish>";</SCRIPT>
 <style />
 <textarea />
 <p>{"done"}</p>
 }`
	got := render(t, source)
	want := `<p title="Привет 🌍">Здравствуйте</p><script>const k="K";const s="\u003c/script\u003e";const z="\x3C/scriptish>";</script><style></style><textarea></textarea><p>done</p>`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestStaticClassInventory(t *testing.T) {
	tmpl, err := gox.Compile("classes.gox", `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, tone string) {
 const spacing = "px-4 " + "py-2"
 <div class="base bg-{tone} {"rounded"} suffix">
  b.Class(spacing)
  b.Attr("class", "font-bold")
 </div>
 }`)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, class := range tmpl.Classes {
		seen[class.Name] = true
	}
	for _, name := range []string{"base", "rounded", "suffix", "px-4", "py-2", "font-bold"} {
		if !seen[name] {
			t.Errorf("missing %s: %v", name, tmpl.Classes)
		}
	}
	if len(seen) != 6 || !tmpl.DynamicClasses {
		t.Fatalf("%v dynamic=%v", tmpl.Classes, tmpl.DynamicClasses)
	}
	tmpl, err = gox.Compile("static.gox", `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
 <div>
  b.Class("static")
 </div>
 }`)
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.DynamicClasses {
		t.Fatal("synthetic helper marked static template dynamic")
	}
}

func TestRootComponentReturningHTML(t *testing.T) {
	got := render(t, `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
 <hello />
 }
 func hello() gox.HTML { return gox.HTML("<p>hello</p>") }`)
	if got != "<p>hello</p>" {
		t.Fatal(got)
	}
}

func TestNestedAndEmptySlots(t *testing.T) {
	got := render(t, `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
 <panel>
  <slot-body>
   <panel>
    <slot-body>
     "inner"
    </slot-body>
   </panel>
  </slot-body>
 </panel>
 <panel><slot-body /></panel>
 }
 func panel(b *gox.Builder, body func()) {
  <section>
   body()
  </section>
 }`)
	if got != `<section><section>inner</section></section><section></section>` {
		t.Fatal(got)
	}
}

func TestRendererFunctionsAsValues(t *testing.T) {
	sources := map[string]string{
		"page.gox": `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, title string) {
 chosen := Button
 <Wrapper component={chosen} title={title} />
 <div>
  Button(title)
 </div>
 }`,
		"button.gox": `package plugin

import "github.com/andreyvit/evm/gox"

func Button(b *gox.Builder, title string) {
 <button>{title}</button>
 }`,
		"wrapper.gox": `package plugin

import "github.com/andreyvit/evm/gox"

func Wrapper(b *gox.Builder, component func(b *gox.Builder,title string),title string) {
 <section>
  <component title={title} />
 </section>
 }`,
	}
	tmpl, err := gox.CompileFiles("page.gox", sources)
	if err != nil {
		t.Fatal(err)
	}
	program, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := program.Render(context.Background(), []values.Value{values.String("Save")}, gox.RenderOptions{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	if got != `<section><button>Save</button></section><div><button>Save</button></div>` {
		t.Fatal(got)
	}
}

func TestRetainedRenderer(t *testing.T) {
	tmpl, err := gox.CompileFiles("page.gox", map[string]string{
		"page.gox": `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, title string) {
 <p>{title}</p>
 }`,
		"main.go": `package plugin
import "github.com/andreyvit/evm/gox"
func Choose(title string)func(*gox.Builder){return func(b *gox.Builder){Page(b,title)}}
func Invalid()func()int{return func()int{return 1}}
`,
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	vm := evm.New(p.EVM())
	if err := vm.Run(context.Background(), "Choose", []values.Value{values.String("<hello>")}, out[:]); err != nil {
		t.Fatal(err)
	}
	fn, err := p.EVM().FunctionValue(out[0])
	if err != nil {
		t.Fatal(err)
	}
	if gox.ProgramOf(fn.Program()) != p {
		t.Fatal("callable lost GoX runtime")
	}
	second, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	if gox.ProgramOf(second.EVM()) != second || second == p {
		t.Fatal("links share GoX runtime")
	}
	plainModule, err := evmcompiler.Compile("plain.go", "package plugin\nfunc Main() {}")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := evm.Link(plainModule)
	if err != nil {
		t.Fatal(err)
	}
	if gox.ProgramOf(plain) != nil {
		t.Fatal("plain EVM acquired GoX metadata")
	}

	if err := vm.Reset(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		got, err := p.RenderFunction(context.Background(), fn, nil, gox.RenderOptions{Strict: true})
		if err != nil || got != "<p>&lt;hello&gt;</p>" {
			t.Fatalf("%s %v", got, err)
		}
	}
	if _, err := p.RenderFunction(context.Background(), fn, nil, gox.RenderOptions{MaxOutputBytes: 2, Strict: true}); err == nil {
		t.Fatal("output limit ignored")
	}
	got, err := p.RenderFunction(context.Background(), fn, nil, gox.RenderOptions{Strict: true})
	if err != nil || got != "<p>&lt;hello&gt;</p>" {
		t.Fatalf("after failure: %s %v", got, err)
	}
	if err := vm.Run(context.Background(), "Invalid", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	invalid, err := p.EVM().FunctionValue(out[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.RenderFunction(context.Background(), invalid, nil, gox.RenderOptions{}); err == nil {
		t.Fatal("invalid renderer accepted")
	}
	other, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.RenderFunction(context.Background(), fn, nil, gox.RenderOptions{}); !errors.Is(err, evm.ErrFunction) {
		t.Fatal(err)
	}
	if _, err := p.RenderFunction(context.Background(), nil, nil, gox.RenderOptions{}); !errors.Is(err, evm.ErrNilFunction) {
		t.Fatal(err)
	}
}

func TestMarkupIndependentOfLines(t *testing.T) {
	for _, body := range []string{`<p>name<strong>"VIP"</strong></p>`, "<p>\nname\n<strong>\n\"VIP\"\n</strong>\n</p>"} {
		if got := render(t, "package plugin\n\nimport \"github.com/andreyvit/evm/gox\"\n\nfunc Page(b *gox.Builder, name string) {"+body+"}", values.String("<Alice>")); got != "<p>&lt;Alice&gt;<strong>VIP</strong></p>" {
			t.Fatal(got)
		}
	}
}

func TestMarkupLexicalModes(t *testing.T) {
	// Pipelines keep the bare comparison and shift forms at the start of an
	// output expression.
	tmpl, err := gox.Compile("test.gox", `package plugin

import (
	"strconv"

	"github.com/andreyvit/evm/gox"
)

func Page(b *gox.Builder) {
 call := func(f func()) { f() }
 <section>
 call(func() { <p>(1 < 2) | strconv.FormatBool</p> })
 for i := 0; (i < 2); i++ { <i>i | strconv.Itoa</i> }
 <b title={1 < 2}>([]string{"no", "yes"}[1])</b>
 <em>1 << 2 | strconv.Itoa</em><em>(1 <= 2) | strconv.FormatBool</em>
 <span>"<fake>" /* </fake> */</span>
 <>
 "tail" // <fake>
 </>
 </section>
}`)
	if err != nil {
		t.Fatal(err)
	}
	program, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := program.Render(context.Background(), nil, gox.RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := `<section><p>true</p><i>0</i><i>1</i><b title="true">yes</b><em>4</em><em>true</em><span>&lt;fake&gt;</span>tail</section>`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	for _, body := range []string{`<p>1 < 2</p>`, `<p>"x"`, `<p>(1 < 2]</p>`, `<p>unknown</p>`} {
		if _, err := gox.Compile("bad.gox", "package plugin\n\nimport \"github.com/andreyvit/evm/gox\"\n\nfunc Page(b *gox.Builder) {"+body+"}"); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}
