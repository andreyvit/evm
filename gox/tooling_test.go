package gox_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/gox"
	"github.com/andreyvit/evm/values"
)

func TestPreprocessAndCompile(t *testing.T) {
	const source = `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, name string) { <p>label(name)</p> }
func label(name string) string { return name }`
	result, err := gox.Preprocess("page.gox", source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.GeneratedSources["page.gox.go"], "label(name)") {
		t.Fatal(result.GeneratedSources)
	}
	tmpl, err := result.Compile()
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Render(context.Background(), []values.Value{values.String("A&B")}, gox.RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "<p>A&amp;B</p>" {
		t.Fatal(got)
	}
	if _, err := gox.Preprocess("bad.gox", `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) { <p>missing()</p> }`); err == nil {
		t.Fatal("accepted unresolved call")
	}
}

func TestSyntaxAndFormatting(t *testing.T) {
	source := "package plugin\nfunc Page(b *gox.Builder) {<p> unknown.Name </p>}"
	if err := gox.CheckSyntax("page.gox", source); err != nil {
		t.Fatal(err)
	}
	got, err := gox.FormatSource("page.gox", source)
	if err != nil {
		t.Fatal(err)
	}
	want := "package plugin\n\nimport \"github.com/andreyvit/evm/gox\"\n\nfunc Page(b *gox.Builder) {\n\t<p>unknown.Name</p>\n}\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	again, err := gox.FormatSource("page.gox", got)
	if err != nil || again != got {
		t.Fatalf("not idempotent: %v\n%s", err, again)
	}
	for _, source := range []string{`package plugin
func Page() { <p>"x"</div> }`, `package plugin
func Page() { <p> }`, `package plugin
func Page() { <p>{1 +}</p> }`} {
		if err := gox.CheckSyntax("bad.gox", source); err == nil {
			t.Fatalf("accepted %s", source)
		}
	}
}

func TestFormattingLayouts(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"merge", "<p>\nname\n</p>", "\t<p>name</p>"},
		{"multiple values", `<p>"hello"; name</p>`, "\t<p>\n\t\t\"hello\"\n\t\tname\n\t</p>"},
		{"nested", "<div>\n<p>name</p>\n</div>", "\t<div><p>name</p></div>"},
		{"single medium label", `<p>"a medium sized label that stays inline within the line width"</p>`, "\t<p>\"a medium sized label that stays inline within the line width\"</p>"},
		{"merge medium label", "<p>\n\"a medium sized label that stays inline within the line width\"\n</p>", "\t<p>\"a medium sized label that stays inline within the line width\"</p>"},
		{"split", `<p>"a very long label which exceeds the maximum line width even with the wider formatting limit and must move onto a line of its own"</p>`, "\t<p>\n\t\t\"a very long label which exceeds the maximum line width even with the wider formatting limit and must move onto a line of its own\"\n\t</p>"},
		{"attributes", `<Button label="a very long label which exceeds the maximum line width even with the wider formatting limit" value={x+1}/>`, "\t<Button\n\t\tlabel=\"a very long label which exceeds the maximum line width even with the wider formatting limit\"\n\t\tvalue={x + 1}\n\t/>"},
		{"attribute merge", "<Button\nlabel={ x+1 }\n/>", "\t<Button label={x + 1} />"},
		{"component attribute merge", "<Button\nlabel=\"a medium sized label\"\n/>", "\t<Button label=\"a medium sized label\" />"},
		{"comparison", `<p>if (a < b) { "yes" }</p>`, "\t<p>\n\t\tif (a < b) {\n\t\t\t\"yes\"\n\t\t}\n\t</p>"},
		{"literal", "<pre>`a\n  b\nc`</pre>", "\t<pre>\n\t\t`a\n  b\nc`\n\t</pre>"},
		{"raw", "<script>const x = {=data};\n  // preserve\nfoo(x)</script>", "\t<script>const x = {=data};\n  // preserve\nfoo(x)</script>"},
		{"comment", "<p>// label\nname</p>", "\t<p> // label\n\t\tname\n\t</p>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := gox.FormatSource("page.gox", "package plugin\nfunc Page(b *gox.Builder) {"+c.body+"}")
			if err != nil {
				t.Fatal(err)
			}
			want := "package plugin\n\nimport \"github.com/andreyvit/evm/gox\"\n\nfunc Page(b *gox.Builder) {\n" + c.want + "\n}\n"
			if got != want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, want)
			}
			again, err := gox.FormatSource("page.gox", got)
			if err != nil || again != got {
				t.Fatalf("not idempotent: %v\n%s", err, again)
			}
		})
	}
}

func TestFormatFixtures(t *testing.T) {
	names, err := filepath.Glob("testdata/*/*.gox")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			got, err := gox.FormatSource(name, string(data))
			if err != nil {
				t.Fatal(err)
			}
			again, err := gox.FormatSource(name, got)
			if err != nil || again != got {
				t.Fatalf("not idempotent: %v\nfirst:\n%s\nsecond:\n%s", err, got, again)
			}
		})
	}
}

func TestFormattingPreservesRendering(t *testing.T) {
	const source = "package plugin\n\nimport \"github.com/andreyvit/evm/gox\"\n\nfunc Page(b *gox.Builder, name string) {<div title={name /* important */ + \"!\"}><p>name // inline comment\n</p><pre>`first\n  second\nthird`</pre><script>const x = {=name};\n  console.log(x)</script>for i:=0;(i<2);i++ {<span>i</span>}</div>}"
	formatted, err := gox.FormatSource("page.gox", source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(formatted, "/* important */") {
		t.Fatal("attribute comment lost:", formatted)
	}
	var want string
	for _, s := range []string{source, formatted} {
		tmpl, err := gox.Compile("page.gox", s)
		if err != nil {
			t.Fatal(err)
		}
		p, err := tmpl.Link()
		if err != nil {
			t.Fatal(err)
		}
		got, err := p.Render(context.Background(), []values.Value{values.String("<Alice>")}, gox.RenderOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if want == "" {
			want = got
		} else if got != want {
			t.Fatalf("%q != %q", got, want)
		}
	}
	again, err := gox.FormatSource("page.gox", formatted)
	if err != nil || again != formatted {
		t.Fatalf("not idempotent: %v\n%s\n%s", err, formatted, again)
	}
}

func TestFormattingImports(t *testing.T) {
	source := `package plugin
import unused "math"
import s "strings"
func Page(b *gox.Builder, text string) { <ui.Card title={s.TrimSpace(text)}><p>fmt.Sprint(text)</p></ui.Card> }`
	got, err := gox.FormatSource("page.gox", source, evm.Package{Path: "example.com/ui"})
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{`"fmt"`, `s "strings"`, `"github.com/andreyvit/evm/gox"`, `"example.com/ui"`, `<ui.Card title={s.TrimSpace(text)}>`} {
		if !strings.Contains(got, part) {
			t.Fatalf("missing %s in %s", part, got)
		}
	}
	if strings.Contains(got, "math") {
		t.Fatal(got)
	}
	again, err := gox.FormatSource("page.gox", got, evm.Package{Path: "example.com/ui"})
	if err != nil || again != got {
		t.Fatalf("not idempotent: %v\n%s", err, again)
	}
	source = `package plugin
func Page(b *gox.Builder) { fmt:=struct{ Name string }{}; <p>fmt.Name</p> }`
	got, err = gox.FormatSource("page.gox", source)
	if err != nil || strings.Contains(got, `"fmt"`) {
		t.Fatalf("%v\n%s", err, got)
	}
}

func TestFormattingVoidComponentAndClosures(t *testing.T) {
	for _, source := range []string{
		`package plugin

import "github.com/andreyvit/evm/gox"

//go:build ignore

func Page(b *gox.Builder) { <p>"x"</p> }`,
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) { <div><input><br></div> }`,
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) { <input>"child"</input> }`,
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) { <div><>"one"; "two"</></div> }`,
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) { <div>func(){ <p>"one"</p> }()</div> }`,
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) { <Button label={func() string { return "x" }()} /> }`,
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) { <Button label={func() string {
 return "x"
 }()} /> }`,
	} {
		got, err := gox.FormatSource("page.gox", source)
		if err != nil {
			t.Fatalf("%v\n%s", err, source)
		}
		again, err := gox.FormatSource("page.gox", got)
		if err != nil || again != got {
			t.Fatalf("not idempotent: %v\n%s\n%s", err, got, again)
		}
	}
}

func TestFormattingWidthBoundaries(t *testing.T) {
	for _, c := range []struct {
		width              int
		single, wantSingle bool
	}{
		{79, false, true}, {80, false, true}, {80, true, true}, {81, true, false}, {81, false, false},
	} {
		content := `"` + strings.Repeat("x", c.width-13) + `"`
		body := "<p>" + content + "</p>"
		if !c.single {
			body = "<p>\n" + content + "\n</p>"
		}
		got, err := gox.FormatSource("page.gox", "package plugin\n\nimport \"github.com/andreyvit/evm/gox\"\n\nfunc Page(b *gox.Builder){"+body+"}")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, "<p>"+content+"</p>") != c.wantSingle {
			t.Fatalf("width %d single %v:\n%s", c.width, c.single, got)
		}
	}
}

func FuzzFormatSource(f *testing.F) {
	for _, source := range []string{
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder){<p>"Hello"</p>}`,
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder){<p title={name}>name; "!"</p>}`,
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder){<script>const x={=name};</script>}`,
		`package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder){for i:=0;(i<2);i++{<p>i</p>}}`,
	} {
		f.Add(source)
	}
	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 4096 {
			t.Skip()
		}
		got, err := gox.FormatSource("fuzz.gox", source)
		if err != nil {
			return
		}
		again, err := gox.FormatSource("fuzz.gox", got)
		if err != nil || again != got {
			t.Fatalf("not idempotent: %v\n%s\n%s", err, got, again)
		}
	})
}

func TestPreprocessWithoutBytecode(t *testing.T) {
	result, err := gox.PreprocessSources(map[string]string{"main.go": `package plugin
func channels() { ch:=make(chan int); _=ch }`})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.GeneratedSources) == 0 {
		t.Fatal("missing lowered sources")
	}
	if _, err := result.Compile(); err == nil {
		t.Fatal("expected unsupported EVM operation")
	}
	host := evm.Package{Path: "widgets", Functions: []evm.ExternalFunction{{Name: "Badge", ParamNames: []string{"name"}, Signature: evm.Signature{Params: []*values.Type{values.StringType}, Results: []*values.Type{values.StringType}}}}}
	result, err = gox.Preprocess("page.gox", `package plugin

import (
	"widgets"

	"github.com/andreyvit/evm/gox"
)

func Page(b *gox.Builder,name string){<p>widgets.Badge(name)</p>}`, host)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.GeneratedSources["page.gox.go"], "widgets.Badge(name)") {
		t.Fatal(result.GeneratedSources)
	}
}

func TestFormattingNestedComponentCalls(t *testing.T) {
	source := "package plugin\n\nimport \"github.com/andreyvit/evm/gox\"\n\nfunc Page(b *gox.Builder) {\n" + strings.Repeat("if true {\n", 8) + "<helpers.BlockClass\nelement=\"item-done\"\n/>\n" + strings.Repeat("}\n", 8) + "}"
	got, err := gox.FormatSource("page.gox", source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `<helpers.BlockClass element="item-done" />`) {
		t.Fatal(got)
	}
	again, err := gox.FormatSource("page.gox", got)
	if err != nil || again != got {
		t.Fatalf("unstable formatting: %v\n%s", err, again)
	}
}

func TestFormatLiteralStringAttributes(t *testing.T) {
	source := `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
 <Part key={"top"} />
 <input name={"receipt_file"} title={"A&B"} placeholder={"{name}"}>
}
func Part(b *gox.Builder, key string) { <div data-key={key}></div> }`
	formatted, err := gox.FormatSource("literal.gox", source)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`key="top"`, `name="receipt_file"`, `title={"A&B"}`, `placeholder={"{name}"}`} {
		if !strings.Contains(formatted, want) {
			t.Fatalf("missing %s in %s", want, formatted)
		}
	}
	again, err := gox.FormatSource("literal.gox", formatted)
	if err != nil || again != formatted {
		t.Fatalf("not idempotent: %s, %v", again, err)
	}
	var outputs []string
	for _, src := range []string{source, formatted} {
		tmpl, err := gox.Compile("literal.gox", src)
		if err != nil {
			t.Fatal(err)
		}
		p, err := tmpl.Link()
		if err != nil {
			t.Fatal(err)
		}
		output, err := p.Render(context.Background(), nil, gox.RenderOptions{})
		if err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, output)
	}
	if outputs[0] != outputs[1] {
		t.Fatalf("rendering changed: %q", outputs)
	}
}
