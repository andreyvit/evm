package gox_test

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/gox"
	"github.com/andreyvit/evm/values"
)

type environmentCase struct {
	name    string
	sources map[string]string
	entry   string
	args    []any
	want    string // rendered output
	wantErr string // substring of the compile error
}

var environmentCorpus = []environmentCase{
	{name: "helper", entry: "Page", args: []any{"<Alice>", 2}, want: `<section><h1>Customers: 2</h1><p>Hello, &lt;Alice&gt;!</p><span>0</span><span>1</span></section>`, sources: map[string]string{"customers.gox": `package plugin

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
`}},
	{name: "escaping", entry: "Page", args: []any{`" onmouseover="alert(1)`}, want: `<a href="%22%20onmouseover=%22alert%281%29" title="&#34; onmouseover=&#34;alert(1)">&#34; onmouseover=&#34;alert(1)</a><script>const v = "\" onmouseover=\"alert(1)";</script><textarea>&#34; onmouseover=&#34;alert(1)</textarea>`, sources: map[string]string{"page.gox": `package plugin

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, value string) {
 <a href={value} title={value}>{value}</a>
 <script>const v = {=value};</script>
 <textarea>{value}</textarea>
}`}},
	{name: "standard packages", entry: "Page", args: []any{[]string{"b", "a"}, time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC), "café au lait"}, want: `<p title="a,b">Sunday March caf%C3%A9&#43;au&#43;lait non-ASCII</p>`, sources: map[string]string{"page.gox": `package views

import (
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/andreyvit/evm/gox"
)

func Page(b *gox.Builder, tags []string, joined time.Time, query string) {
	sorted := slices.Sorted(slices.Values(tags))
	<p title={strings.Join(sorted, ",")}>
		joined.Add(36 * time.Hour).Weekday().String()
		" "
		joined.Month().String()
		" "
		url.QueryEscape(query)
		if (utf8.RuneCountInString(query) < len(query)) {
			" non-ASCII"
		}
	</p>
}
`}},
	// Several files: a component in its own GoX file, an ordinary Go helper
	// and a host component.
	{name: "components", entry: "Page", args: []any{"north"}, want: `<main class="page"><div class="card"><h2>NORTH</h2><b>host</b></div><div class="card"><h2>SOUTH</h2><b>host</b></div></main>`, sources: map[string]string{
		"page.gox": `package views

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, region string) {
	<main class="page">
		<Card title={region} />
		<Card title="south" />
	</main>
}
`,
		"card.gox": `package views

import (
	"example.com/app"
	"github.com/andreyvit/evm/gox"
)

func Card(b *gox.Builder, title string) {
	<div class="card">
		<h2>{shout(title)}</h2>
		<b>{app.Badge("host")}</b>
	</div>
}
`,
		"helpers.go": `package views

import "strings"

func shout(s string) string { return strings.ToUpper(s) }
`}},
	{name: "missing import", wantErr: `undefined: strings (add import "strings")`, sources: map[string]string{"page.gox": `package views

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, n int) {
	<p title={strings.ToUpper("x")}>"x"</p>
}
`}},
	{name: "type error", wantErr: "cannot use", sources: map[string]string{"page.gox": `package views

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, n int) {
	var s string = n
	<p>{s}</p>
}
`}},
	{name: "unclosed element", wantErr: "page.gox", sources: map[string]string{"page.gox": `package views

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder) {
	<p>
}
`}},
}

type renderedTemplate struct {
	generated  map[string]string
	listing    string
	parameters string
	classes    string
	output     string
	err        string
}

func renderEnvironmentCase(c environmentCase, options gox.Options, packages ...evm.Package) renderedTemplate {
	tmpl, err := options.CompileSources(c.sources, packages...)
	if err != nil {
		return renderedTemplate{err: err.Error()}
	}
	result := renderedTemplate{generated: tmpl.GeneratedSources, listing: tmpl.Module.Disassemble(), parameters: fmt.Sprintf("%+v", tmpl.FunctionParameters), classes: fmt.Sprintf("%+v %v", tmpl.Classes, tmpl.DynamicClasses)}
	program, err := tmpl.Link(appPackage())
	if err != nil {
		return renderedTemplate{err: "link: " + err.Error()}
	}
	result.output, err = program.RenderFuncNative(context.Background(), c.entry, c.args, gox.RenderOptions{Strict: true})
	if err != nil {
		return renderedTemplate{err: "render: " + err.Error()}
	}
	return result
}

func (r renderedTemplate) difference(other renderedTemplate) string {
	switch {
	case r.err != other.err:
		return fmt.Sprintf("errors differ:\n%s\n%s", r.err, other.err)
	case !maps.Equal(r.generated, other.generated):
		return "generated sources differ"
	case r.listing != other.listing:
		return "disassembly differs"
	case r.parameters != other.parameters:
		return fmt.Sprintf("parameters differ:\n%s\n%s", r.parameters, other.parameters)
	case r.classes != other.classes:
		return fmt.Sprintf("classes differ:\n%s\n%s", r.classes, other.classes)
	case r.output != other.output:
		return fmt.Sprintf("output differs:\n%s\n%s", r.output, other.output)
	}
	return ""
}

// Templates compiled through a prepared environment must equal those the
// package-list entry points produce, and render the same output.
func TestEnvironmentMatchesPackageList(t *testing.T) {
	env, err := gox.NewEnvironment(appPackage())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range environmentCorpus {
		t.Run(c.name, func(t *testing.T) {
			lazy := renderEnvironmentCase(c, gox.Options{}, appPackage())
			if c.wantErr != "" {
				if !strings.Contains(lazy.err, c.wantErr) {
					t.Fatalf("error = %q, want %q", lazy.err, c.wantErr)
				}
			} else if lazy.err != "" || lazy.output != c.want {
				t.Fatalf("package list: output %q, error %q, want %q", lazy.output, lazy.err, c.want)
			}
			for round := 0; round < 2; round++ { // The second compile sees what the first left behind.
				prepared := renderEnvironmentCase(c, gox.Options{Environment: env})
				if difference := lazy.difference(prepared); difference != "" {
					t.Fatalf("round %d: %s", round, difference)
				}
			}
		})
	}
}

// Many goroutines compile and render different templates on one environment.
// Run with -race.
func TestEnvironmentConcurrentCompiles(t *testing.T) {
	env, err := gox.NewEnvironment(appPackage())
	if err != nil {
		t.Fatal(err)
	}
	references := make([]renderedTemplate, len(environmentCorpus))
	for i, c := range environmentCorpus {
		references[i] = renderEnvironmentCase(c, gox.Options{Environment: env})
	}
	const goroutines, rounds = 12, 2
	var wg sync.WaitGroup
	failures := make(chan string, goroutines*rounds*len(environmentCorpus))
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := 0; round < rounds; round++ {
				for step := range environmentCorpus {
					i := (step + g) % len(environmentCorpus)
					got := renderEnvironmentCase(environmentCorpus[i], gox.Options{Environment: env})
					if difference := references[i].difference(got); difference != "" {
						failures <- environmentCorpus[i].name + ": " + difference
					}
				}
			}
		}()
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
}

func TestEnvironmentEntryPoints(t *testing.T) {
	env, err := gox.NewEnvironment(appPackage())
	if err != nil {
		t.Fatal(err)
	}
	options := gox.Options{Environment: env}
	c := environmentCorpus[0]
	const name = "customers.gox"
	source := c.sources[name]

	if _, err := options.CompileSources(c.sources, appPackage()); err == nil || !strings.Contains(err.Error(), "Options.Environment supplies the packages") {
		t.Fatalf("packages with an environment: %v", err)
	}
	if _, err := options.FormatSource(name, source, appPackage()); err == nil || !strings.Contains(err.Error(), "Options.Environment supplies the packages") {
		t.Fatalf("FormatSource packages with an environment: %v", err)
	}

	reference, err := gox.Compile(name, source, appPackage())
	if err != nil {
		t.Fatal(err)
	}
	compilers := map[string]func() (*gox.Template, error){
		"Compile":        func() (*gox.Template, error) { return options.Compile(name, source) },
		"CompileFiles":   func() (*gox.Template, error) { return options.CompileFiles("Page", c.sources) },
		"CompileSources": func() (*gox.Template, error) { return options.CompileSources(c.sources) },
		"Preprocess": func() (*gox.Template, error) {
			p, err := options.Preprocess(name, source)
			if err != nil {
				return nil, err
			}
			return p.Compile()
		},
		"PreprocessFiles": func() (*gox.Template, error) {
			p, err := options.PreprocessFiles("Page", c.sources)
			if err != nil {
				return nil, err
			}
			return p.Compile()
		},
		"PreprocessSources": func() (*gox.Template, error) {
			p, err := options.PreprocessSources(c.sources)
			if err != nil {
				return nil, err
			}
			return p.Compile()
		},
	}
	for name, compile := range compilers {
		tmpl, err := compile()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !maps.Equal(tmpl.GeneratedSources, reference.GeneratedSources) || tmpl.Module.Disassemble() != reference.Module.Disassemble() {
			t.Errorf("%s differs from the package-list compile", name)
		}
		if tmpl.Entry != "" && !reflect.DeepEqual(tmpl.Parameters, reference.Parameters) {
			t.Errorf("%s: parameters %+v, want %+v", name, tmpl.Parameters, reference.Parameters)
		}
	}

	const unformatted = "package views\n\nfunc Page(b *gox.Builder) {\n<p>{strings.ToUpper(app.Badge(\"x\"))}</p>\n}\n"
	want, err := gox.FormatSource("page.gox", unformatted, appPackage())
	if err != nil {
		t.Fatal(err)
	}
	got, err := options.FormatSource("page.gox", unformatted)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || !strings.Contains(got, `"example.com/app"`) {
		t.Errorf("FormatSource with an environment:\n%s\nwant:\n%s", got, want)
	}

	// Ordinary Go compiles against the same prepared packages.
	module, err := env.Compiler().Compile("main.go", "import (\"example.com/app\"; \"slices\")\nfunc Run() string { return app.Badge(slices.Max([]string{\"a\", \"b\"})) }\n", evmcompiler.Options{})
	if err != nil {
		t.Fatal(err)
	}
	program, err := gox.LinkModule(module, appPackage())
	if err != nil {
		t.Fatal(err)
	}
	out := make([]values.Value, 1)
	if err := evm.New(program.EVM()).Run(context.Background(), "Run", nil, out); err != nil || out[0].AsString() != "b" {
		t.Fatalf("Run = %q, %v", out[0].AsString(), err)
	}
}
