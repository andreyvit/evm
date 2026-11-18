package gox_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/andreyvit/evm/gox"
)

func TestFrontmatter(t *testing.T) {
	const header = "---\ntitle: Example\n---\n"
	const body = "package demo\n\nimport \"github.com/andreyvit/evm/gox\"\n\nfunc Page(b *gox.Builder) { <p>{\"Hello\"}</p> }\nfunc Name() string { return \"demo\" }\n"
	calls := 0
	metadata := &struct{ Title string }{"Example"}
	options := gox.Options{ParseFrontmatter: func(raw []byte) (any, error) {
		calls++
		if string(raw) != "title: Example\n" {
			t.Fatalf("raw: %q", raw)
		}
		return metadata, nil
	}}
	tmpl, err := options.CompileSources(map[string]string{"page.gox": header + body, "other.gox": "package demo\nfunc Other() string { return \"other\" }"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("parser called %d times", calls)
	}
	program, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Page", "Name"} {
		if got, ok := tmpl.Frontmatter(name); !ok || got != metadata {
			t.Fatalf("template %s: %v, %v", name, got, ok)
		}
		if got, ok := program.Frontmatter(name); !ok || got != metadata {
			t.Fatalf("program %s: %v, %v", name, got, ok)
		}
	}
	for _, name := range []string{"Other", "missing"} {
		if _, ok := program.Frontmatter(name); ok {
			t.Fatalf("unexpected metadata for %s", name)
		}
	}
	if err := options.CheckSyntax("page.gox", header+body); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{header + body, "---\n---\n" + body} {
		if _, err := gox.Compile("page.gox", source); err == nil || !strings.Contains(err.Error(), "frontmatter requires a host parser") {
			t.Fatalf("no parser: %v", err)
		}
	}
	failing := gox.Options{ParseFrontmatter: func([]byte) (any, error) { return nil, errors.New("bad metadata") }}
	if _, err := failing.Compile("page.gox", header+"invalid go"); err == nil || !strings.Contains(err.Error(), "page.gox:2:1: invalid frontmatter: bad metadata") {
		t.Fatalf("parser error: %v", err)
	}
	if _, err := options.Compile("page.gox", header+"package plugin\n\nimport \"github.com/andreyvit/evm/gox\"\n\nfunc Page(b *gox.Builder) {\n<p>{missing}</p>\n}"); err == nil || !strings.Contains(err.Error(), "page.gox:9:") {
		t.Fatalf("source position: %v", err)
	}
	nilParser := gox.Options{ParseFrontmatter: func([]byte) (any, error) { return nil, nil }}
	tmpl, err = nilParser.Compile("page.gox", "---\n---\n"+body)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := tmpl.Frontmatter("Page"); got != nil || !ok {
		t.Fatalf("nil metadata: %v %v", got, ok)
	}
}

func TestSplitFrontmatter(t *testing.T) {
	for _, tc := range []struct {
		source, raw, body string
		present, invalid  bool
	}{
		{source: "package plugin\nfunc F() {}", body: "package plugin\nfunc F() {}"},
		{source: "---\na: b\n---\nbody", raw: "a: b\n", body: "body", present: true},
		{source: "\ufeff---\r\na: b\r\n---\r\nbody", raw: "a: b\r\n", body: "body", present: true},
		{source: "---\n---", present: true},
		{source: "---\nx: ---\n---\n", raw: "x: ---\n", present: true},
		{source: "---", invalid: true},
		{source: "---\na: b", invalid: true},
	} {
		raw, body, err := gox.SplitFrontmatter("page.gox", tc.source)
		if (err != nil) != tc.invalid || string(raw) != tc.raw || body != tc.body || (raw != nil) != tc.present {
			t.Errorf("%q: raw %q body %q err %v", tc.source, raw, body, err)
		}
	}
}

func TestFormatFrontmatter(t *testing.T) {
	const header = "---\nopaque: {not Go or YAML!}\n---\n"
	formatted, err := gox.FormatSource("page.gox", header+`package plugin
func Page(b *gox.Builder){<p>{"Hello"}</p>}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(formatted, header+"\n") {
		t.Fatalf("header changed: %q", formatted)
	}
	again, err := gox.FormatSource("page.gox", formatted)
	if err != nil || again != formatted {
		t.Fatalf("not idempotent: %q, %v", again, err)
	}
}
