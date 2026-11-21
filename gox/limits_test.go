package gox_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/gox"
)

const limitsHeader = "package plugin\n\nimport \"github.com/andreyvit/evm/gox\"\n\nfunc Page(b *gox.Builder) {\n"

// nestedPage repeats open and close n times around an output, a line each.
func nestedPage(n int, open, close string) string {
	return limitsHeader + strings.Repeat(open, n) + "\"x\"\n" + strings.Repeat(close, n) + "}\n"
}

// limitErrors runs the three entry points that read guest source and requires
// them to agree, since a draft is checked and formatted before it is compiled.
func limitErrors(t *testing.T, source string) string {
	t.Helper()
	text := func(err error) string {
		if err == nil {
			return ""
		}
		return err.Error()
	}
	checked := text(gox.CheckSyntax("page.gox", source))
	_, err := gox.FormatSource("page.gox", source)
	if formatted := text(err); formatted != checked {
		t.Fatalf("CheckSyntax: %s\nFormatSource: %s", checked, formatted)
	}
	_, err = gox.Compile("page.gox", source)
	if compiled := text(err); compiled != checked {
		t.Fatalf("CheckSyntax: %s\nCompile: %s", checked, compiled)
	}
	return checked
}

func TestTagNestingLimit(t *testing.T) {
	if gox.MaxTagNesting != 100 {
		t.Fatal("the positions below assume 100")
	}
	for name, c := range map[string]struct{ source, want string }{
		"at the limit":      {nestedPage(100, "<div>\n", "</div>\n"), ""},
		"past the limit":    {nestedPage(101, "<div>\n", "</div>\n"), "page.gox:106:1: tags nest more than 100 levels"},
		"same line":         {limitsHeader + strings.Repeat("<b>", 101) + strings.Repeat("</b>", 101) + "\n}\n", "page.gox:6:301: tags nest more than 100 levels"},
		"fragments":         {nestedPage(101, "<>\n", "</>\n"), "page.gox:106:1: tags nest more than 100 levels"},
		"siblings":          {nestedPage(100, "<p></p><div>\n", "</div><p></p>\n"), ""},
		"void tags":         {limitsHeader + "<div>\n" + strings.Repeat("<br>\n<img src=\"a\">\n<hr/>\n", 200) + "</div>\n}\n", ""},
		"closed void tags":  {nestedPage(101, "<input>\n", "</input>\n"), "page.gox:106:1: tags nest more than 100 levels"},
		"10,000 deep":       {nestedPage(10000, "<div>\n", "</div>\n"), "page.gox:106:1: tags nest more than 100 levels"},
		"500 deep with ifs": {nestedPage(500, "<div>\nif true {\n", "}\n</div>\n"), "page.gox:206:1: tags nest more than 100 levels"},
	} {
		if got := limitErrors(t, c.source); got != c.want {
			t.Errorf("%s: got %s, want %s", name, got, c.want)
		}
	}
}

// Positions are the author's, although the limits are checked on the Go that
// preprocessing produced, where a tag is a call on one line.
func TestGoLimitsInTemplates(t *testing.T) {
	markup := "<div\n\tclass=\"a\"\n\tid=\"b\">\n</div>\n<p>\"x\"</p>; "
	for name, c := range map[string]struct{ source, want string }{
		"statements":        {nestedPage(101, "if true {\n", "}\n"), "page.gox:106:1: statements nest more than 100 levels"},
		"type":              {limitsHeader + markup + "var x " + strings.Repeat("[]", 33) + "int\n_ = x\n}\n", "page.gox:10:83: type nests more than 32 levels"},
		"expression":        {limitsHeader + markup + "_ = " + strings.Repeat("(", 501) + "1" + strings.Repeat(")", 501) + "\n}\n", "page.gox:10:517: expression nests more than 500 levels"},
		"attribute":         {limitsHeader + "<p\n\tclass={" + strings.Repeat("(", 501) + "\"a\"" + strings.Repeat(")", 501) + "}></p>\n}\n", "page.gox:7:507: expression nests more than 500 levels"},
		"function literals": {limitsHeader + markup + strings.Repeat("func() {", 33) + strings.Repeat("}()", 33) + "\n}\n", "page.gox:10:269: function literals nest more than 32 levels"},
		"constant":          {limitsHeader + markup + "const a = \"" + strings.Repeat("x", 600000) + "\"\n<p>a + a</p>\n}\n", "page.gox:11:4: constant string may exceed 1048576 bytes"},
		"branches":          {limitsHeader + markup + strings.Repeat("if true {\n<p>\"x\"</p>\n}\n", 4200) + "}\n", "page.gox:5:6: function is too large: its branches and local variables multiply past the compile budget"},
	} {
		if got := limitErrors(t, c.source); got != c.want {
			t.Errorf("%s: got %.200s, want %s", name, got, c.want)
		}
	}
}

// A template at the limits compiles: the Go that lowering adds around an
// output is allowed for, and the author's own nesting is what counts.
func TestTemplateAtTheLimitsCompiles(t *testing.T) {
	source := `package plugin

import "github.com/andreyvit/evm/gox"

type Node struct {
	Name string
	Next *Node
	Open bool
}

func Page(b *gox.Builder) {
	node := &Node{Name: "leaf", Open: true}
	root := &Node{Next: node, Open: true}
	if root.Open {
` + strings.Repeat("<div>\nif root.Open {\n", 99) + "<b>root.Next.Name</b>\n<i>root.Next.Next.Name</i>\n<u hidden={root.Open && (root.Next.Open || root.Next.Next.Open)}></u>\n" + strings.Repeat("}\n</div>\n", 99) + "}\n}\n"
	if err := gox.CheckSyntax("page.gox", source); err != nil {
		t.Fatal(err)
	}
	formatted, err := gox.FormatSource("page.gox", source)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := gox.FormatSource("page.gox", formatted); err != nil || again != formatted {
		t.Fatalf("unstable formatting: %v", err)
	}
	tmpl, err := gox.Compile("page.gox", source)
	if err != nil {
		t.Fatal(err)
	}
	program, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	var missing []gox.Diagnostic
	got, err := program.Render(context.Background(), nil, gox.RenderOptions{Report: func(d gox.Diagnostic) { missing = append(missing, d) }})
	want := strings.Repeat("<div>", 99) + "<b>leaf</b><i></i><u hidden></u>" + strings.Repeat("</div>", 99)
	if err != nil || got != want || len(missing) != 1 || missing[0].Position.Line != 214 {
		t.Fatalf("%v: %.200s %v", err, got, missing)
	}
}

// A component with children is a function literal, so components nest as
// deep as function literals do. The diagnostic names the tag's position.
func TestComponentNestingCountsAsFunctionLiterals(t *testing.T) {
	page := func(n int) string {
		return `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
` + strings.Repeat("<Box>\n", n) + "\"x\"\n" + strings.Repeat("</Box>\n", n) + `}

func Box(b *gox.Builder, body func()) {
	<div>body()</div>
}
`
	}
	tmpl, err := gox.Compile("page.gox", page(evmcompiler.MaxFuncNesting))
	if err != nil {
		t.Fatal(err)
	}
	program, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := program.Render(context.Background(), nil, gox.RenderOptions{})
	if want := strings.Repeat("<div>", 32) + "x" + strings.Repeat("</div>", 32); err != nil || got != want {
		t.Fatalf("%v: %.200s", err, got)
	}
	_, err = gox.Compile("page.gox", page(evmcompiler.MaxFuncNesting+1))
	if want := fmt.Sprintf("page.gox:%d:1: function literals nest more than 32 levels", 5+evmcompiler.MaxFuncNesting+1); err == nil || err.Error() != want {
		t.Fatalf("got %v, want %s", err, want)
	}
}
