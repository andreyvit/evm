package evm_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmstd"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/values"
)

type linkTestError struct{ code int }

func (e *linkTestError) Error() string { return fmt.Sprint("code ", e.code) }

// linkTestPackages is a host with records, native errors, interface results,
// a package value, a generic native function and the evmstd packages.
func linkTestPackages(t testing.TB) []evm.Package {
	s := kvo.NewSchema()
	customer := kvo.NewEntityType(s, "customer")
	total := kvo.NewProp(s, 99, "total", kvo.TInt64, nil)
	model := kvo.NewModel(s, customer, func(b *kvo.ModelBuilder) { b.Prop(s.MustPropByCode(total)) })
	db := evm.Package{Path: "db", Records: []*evm.RecordType{evm.NewRecordType("Customer", model, false), evm.NewRecordType("Edit", model, true)}}

	sentinel := errors.New("sentinel")
	failures := []error{nil, sentinel, fmt.Errorf("wrapped: %w", sentinel), &linkTestError{7}}
	host := evm.Package{Path: "host", Source: "package host\nfunc First[T comparable](a, b T) T\n", Functions: []evm.ExternalFunction{
		{Name: "Fail", Signature: evm.Signature{Params: []*values.Type{values.IntType}, Results: []*values.Type{values.ErrorType}}, Call: func(c *evm.Call) error { return c.SetError(0, failures[c.Argument(0).AsInt()]) }},
		{Name: "Any", Signature: evm.Signature{Params: []*values.Type{values.IntType}, Results: []*values.Type{values.AnyType}}, DynamicTypes: []*values.Type{values.StringType, values.IntType}, Call: func(c *evm.Call) error {
			var x any = "text"
			if c.Argument(0).AsInt() != 0 {
				x = int(c.Argument(0).AsInt())
			}
			v, err := c.AnyValue(x)
			if err != nil {
				return err
			}
			c.SetResult(0, v)
			return nil
		}},
		{Name: "First", Instantiate: func(args []*values.Type) (evm.ExternalFunction, error) {
			if len(args) != 1 {
				return evm.ExternalFunction{}, fmt.Errorf("First needs one type argument")
			}
			return evm.ExternalFunction{Signature: evm.Signature{Params: []*values.Type{args[0], args[0]}, Results: args}, Call: func(c *evm.Call) error { c.SetResult(0, c.Argument(0)); return nil }}, nil
		}},
	}, Values: []evm.ExternalValue{{Name: "Limit", Type: values.IntType, Read: func(c *evm.Call) error { c.SetResult(0, values.Int(40)); return nil }}}}
	return append([]evm.Package{db, host}, evmstd.Packages()...)
}

var linkTestSources = map[string]string{
	"basics": `import ("fmt";"strings")
type Shape interface{ Area() int }
type Circle struct{ R int }
func (c Circle) Area() int { return c.R * c.R }
type Pair[K comparable, V any] struct{ Key K; Value V }
var counter = 2
func init() { counter++ }
func sum(xs ...int) (total int) { for _, x := range xs { total += x }; return }
func safe() (msg string) { defer func() { if r := recover(); r != nil { msg = fmt.Sprint("recovered:", r) } }(); panic("boom") }
func describe(v any) string { switch v := v.(type) { case Shape: return fmt.Sprint("circle:", v.Area()); case string: return v }; return "?" }
func Run() string {
	m := map[string]int{"a": 1, "b": 2, "c": 3}
	add := func(n int) func(int) int { return func(x int) int { return x + n } }
	pairs := []Pair[string, int]{{"b", 2}, {"a", 1}}
	names := make([]string, 0, len(pairs))
	for _, p := range pairs { names = append(names, p.Key) }
	total := 0.0
	for i := 0; i < 100; i++ { total += float64(i) * 0.5 }
	return fmt.Sprint(sum(1, 2, 3), " ", len(m), " ", safe(), " ", describe(Circle{3}), " ", describe("s"), " ", strings.Join(names, ","), " ", add(2)(counter), " ", total)
}`,
	"host": `import ("errors";"fmt";"host";"slices";"strconv")
type ID uint64
func Run() string {
	out := []string{strconv.Itoa(host.Limit + 2)}
	for i := 0; i < 4; i++ {
		err := host.Fail(i)
		out = append(out, fmt.Sprint(err, errors.Is(err, host.Fail(1))))
	}
	slices.Sort(out)
	text, _ := host.Any(0).(string)
	number, _ := host.Any(5).(int)
	return fmt.Sprint(out, " ", text, " ", number, " ", host.First("a", "b"), " ", host.First(ID(7), ID(8)))
}`,
	"records": `import "db"
func Change(c *db.Edit) { c.total += 3 }
func Read(c *db.Customer) int64 { copy := *c; return copy.total * 10 }
func Run() string { return "records" }`,
	"panic": `import "host"
func Run() string { panic(host.Fail(3)) }`,
}

func linkTestRun(p *evm.Program) string {
	out := make([]values.Value, 1)
	if err := evm.New(p).Run(context.Background(), "Run", nil, out); err != nil {
		return "error: " + err.Error()
	}
	return out[0].AsString()
}

// A program linked through a prepared environment equals one linked from the
// package list: the same derived tables, the same results. That holds for a
// module that names the environment's shared shapes and for one that does
// not, and nothing writes to what the programs share.
func TestLinkEnvironmentMatchesPackageList(t *testing.T) {
	packages := linkTestPackages(t)
	compiler, err := evmcompiler.NewEnvironment(packages...)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := compiler.NewLinkEnvironment(packages...)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := evm.NewLinkEnvironment(packages...)
	if err != nil {
		t.Fatal(err)
	}
	fingerprints := map[*evm.LinkEnvironment]uint64{shared: shared.Fingerprint(), plain: plain.Fingerprint()}
	for name, source := range linkTestSources {
		for _, options := range []evmcompiler.Options{{}, {NoOptimization: true}} {
			t.Run(fmt.Sprintf("%s/noopt=%v", name, options.NoOptimization), func(t *testing.T) {
				lazy, err := options.Compile(name+".go", source, packages...)
				if err != nil {
					t.Fatal(err)
				}
				prepared, err := compiler.Compile(name+".go", source, options)
				if err != nil {
					t.Fatal(err)
				}
				for _, m := range []*evm.Module{lazy, prepared} {
					listing := m.Disassemble()
					want, err := evm.Link(m, packages...)
					if err != nil {
						t.Fatal(err)
					}
					for _, env := range []*evm.LinkEnvironment{shared, plain} {
						for round := 0; round < 2; round++ {
							got, err := env.LinkWithHostData(m, name)
							if err != nil {
								t.Fatal(err)
							}
							if got.HostData() != name {
								t.Fatalf("host data = %v", got.HostData())
							}
							if a, b := evm.ProgramListing(want), evm.ProgramListing(got); a != b {
								t.Fatalf("round %d: programs differ\npackage list:\n%s\nenvironment:\n%s", round, a, b)
							}
							if a, b := linkTestRun(want), linkTestRun(got); a != b || strings.HasPrefix(a, "error") != (name == "panic") {
								t.Fatalf("round %d: package list result %q, environment result %q", round, a, b)
							}
							sharedShapes, _ := evm.SharedShapes(got)
							if env == shared && m == prepared && sharedShapes == 0 {
								t.Fatal("a prepared module shares no shapes")
							}
							if (env != shared || m != prepared) && sharedShapes != 0 {
								t.Fatalf("a module with its own shapes shares %d", sharedShapes)
							}
						}
					}
					if m.Disassemble() != listing {
						t.Fatal("linking changed the module")
					}
				}
			})
		}
	}
	for env, want := range fingerprints {
		if env.Fingerprint() != want {
			t.Error("linking or running wrote to data the programs share")
		}
	}
}

// A link through an environment reports what a link from the packages does.
func TestLinkEnvironmentErrors(t *testing.T) {
	packages := linkTestPackages(t)
	env, err := evm.NewLinkEnvironment(packages...)
	if err != nil {
		t.Fatal(err)
	}
	other := evm.Package{Path: "other", Functions: []evm.ExternalFunction{{Name: "Value", Signature: evm.Signature{Results: []*values.Type{values.IntType}}, Call: func(c *evm.Call) error { c.SetResult(0, values.Int(1)); return nil }}}}
	unbound, err := evmcompiler.Compile("main.go", `import "other"
func Run() int { return other.Value() }`, append(packages, other)...)
	if err != nil {
		t.Fatal(err)
	}
	changed := other
	changed.Functions = []evm.ExternalFunction{{Name: "Value", Signature: evm.Signature{Results: []*values.Type{values.StringType}}, Call: other.Functions[0].Call}}
	mismatch, err := evm.NewLinkEnvironment(append(packages, changed)...)
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		env      *evm.LinkEnvironment
		packages []evm.Package
		module   *evm.Module
	}{
		"nil module":         {env, packages, nil},
		"empty module":       {env, packages, &evm.Module{}},
		"unresolved import":  {env, packages, unbound},
		"signature mismatch": {mismatch, append(packages, changed), unbound},
	} {
		_, want := evm.Link(c.module, c.packages...)
		_, got := c.env.Link(c.module)
		if want == nil || got == nil || want.Error() != got.Error() {
			t.Errorf("%s: package list error %v, environment error %v", name, want, got)
		}
	}

	// Package errors fail the environment with the error every link reports.
	for name, bad := range map[string][]evm.Package{
		"duplicate package": append(packages, packages[0]),
		"reserved package":  append(packages, evm.Package{Path: "strings"}),
		"unbound function":  append(packages, evm.Package{Path: "bad", Functions: []evm.ExternalFunction{{Name: "F"}}}),
		"invalid codec":     append(packages, evm.Package{Path: "bad", Types: []evm.TypeDecl{{NativeCodec: &evm.NativeValueCodec{}}}}),
	} {
		m, err := evmcompiler.Compile("main.go", `func Run() int { return 1 }`)
		if err != nil {
			t.Fatal(err)
		}
		_, want := evm.Link(m, bad...)
		_, got := evm.NewLinkEnvironment(bad...)
		if want == nil || got == nil || want.Error() != got.Error() {
			t.Errorf("%s: link error %v, environment error %v", name, want, got)
		}
	}
}

// Links and runs on one environment do not interfere; run with -race.
func TestLinkEnvironmentConcurrentUse(t *testing.T) {
	packages := linkTestPackages(t)
	compiler, err := evmcompiler.NewEnvironment(packages...)
	if err != nil {
		t.Fatal(err)
	}
	env, err := compiler.NewLinkEnvironment(packages...)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := env.Fingerprint()
	type compiled struct {
		name   string
		module *evm.Module
		want   string
	}
	var modules []compiled
	for name, source := range linkTestSources {
		for _, compile := range []func() (*evm.Module, error){
			func() (*evm.Module, error) { return compiler.Compile(name+".go", source, evmcompiler.Options{}) },
			func() (*evm.Module, error) { return evmcompiler.Compile(name+".go", source, packages...) },
		} {
			m, err := compile()
			if err != nil {
				t.Fatal(err)
			}
			p, err := evm.Link(m, packages...)
			if err != nil {
				t.Fatal(err)
			}
			modules = append(modules, compiled{name, m, linkTestRun(p)})
		}
	}
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := 0; round < 10; round++ {
				for _, c := range modules {
					p, err := env.Link(c.module)
					if err != nil {
						t.Error(err)
						return
					}
					if got := linkTestRun(p); got != c.want {
						t.Errorf("%s: got %q, want %q", c.name, got, c.want)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	if env.Fingerprint() != fingerprint {
		t.Error("linking or running wrote to data the programs share")
	}
}
