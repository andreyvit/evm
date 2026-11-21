package gox_test

import (
	"context"
	"strings"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/gox"
	"github.com/andreyvit/evm/values"
)

// appPackage exposes app.Badge as a host component returning its title.
func appPackage() evm.Package {
	return evm.Package{Path: "example.com/app", Functions: []evm.ExternalFunction{{
		Name: "Badge", ParamNames: []string{"title"},
		Signature: evm.Signature{Params: []*values.Type{values.StringType}, Results: []*values.Type{values.StringType}},
		Call: func(c *evm.VM) error {
			c.SetCallResult(0, c.CallArgument(0))
			return nil
		},
	}}}
}

func compileImports(t *testing.T, sources map[string]string, packages ...evm.Package) *gox.Program {
	t.Helper()
	tmpl, err := gox.CompileSources(sources, packages...)
	if err != nil {
		t.Fatal(err)
	}
	program, err := tmpl.Link(packages...)
	if err != nil {
		t.Fatal(err)
	}
	return program
}

// A GoX file imports every package its own source names. The error points at
// the reference and names the import when the package is known.
func TestMissingImportNamesTheImport(t *testing.T) {
	for _, tt := range []struct {
		name, filename, source, want string
	}{
		{"runtime", "page.gox", `package views

func Page(b *gox.Builder) {
	<p>"x"</p>
}
`, `page.gox:3:14: undefined: gox (add import "github.com/andreyvit/evm/gox")`},
		{"standard library in markup", "views/page.gox", `package views

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, n int) {
	<p title={strings.ToUpper("x")}>fmt.Sprint(n)</p>
}
`, `views/page.gox:6:12: undefined: strings (add import "strings")`},
		{"standard library in a helper", "page.gox", `package views

func label(n int) string { return fmt.Sprint(n) }
`, `page.gox:3:35: undefined: fmt (add import "fmt")`},
		{"runtime types", "page.gox", `package views

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
	var ambient *evmruntime.Ambient
	<p>ambient.Get("x")</p>
}
`, `page.gox:6:15: undefined: evmruntime (add import "evmruntime")`},
		{"host package", "page.gox", `package views

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
	<p>app.Badge("x")</p>
}
`, `page.gox:6:5: undefined: app (add import "example.com/app")`},
		{"shared basename", "page.gox", `package views

func note() { log.Print("x") }
`, `page.gox:3:15: undefined: log (add import "evmruntime/log" or "log")`},
		{"after frontmatter", "page.gox", `---
title: Page
---
package views

func Page(b *gox.Builder) {}
`, `page.gox:6:14: undefined: gox (add import "github.com/andreyvit/evm/gox")`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			options := gox.Options{ParseFrontmatter: func([]byte) (any, error) { return nil, nil }}
			_, err := options.CompileSources(map[string]string{tt.filename: tt.source}, appPackage())
			if err == nil || err.Error() != tt.want {
				t.Fatalf("got  %v\nwant %s", err, tt.want)
			}
		})
	}
}

// Unknown names keep the checker's own error.
func TestUnknownPackageNameStaysUndefined(t *testing.T) {
	_, err := gox.Compile("page.gox", `package views

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
	<p>app.Badge("x")</p>
}
`)
	if err == nil || !strings.HasPrefix(err.Error(), "page.gox:6:") || !strings.HasSuffix(err.Error(), ": undefined: app") {
		t.Fatalf("got %v", err)
	}
}

// A tag name lives outside the Go AST, so its package qualifier is checked separately.
func TestComponentTagRequiresImport(t *testing.T) {
	const page = `package views

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
	<section>
		<app.Badge title="x" />
	</section>
}
`
	_, err := gox.Compile("page.gox", page, appPackage())
	if want := `page.gox:7:4: undefined: app (add import "example.com/app")`; err == nil || err.Error() != want {
		t.Fatalf("got  %v\nwant %s", err, want)
	}
	_, err = gox.Compile("page.gox", page)
	if err == nil || !strings.Contains(err.Error(), "page.gox:7:") || !strings.Contains(err.Error(), "unknown or untyped component app.Badge") {
		t.Fatalf("unknown package: %v", err)
	}
	imported := strings.Replace(page, `import "github.com/andreyvit/evm/gox"`, "import (\n\t\"example.com/app\"\n\n\t\"github.com/andreyvit/evm/gox\"\n)", 1)
	program := compileImports(t, map[string]string{"page.gox": imported}, appPackage())
	got, err := program.RenderFunc(context.Background(), "Page", nil, gox.RenderOptions{Strict: true})
	if err != nil || got != "<section>x</section>" {
		t.Fatalf("%q: %v", got, err)
	}
}

// Scopes, not spelling, tell a package from a value: declarations that share a
// package basename are ordinary values and bring in no import.
func TestLocalNamesAreNotPackages(t *testing.T) {
	sources := map[string]string{"page.gox": `package views

import "github.com/andreyvit/evm/gox"

type Card struct{ Label string }

func (c Card) Render(b *gox.Builder, title string) {
	<b>c.Label; title</b>
}

var log = Card{Label: "global "}

func show(b *gox.Builder, app Card) {
	strings := Card{Label: "local "}
	fmt := strings.Render
	<p>app.Label</p>
	<app.Render title="param" />
	<strings.Render title="variable" />
	<fmt title="function" />
	<log.Render title={log.Label} />
}

func Page(b *gox.Builder) {
	<div>show(Card{Label: "param "})</div>
}
`}
	tmpl, err := gox.CompileSources(sources, appPackage())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{`"example.com/app"`, `"strings"`, `"fmt"`, `"log"`} {
		if strings.Contains(tmpl.GeneratedSources["page.gox.go"], path) {
			t.Fatalf("imported %s:\n%s", path, tmpl.GeneratedSources["page.gox.go"])
		}
	}
	program, err := tmpl.Link(appPackage())
	if err != nil {
		t.Fatal(err)
	}
	got, err := program.RenderFunc(context.Background(), "Page", nil, gox.RenderOptions{Strict: true})
	if want := "<div><p>param </p><b>param param</b><b>local variable</b><b>local function</b><b>global global </b></div>"; err != nil || got != want {
		t.Fatalf("%q: %v", got, err)
	}
}

// Lowering may name packages the file never mentions. Those imports are the
// compiler's business; the user's source stays as written.
func TestGeneratedCodeImportsItsOwnPackages(t *testing.T) {
	sources := map[string]string{
		// No markup and no package references: nothing to import.
		"helpers.gox": `package views

func double(n int) int { return n * 2 }
`,
		// The interpolated attribute lowers to fmt.Sprint.
		"labels.gox": `package views

func label(n int) string {
	text := <caption text="No. {double(n)}" />
	return text
}

func caption(text string) string { return "[" + text + "]" }
`,
		// The slot closure takes its parameter type from Each's signature.
		"page.gox": `package views

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, n int) {
	<p>label(n)</p>
	<Each>
		<slot-row><i>ambient.Get("color").(string)</i></slot-row>
	</Each>
}
`,
		"each.gox": `package views

import (
	"evmruntime"

	"github.com/andreyvit/evm/gox"
)

func Each(b *gox.Builder, row func(ambient *evmruntime.Ambient)) {
	var ambient *evmruntime.Ambient
	row(ambient.CloneWith(map[string]any{"color": "teal"}))
}
`,
	}
	tmpl, err := gox.CompileFiles("Page", sources)
	if err != nil {
		t.Fatal(err)
	}
	if generated := tmpl.GeneratedSources["helpers.gox.go"]; strings.Contains(generated, "import") {
		t.Fatalf("helper file gained an import:\n%s", generated)
	}
	if generated := tmpl.GeneratedSources["labels.gox.go"]; !strings.Contains(generated, `"fmt"`) || strings.Contains(generated, "evm/gox") {
		t.Fatalf("labels file imports:\n%s", generated)
	}
	if generated := tmpl.GeneratedSources["page.gox.go"]; !strings.Contains(generated, `"evmruntime"`) {
		t.Fatalf("page file imports:\n%s", generated)
	}
	program, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := program.Render(context.Background(), []values.Value{values.Int(2)}, gox.RenderOptions{Strict: true})
	if err != nil || got != "<p>[No. 4]</p><i>teal</i>" {
		t.Fatalf("%q: %v", got, err)
	}
}

// The formatter is the supported fix: given the host packages, it adds their paths.
func TestFormatSourceAddsHostImports(t *testing.T) {
	const source = `package views

func Page(b *gox.Builder, name string) {
	<section>
		<app.Badge title={strings.ToUpper(name)} />
		app.Badge("plain")
	</section>
}
`
	if _, err := gox.Compile("page.gox", source, appPackage()); err == nil {
		t.Fatal("compiled without imports")
	}
	formatted, err := gox.FormatSource("page.gox", source, appPackage())
	if err != nil {
		t.Fatal(err)
	}
	const want = `package views

import (
	"strings"

	"example.com/app"
	"github.com/andreyvit/evm/gox"
)

func Page(b *gox.Builder, name string) {
	<section>
		<app.Badge title={strings.ToUpper(name)} />
		app.Badge("plain")
	</section>
}
`
	if formatted != want {
		t.Fatalf("got:\n%s\nwant:\n%s", formatted, want)
	}
	program := compileImports(t, map[string]string{"page.gox": formatted}, appPackage())
	got, err := program.RenderFunc(context.Background(), "Page", []values.Value{values.String("ada")}, gox.RenderOptions{Strict: true})
	if err != nil || got != "<section>ADAplain</section>" {
		t.Fatalf("%q: %v", got, err)
	}
	// Without the host package the formatter cannot know the path.
	bare, err := gox.FormatSource("page.gox", source)
	if err != nil || strings.Contains(bare, "example.com/app") || !strings.Contains(bare, `"github.com/andreyvit/evm/gox"`) {
		t.Fatalf("%v\n%s", err, bare)
	}
}
