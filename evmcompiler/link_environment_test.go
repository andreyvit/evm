package evmcompiler_test

import (
	"fmt"
	"runtime"
	"slices"
	"sync"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

// linkedShapes lists, for the types a module names, which of them the
// program knows a shape for.
func linkedShapes(m *evm.Module, p *evm.Program) string {
	var names []string
	add := func(t *values.Type) {
		names = append(names, fmt.Sprintf("%s=%t", t.Name(), p.Shape(t) != nil))
	}
	for s := range m.ListedShapes() {
		add(s.Type)
	}
	for _, imp := range m.Imports {
		for _, t := range imp.Signature.Params {
			add(t)
		}
		for _, t := range imp.Signature.Results {
			add(t)
		}
	}
	slices.Sort(names)
	return fmt.Sprint(slices.Compact(names))
}

// A program linked through a link environment behaves like one linked from
// the package list, whether or not its module shares the environment's
// shapes.
func TestLinkEnvironmentMatchesPackageList(t *testing.T) {
	f := newEnvironmentFixture()
	env, err := evmcompiler.NewEnvironment(f.packages...)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := env.NewLinkEnvironment(f.packages...)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := evm.NewLinkEnvironment(f.packages...)
	if err != nil {
		t.Fatal(err)
	}
	for _, options := range []evmcompiler.Options{{}, {NoOptimization: true}} {
		for _, c := range environmentCorpus {
			if c.wantErr != "" {
				continue
			}
			t.Run(fmt.Sprintf("%s/noopt=%v", c.name, options.NoOptimization), func(t *testing.T) {
				lazy, err := options.CompileFiles(c.sources, f.packages...)
				if err != nil {
					t.Fatal(err)
				}
				prepared, err := env.CompileFiles(c.sources, options)
				if err != nil {
					t.Fatal(err)
				}
				want := c.want
				if c.name == "records" {
					want += " read=37"
				}
				for moduleName, m := range map[string]*evm.Module{"package list": lazy, "prepared": prepared} {
					listing := m.Disassemble()
					reference, err := evm.Link(m, f.packages...)
					if err != nil {
						t.Fatal(err)
					}
					for linkName, links := range map[string]*evm.LinkEnvironment{"shared": shared, "plain": plain} {
						for round := 0; round < 2; round++ {
							p, err := links.Link(m)
							if err != nil {
								t.Fatalf("%s module, %s environment: %v", moduleName, linkName, err)
							}
							if got := runEnvironmentProgram(t, f, m, p); got != want {
								t.Fatalf("%s module, %s environment, round %d: result = %q, want %q", moduleName, linkName, round, got, want)
							}
							if a, b := reference.DisassembleFloatLoops(), p.DisassembleFloatLoops(); a != b {
								t.Fatalf("%s module, %s environment: float loops differ\npackage list:\n%s\nenvironment:\n%s", moduleName, linkName, a, b)
							}
							if a, b := linkedShapes(m, reference), linkedShapes(m, p); a != b {
								t.Fatalf("%s module, %s environment: linked shapes differ\npackage list: %s\nenvironment:  %s", moduleName, linkName, a, b)
							}
						}
					}
					if m.Disassemble() != listing {
						t.Fatalf("linking changed the %s module", moduleName)
					}
				}
			})
		}
	}
}

// Many goroutines compile, link and run on one compile environment and one
// link environment. Run with -race: the programs share the linked shapes and
// bindings.
func TestLinkEnvironmentConcurrentUse(t *testing.T) {
	f := newEnvironmentFixture()
	env, err := evmcompiler.NewEnvironment(f.packages...)
	if err != nil {
		t.Fatal(err)
	}
	links, err := env.NewLinkEnvironment(f.packages...)
	if err != nil {
		t.Fatal(err)
	}
	var corpus []environmentCase
	var modules []*evm.Module
	for _, c := range environmentCorpus {
		if c.wantErr != "" {
			continue
		}
		m, err := env.CompileFiles(c.sources, evmcompiler.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if c.name == "records" {
			c.want += " read=37"
		}
		corpus, modules = append(corpus, c), append(modules, m)
	}
	const goroutines, rounds = 16, 4
	var wg sync.WaitGroup
	failures := make(chan string, goroutines*rounds*len(corpus))
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := 0; round < rounds; round++ {
				for step := range corpus {
					i := (step + g) % len(corpus)
					c, m := corpus[i], modules[i]
					if round%2 == 1 { // A module of its own, and one shared with other goroutines.
						var err error
						if m, err = env.CompileFiles(c.sources, evmcompiler.Options{}); err != nil {
							failures <- fmt.Sprintf("%s: %v", c.name, err)
							continue
						}
					}
					p, err := links.Link(m)
					if err != nil {
						failures <- fmt.Sprintf("%s: %v", c.name, err)
						continue
					}
					if got := runEnvironmentProgram(reporter{failures, c.name}, f, m, p); got != c.want {
						failures <- fmt.Sprintf("%s: result = %q, want %q", c.name, got, c.want)
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

// reporter collects failures of helpers that run outside the test goroutine.
type reporter struct {
	failures chan<- string
	name     string
}

func (r reporter) Helper() {}
func (r reporter) Fatal(args ...any) {
	r.failures <- r.name + ": " + fmt.Sprint(args...)
	runtime.Goexit()
}

// A shape that cannot be linked stays out of the shared table: the link that
// names it reports the error, and other links succeed.
func TestLinkEnvironmentInvalidSharedShape(t *testing.T) {
	f := newEnvironmentFixture()
	bad := &values.Shape{Type: values.NewType("bad", values.Pointer), Kind: values.StructKind, Cells: 7}
	links, err := evm.LinkOptions{Shapes: []*values.Shape{bad, nil, f.entry, {}}}.NewEnvironment(f.packages...)
	if err != nil {
		t.Fatal(err)
	}
	m, err := evmcompiler.CompileFiles(map[string]string{"main.go": `func Run() string { return "ok" }`}, f.packages...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := links.Link(m)
	if err != nil {
		t.Fatal(err)
	}
	if got := runEnvironmentProgram(t, f, m, p); got != "ok" {
		t.Fatalf("result = %q", got)
	}
	broken := *m
	broken.Shapes = append(slices.Clone(m.Shapes), bad)
	_, want := evm.Link(&broken, f.packages...)
	_, got := links.Link(&broken)
	if want == nil || got == nil || want.Error() != got.Error() {
		t.Fatalf("package list error %v, environment error %v", want, got)
	}
}
