package gox_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/gox"
	"github.com/andreyvit/evm/values"
)

func renderLinked(c environmentCase, program *gox.Program, err error) string {
	if err != nil {
		return "link: " + err.Error()
	}
	output, err := program.RenderFuncNative(context.Background(), c.entry, c.args, gox.RenderOptions{Strict: true})
	if err != nil {
		return "render: " + err.Error()
	}
	return output
}

// A template linked through a link environment renders what one linked from
// the package list renders, whether or not it was compiled through the
// environment whose shapes the programs share.
func TestLinkEnvironmentMatchesPackageList(t *testing.T) {
	env, err := gox.NewEnvironment(appPackage())
	if err != nil {
		t.Fatal(err)
	}
	shared, err := env.NewLinkEnvironment(appPackage())
	if err != nil {
		t.Fatal(err)
	}
	plain, err := gox.NewLinkEnvironment(appPackage())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range environmentCorpus {
		if c.wantErr != "" {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			lazy, err := gox.Options{}.CompileSources(c.sources, appPackage())
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := gox.Options{Environment: env}.CompileSources(c.sources)
			if err != nil {
				t.Fatal(err)
			}
			for compiled, tmpl := range map[string]*gox.Template{"package list": lazy, "prepared": prepared} {
				listing := tmpl.Module.Disassemble()
				reference, err := tmpl.Link(appPackage())
				if got := renderLinked(c, reference, err); got != c.want {
					t.Fatalf("%s template, package list: %q, want %q", compiled, got, c.want)
				}
				for name, links := range map[string]*gox.LinkEnvironment{"shared": shared, "plain": plain} {
					for round := 0; round < 2; round++ {
						program, err := links.Link(tmpl)
						if got := renderLinked(c, program, err); got != c.want {
							t.Fatalf("%s template, %s environment, round %d: %q, want %q", compiled, name, round, got, c.want)
						}
						if gox.ProgramOf(program.EVM()) != program {
							t.Fatal("the linked program does not know its GoX runtime")
						}
						if a, b := reference.EVM().DisassembleFloatLoops(), program.EVM().DisassembleFloatLoops(); a != b {
							t.Fatalf("%s template, %s environment: float loops differ", compiled, name)
						}
						if fmt.Sprint(program.ParametersFor(c.entry)) != fmt.Sprint(reference.ParametersFor(c.entry)) {
							t.Fatalf("%s template, %s environment: parameters differ", compiled, name)
						}
					}
				}
				if tmpl.Module.Disassemble() != listing {
					t.Fatalf("linking changed the %s module", compiled)
				}
			}
		})
	}

	// Ordinary Go links against the same prepared packages.
	const source = "import (\"example.com/app\"; \"slices\")\nfunc Run() string { return app.Badge(slices.Max([]string{\"a\", \"b\"})) }\n"
	module, err := env.Compiler().Compile("main.go", source, evmcompiler.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for name, links := range map[string]*gox.LinkEnvironment{"shared": shared, "plain": plain} {
		program, err := links.LinkModule(module)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]values.Value, 1)
		if err := evm.New(program.EVM()).Run(context.Background(), "Run", nil, out); err != nil || out[0].AsString() != "b" {
			t.Fatalf("%s environment: Run = %q, %v", name, out[0].AsString(), err)
		}
	}
	// A Go module compiled by another environment shares that one's shapes.
	code, err := evmcompiler.NewEnvironment(appPackage(), gox.Package())
	if err != nil {
		t.Fatal(err)
	}
	both, err := gox.LinkOptions{Compilers: []*evmcompiler.Environment{env.Compiler(), code}}.NewEnvironment(appPackage())
	if err != nil {
		t.Fatal(err)
	}
	module, err = code.Compile("main.go", "import \"example.com/app\"\nfunc Run() string { return app.Badge(\"b\") }\n", evmcompiler.Options{})
	if err != nil {
		t.Fatal(err)
	}
	program, err := both.LinkModule(module)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]values.Value, 1)
	if err := evm.New(program.EVM()).Run(context.Background(), "Run", nil, out); err != nil || out[0].AsString() != "b" {
		t.Fatalf("two compile environments: Run = %q, %v", out[0].AsString(), err)
	}
	c := environmentCorpus[0]
	tmpl, err := gox.Options{Environment: env}.CompileSources(c.sources)
	if err != nil {
		t.Fatal(err)
	}
	linked, err := both.Link(tmpl)
	if got := renderLinked(c, linked, err); got != c.want {
		t.Fatalf("two compile environments: %q, want %q", got, c.want)
	}
}

// Many goroutines compile, link and render on one compile environment and
// one link environment. Run with -race.
func TestLinkEnvironmentConcurrentUse(t *testing.T) {
	env, err := gox.NewEnvironment(appPackage())
	if err != nil {
		t.Fatal(err)
	}
	links, err := env.NewLinkEnvironment(appPackage())
	if err != nil {
		t.Fatal(err)
	}
	var corpus []environmentCase
	var templates []*gox.Template
	for _, c := range environmentCorpus {
		if c.wantErr != "" {
			continue
		}
		tmpl, err := gox.Options{Environment: env}.CompileSources(c.sources)
		if err != nil {
			t.Fatal(err)
		}
		corpus, templates = append(corpus, c), append(templates, tmpl)
	}
	const goroutines, rounds = 12, 4
	var wg sync.WaitGroup
	failures := make(chan string, goroutines*rounds*len(corpus))
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := 0; round < rounds; round++ {
				for step := range corpus {
					i := (step + g) % len(corpus)
					c, tmpl := corpus[i], templates[i]
					if round%2 == 1 { // A template of its own, and one shared with other goroutines.
						var err error
						if tmpl, err = (gox.Options{Environment: env}).CompileSources(c.sources); err != nil {
							failures <- c.name + ": " + err.Error()
							continue
						}
					}
					program, err := links.Link(tmpl)
					for render := 0; render < 2; render++ {
						if got := renderLinked(c, program, err); got != c.want {
							failures <- fmt.Sprintf("%s: %q, want %q", c.name, got, c.want)
						}
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
