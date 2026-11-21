package evmcompiler_test

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/kvo/codec/row2"
	"github.com/andreyvit/evm/nativebind"
	"github.com/andreyvit/evm/values"
)

// environmentFixture is a host with every kind of declaration: KVO records
// with operations, native structs with a native function, a generic native
// function declared in source, and a source package with bodies and generics.
type environmentFixture struct {
	packages []evm.Package
	customer *kvo.Model
	total    kvo.PropCode
	entry    *values.Shape
	entryPtr *values.Shape
}

func newEnvironmentFixture() *environmentFixture {
	f := &environmentFixture{}
	s := kvo.NewSchema()
	bar := kvo.NewEntityType(s, "bar")
	customer := kvo.NewEntityType(s, "customer")
	boz := kvo.NewProp(s, 5, "boz", kvo.TInt64, nil)
	child := kvo.NewProp(s, 41, "bar", bar, nil)
	f.total = kvo.NewProp(s, 99, "total", kvo.TInt64, nil)
	barModel := kvo.NewModel(s, bar, func(b *kvo.ModelBuilder) { b.Prop(s.MustPropByCode(boz)) })
	f.customer = kvo.NewModel(s, customer, func(b *kvo.ModelBuilder) {
		b.Prop(s.MustPropByCode(f.total))
		b.Prop(s.MustPropByCode(child))
	})
	db := evm.Package{Path: "db", Records: []*evm.RecordType{evm.NewRecordType("Customer", f.customer, false), evm.NewRecordType("Bar", barModel, false), evm.NewRecordType("Edit", f.customer, true), evm.NewRecordType("EditBar", barModel, true)}}

	text := &values.Shape{Type: values.StringType, Kind: values.StringKind, Cells: 1}
	number := &values.Shape{Type: values.Int64, Kind: values.IntKind, Bits: 64, Cells: 1}
	mode := values.NewType("demo.Mode", values.Raw)
	modeShape := &values.Shape{Type: mode, Kind: values.IntKind, Bits: 64, Cells: 1}
	childShape := values.NewStructShape("demo.Child", values.Field{Name: "Text", Shape: text})
	childPtr := values.NewPointerShape(childShape)
	f.entry = values.NewStructShape("demo.Entry", values.Field{Name: "Title", Shape: text}, values.Field{Name: "Count", Shape: number}, values.Field{Name: "Mode", Shape: modeShape}, values.Field{Name: "Child", Shape: childPtr})
	f.entryPtr = values.NewPointerShape(f.entry)
	entry := f.entry
	demo := evm.Package{Path: "example.test/demo", Types: []evm.TypeDecl{{Name: "Mode", Shape: modeShape}, {Name: "Entry", Shape: f.entry}, {Shape: f.entryPtr}, {Name: "Child", Shape: childShape}, {Shape: childPtr}},
		Constants: []evm.ExternalConstant{{Name: "On", Type: mode, Value: values.Uint(2)}},
		Functions: []evm.ExternalFunction{{Name: "Describe", Signature: evm.Signature{Params: []*values.Type{f.entryPtr.Type}, Results: []*values.Type{values.StringType}}, Call: func(c *evm.Call) error {
			p := c.Argument(0).P
			c.SetResult(0, values.String(fmt.Sprintf("%s/%d/%d", entry.GetField(p, 0).AsString(), entry.GetField(p, 1).AsInt(), entry.GetField(p, 2).R)))
			return nil
		}}}}

	host := evm.Package{Path: "host", Source: "package host\nfunc First[T comparable](a, b T) T\n", Functions: []evm.ExternalFunction{{Name: "First", Instantiate: func(args []*values.Type) (evm.ExternalFunction, error) {
		if len(args) != 1 {
			return evm.ExternalFunction{}, fmt.Errorf("First needs one type argument")
		}
		return evm.ExternalFunction{Signature: evm.Signature{Params: []*values.Type{args[0], args[0]}, Results: args}, Call: func(c *evm.Call) error { c.SetResult(0, c.Argument(0)); return nil }}, nil
	}}}}

	util := evm.Package{Path: "example.test/util", Source: `package util

import "strings"

type Stack[T any] struct{ items []T }

func (s *Stack[T]) Push(v T) { s.items = append(s.items, v) }
func (s *Stack[T]) Pop() T {
	v := s.items[len(s.items)-1]
	s.items = s.items[:len(s.items)-1]
	return v
}
func (s *Stack[T]) Len() int { return len(s.items) }

type Namer interface{ Name() string }
type Greeter struct{ Who string }

func (g Greeter) Name() string { return g.Who }
func Shout(n Namer) string     { return strings.ToUpper(n.Name()) + "!" }
func Map[T, U any](in []T, f func(T) U) []U {
	out := make([]U, 0, len(in))
	for _, v := range in {
		out = append(out, f(v))
	}
	return out
}

type Getter[T any] interface{ Get() T }
type Box[T any] struct {
	V     T
	Label interface{ Name() string }
}

func (b Box[T]) Get() T             { return b.V }
func NewBox(s string) Box[string]   { return Box[string]{V: s} }
func Unbox(g Getter[string]) string { return g.Get() }
`}
	// A native Go struct: guest aggregates that contain it need physical
	// layouts, which the compiler derives and caches per compile.
	scalar := &values.Shape{Type: values.StringType, Kind: values.StringKind, Cells: 1, Native: nativebind.Layout(reflect.TypeFor[string](), nil, true)}
	nativeType := reflect.TypeFor[fixtureIndex[string]]()
	index := &values.Shape{Type: values.NewType("nat.Index[string]", values.Pointer), Kind: values.StructKind, Cells: 1, Fields: []values.Field{{Name: "Key", Shape: scalar}}, Native: nativebind.Layout(nativeType, nativebind.BindFields(nativeType, nil, nativebind.Automatic), false)}
	indexPtr := values.NewPointerShape(index)
	indexPtr.Type = values.NewType("*nat.Index[string]", values.Pair)
	indexPtr.Native = nativebind.Layout(reflect.PointerTo(nativeType), nil, true)
	shared := &fixtureIndex[string]{Key: "original"}
	nat := evm.Package{Path: "nat", Source: "package nat\ntype Index[K any] struct { Key K }\n", Types: []evm.TypeDecl{{SourceName: "Index", TypeArgs: []*values.Type{values.StringType}, Shape: index}, {Shape: indexPtr}}, Values: []evm.ExternalValue{{Name: "ByName", Type: indexPtr.Type, Read: func(c *evm.Call) error {
		c.SetResult(0, values.Value{R: values.NativeAddress, P: unsafe.Pointer(shared)})
		return nil
	}}}}
	f.packages = []evm.Package{db, demo, host, util, nat}
	return f
}

type fixtureIndex[K any] struct{ Key K }

type environmentCase struct {
	name    string
	sources map[string]string
	want    string // result of Run
	wantErr string // substring of the compile error
}

var environmentCorpus = []environmentCase{
	{name: "basics", want: "15 3 [1 4 9] recovered:boom circle:9 b,a", sources: map[string]string{"main.go": `import ("fmt";"strings")
type Shape interface{ Area() int }
type Circle struct{ R int }
func (c Circle) Area() int { return c.R * c.R }
type Pair[K comparable, V any] struct{ Key K; Value V }
func sum(xs ...int) (total int) { for _, x := range xs { total += x }; return }
func safe() (msg string) { defer func() { if r := recover(); r != nil { msg = fmt.Sprint("recovered:", r) } }(); panic("boom") }
func describe(v any) string { switch v := v.(type) { case Shape: return fmt.Sprint("circle:", v.Area()); case string: return v }; return "?" }
func Run() string {
	m := map[string]int{"a": 1, "b": 2, "c": 3}
	squares := []int{}
	for i := 1; i <= 3; i++ { squares = append(squares, i*i) }
	pairs := []Pair[string, int]{{"b", 2}, {"a", 1}}
	names := make([]string, 0, len(pairs))
	for _, p := range pairs { names = append(names, p.Key) }
	return fmt.Sprint(sum(1, 2, 3, 4, 5), " ", len(m), " ", squares, " ", safe(), " ", describe(Circle{3}), " ", strings.Join(names, ","))
}`}},
	{name: "stdlib", want: "42 1.41 HELLO true 3.5", sources: map[string]string{"main.go": `import ("errors";"fmt";"math";"strconv";"strings")
func Run() string {
	n, err := strconv.Atoi("41")
	if err != nil { panic(err) }
	e := errors.New("bad")
	return fmt.Sprintf("%d %.2f %s %v %s", n+1, math.Sqrt(2), strings.ToUpper("Hello"), errors.Is(fmt.Errorf("wrapped: %w", e), e), strconv.FormatFloat(3.5, 'f', -1, 64))
}`}},
	{name: "native", want: "hello/7/2 second/0/0 nested", sources: map[string]string{"main.go": `package example
import d "example.test/demo"
func Run() string {
	first := &d.Entry{Title: "hello", Count: 7, Mode: d.On, Child: &d.Child{Text: "nested"}}
	entries := []*d.Entry{first, {Title: "second"}}
	return d.Describe(entries[0]) + " " + d.Describe(entries[1]) + " " + first.Child.Text
}`}},
	{name: "generic native", want: "first 7 42", sources: map[string]string{"main.go": `import ("fmt";"host")
type ID uint64
func Run() string { f := host.First[int]; return fmt.Sprint(host.First("first", "second"), " ", host.First(ID(7), ID(8)), " ", f(42, 8)) }`}},
	{name: "source package", want: "3 c 2 GUEST! HOST! [2 4 6] {x 1}", sources: map[string]string{"main.go": `import ("fmt";"example.test/util")
type Mine struct{ Label string; N int }
type named string
func (n named) Name() string { return string(n) }
func Run() string {
	var letters util.Stack[string]
	letters.Push("a"); letters.Push("b"); letters.Push("c")
	n := letters.Len()
	top := letters.Pop()
	var mine util.Stack[Mine]
	mine.Push(Mine{"x", 1})
	doubled := util.Map([]int{1, 2, 3}, func(v int) int { return v * 2 })
	return fmt.Sprint(n, " ", top, " ", letters.Len(), " ", util.Shout(named("guest")), " ", util.Shout(util.Greeter{Who: "host"}), " ", doubled, " ", mine.Pop())
}`}},
	{name: "generic instances", want: "boxed guest 2 true", sources: map[string]string{"main.go": `import ("fmt";"example.test/util")
type mine struct{}
func (mine) Get() string { return "guest" }
func Run() string {
	box := util.NewBox("boxed")
	var g util.Getter[string] = box
	numbers := util.Box[int]{V: 2}
	_, labelled := any(box.Label).(util.Namer)
	return fmt.Sprint(util.Unbox(g), " ", util.Unbox(mine{}), " ", numbers.Get(), " ", !labelled)
}`}},
	{name: "native storage", want: "copy b! copy original 2", sources: map[string]string{"main.go": `import ("fmt";"nat")
type Wrapped struct{ I nat.Index[string]; N int }
type Indexes []nat.Index[string]
func Run() string {
	x := *nat.ByName
	x.Key = "copy"
	w := Wrapped{I: x, N: 2}
	pair := [2]nat.Index[string]{{Key: "a"}, {Key: "b"}}
	p := &pair[1]
	p.Key += "!"
	list := []Wrapped{w}
	all := Indexes{x}
	return fmt.Sprint(w.I.Key, " ", pair[1].Key, " ", list[0].I.Key, " ", nat.ByName.Key, " ", len(all)+len(list))
}`}},
	{name: "records", want: "records", sources: map[string]string{"main.go": `import "db"
func Change(c *db.Edit) { b := c.bar; b.boz += 3; c.total, b.boz = b.boz, c.total }
func Read(c *db.Customer) int64 { copy := *c; return copy.total*10 + c.bar.boz }
func Run() string { return "records" }`}},
	// The guest reuses the host's package and type names, a host source
	// filename, and builds composite and embedding types around host types.
	{name: "host names", want: "guest 2 1 3 inner/5/0 4", sources: map[string]string{"evm/_stdlib/host/host.go": `package db
import ("fmt";demo "example.test/demo";util "example.test/util")
type Customer struct{ Name string }
type Entry struct{ demo.Entry; Extra int }
type Stack[T any] struct{ util.Stack[T] }
type Entries []*demo.Entry
func First[T any](a, b T) T { return b }
func Run() string {
	byName := map[string]*demo.Entry{"a": {Title: "a"}, "b": {Title: "b"}}
	children := [3]demo.Child{{Text: "x"}}
	wrapped := Entry{Entry: demo.Entry{Title: "inner", Count: 5}, Extra: 4}
	var stack Stack[Customer]
	stack.Push(Customer{"guest"})
	list := Entries{{Title: "only"}}
	return fmt.Sprint(stack.Pop().Name, " ", len(byName), " ", len(list), " ", len(children), " ", demo.Describe(&wrapped.Entry), " ", First(3, wrapped.Extra))
}`}},
	{name: "files", want: "7 two", sources: map[string]string{"a.go": `package plugin
import "fmt"
var counter = initial()
func initial() int { return 5 }
func init() { counter += 2 }
func Run() string { return fmt.Sprint(counter, " ", helper()) }`, "b.go": `package plugin
import "strings"
func helper() string { return strings.ToLower("TWO") }`}},
	{name: "type error", wantErr: `cannot use "text"`, sources: map[string]string{"main.go": `func Run() string { var n int = "text"; return "" }`}},
	{name: "unexposed import", wantErr: `package "os" is not exposed`, sources: map[string]string{"main.go": `import "os"
func Run() string { return os.Args[0] }`}},
	{name: "method on host type", wantErr: "cannot define new methods on non-local type", sources: map[string]string{"main.go": `import "example.test/demo"
func (e *demo.Entry) Rename(s string) { e.Title = s }
func Run() string { return "" }`}},
	{name: "read-only record", wantErr: "cannot assign", sources: map[string]string{"main.go": `import "db"
func Change(c *db.Customer) { c.total = 1 }
func Run() string { return "" }`}},
	{name: "syntax error", wantErr: "expected", sources: map[string]string{"main.go": `func Run() string { return `}},
}

func runEnvironmentModule(t testing.TB, f *environmentFixture, m *evm.Module) string {
	t.Helper()
	p, err := evm.Link(m, f.packages...)
	if err != nil {
		t.Fatal(err)
	}
	vm := evm.New(p)
	out := make([]values.Value, 1)
	if err := vm.Run(context.Background(), "Run", nil, out); err != nil {
		t.Fatal(err)
	}
	result := out[0].AsString()
	if slices.ContainsFunc(m.Functions, func(fn evm.Function) bool { return fn.Name == "Read" }) {
		ctx, scope := evm.WithRecords(context.Background())
		defer scope.Close()
		edit := row2.Edit(row2.Empty(row2.Compact2))
		edit.Set([]row2.Step{{Key: f.total}}, values.Int(7), row2.Signed)
		if err := vm.Run(ctx, "Change", []values.Value{scope.Edit(f.customer, edit)}, nil); err != nil {
			t.Fatal(err)
		}
		if err := vm.Run(ctx, "Read", []values.Value{scope.Read(f.customer, edit.Snapshot())}, out); err != nil {
			t.Fatal(err)
		}
		result += fmt.Sprint(" read=", out[0].AsInt())
	}
	return result
}

// A module compiled through a prepared environment must equal the module the
// package-list entry points produce, and behave the same.
func TestEnvironmentMatchesPackageList(t *testing.T) {
	f := newEnvironmentFixture()
	env, err := evmcompiler.NewEnvironment(f.packages...)
	if err != nil {
		t.Fatal(err)
	}
	for _, options := range []evmcompiler.Options{{}, {NoOptimization: true}} {
		for _, c := range environmentCorpus {
			t.Run(fmt.Sprintf("%s/noopt=%v", c.name, options.NoOptimization), func(t *testing.T) {
				lazy, lazyErr := options.CompileFiles(c.sources, f.packages...)
				for round := 0; round < 2; round++ { // The second compile sees what the first left behind.
					prepared, err := env.CompileFiles(c.sources, options)
					if fmt.Sprint(err) != fmt.Sprint(lazyErr) {
						t.Fatalf("round %d: errors differ\npackage list: %v\nprepared:     %v", round, lazyErr, err)
					}
					if c.wantErr != "" {
						if err == nil || !strings.Contains(err.Error(), c.wantErr) {
							t.Fatalf("error = %v, want %q", err, c.wantErr)
						}
						continue
					}
					if err != nil {
						t.Fatal(err)
					}
					if difference := evmcompiler.ModuleDifference(lazy, prepared); difference != "" {
						t.Fatalf("round %d: %s", round, difference)
					}
					want := c.want
					if c.name == "records" {
						want += " read=37"
					}
					if got := runEnvironmentModule(t, f, lazy); got != want {
						t.Fatalf("package list result = %q, want %q", got, want)
					}
					if got := runEnvironmentModule(t, f, prepared); got != want {
						t.Fatalf("round %d: prepared result = %q, want %q", round, got, want)
					}
				}
			})
		}
	}
	single, err := env.Compile("main.go", environmentCorpus[0].sources["main.go"], evmcompiler.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := runEnvironmentModule(t, f, single); got != environmentCorpus[0].want {
		t.Fatalf("Compile result = %q", got)
	}
}

// Many goroutines compile different sources on one environment. Run with
// -race: the compiles share every host type, shape and syntax tree.
func TestEnvironmentConcurrentCompiles(t *testing.T) {
	f := newEnvironmentFixture()
	env, err := evmcompiler.NewEnvironment(f.packages...)
	if err != nil {
		t.Fatal(err)
	}
	type reference struct {
		listing, err, result string
	}
	references := make([]reference, len(environmentCorpus))
	for i, c := range environmentCorpus {
		m, err := env.CompileFiles(c.sources, evmcompiler.Options{})
		if err != nil {
			references[i].err = err.Error()
			continue
		}
		references[i] = reference{listing: m.Disassemble(), result: runEnvironmentModule(t, f, m)}
	}
	const goroutines, rounds = 16, 3
	var wg sync.WaitGroup
	failures := make(chan string, goroutines*rounds*len(environmentCorpus))
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := 0; round < rounds; round++ {
				for step := range environmentCorpus {
					i := (step + g) % len(environmentCorpus)
					c := environmentCorpus[i]
					m, err := env.CompileFiles(c.sources, evmcompiler.Options{})
					if err != nil {
						if err.Error() != references[i].err {
							failures <- fmt.Sprintf("%s: error %v, want %q", c.name, err, references[i].err)
						}
						continue
					}
					if references[i].err != "" {
						failures <- fmt.Sprintf("%s: compiled, want error %q", c.name, references[i].err)
						continue
					}
					if m.Disassemble() != references[i].listing {
						failures <- fmt.Sprintf("%s: disassembly differs from the sequential compile", c.name)
					}
					// Analyze shares the facade universe the same way.
					files := env.NewFileSet()
					var syntax []*ast.File
					for _, name := range slices.Sorted(maps.Keys(c.sources)) {
						source := c.sources[name]
						if !strings.HasPrefix(strings.TrimSpace(source), "package") {
							source = "package plugin\n" + source
						}
						file, err := parser.ParseFile(files, name, source, parser.SkipObjectResolution)
						if err != nil {
							failures <- fmt.Sprintf("%s: %v", c.name, err)
							continue
						}
						syntax = append(syntax, file)
					}
					if _, info, err := env.AnalyzeFiles(files, syntax, evmcompiler.Options{}); info == nil {
						failures <- fmt.Sprintf("%s: analyze: %v", c.name, err)
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

// Host types have one identity for every compile on an environment: the
// native types the host declared, the storage shapes built for them, and the
// go/types objects sources are checked against.
func TestEnvironmentTypeIdentity(t *testing.T) {
	f := newEnvironmentFixture()
	env, err := evmcompiler.NewEnvironment(f.packages...)
	if err != nil {
		t.Fatal(err)
	}
	shapes := func(source string) map[string]*values.Shape {
		m, err := env.Compile("main.go", source, evmcompiler.Options{})
		if err != nil {
			t.Fatal(err)
		}
		described := false
		for _, imp := range m.Imports {
			if imp.Name == "Describe" {
				described = true
				if imp.Signature.Params[0] != f.entryPtr.Type {
					t.Fatalf("Describe takes %v, want the declared pointer type", imp.Signature.Params[0].Name())
				}
			}
		}
		if !described {
			t.Fatal("Describe is not imported")
		}
		byName := make(map[string]*values.Shape)
		for _, s := range m.Shapes {
			byName[s.Type.Name()] = s
		}
		return byName
	}
	first := shapes(`import "example.test/demo"
func Run() string { return demo.Describe(&demo.Entry{Title: "a"}) }`)
	second := shapes(`import d "example.test/demo"
type Local struct{ E d.Entry }
func Run() string { l := Local{}; return d.Describe(&l.E) + d.Describe(&d.Entry{Child: &d.Child{}}) }`)
	for _, name := range []string{"demo.Entry", "demo.Child", "demo.Mode", f.entryPtr.Type.Name()} {
		if first[name] == nil || first[name] != second[name] {
			t.Errorf("shape %s: %p and %p, want one shared shape", name, first[name], second[name])
		}
	}
	if first["demo.Entry"].Type != f.entry.Type {
		t.Error("demo.Entry lost its declared native type")
	}

	object := func(source string) (types.Object, *types.Package) {
		files := env.NewFileSet()
		file, err := parser.ParseFile(files, "main.go", source, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		pkg, info, err := env.Analyze(files, file, evmcompiler.Options{})
		if err != nil {
			t.Fatal(err)
		}
		for id, obj := range info.Uses {
			if id.Name == "Entry" {
				if position := files.Position(obj.Pos()); position.IsValid() {
					t.Errorf("native type has position %v", position)
				}
				return obj, pkg.Imports()[0]
			}
		}
		t.Fatal("Entry is not used")
		return nil, nil
	}
	entryA, demoA := object("package a\nimport \"example.test/demo\"\nvar X demo.Entry\n")
	entryB, demoB := object("package b\nimport d \"example.test/demo\"\nfunc F(e *d.Entry) string { return e.Title }\n")
	if entryA != entryB || entryA.Type() != entryB.Type() {
		t.Error("two analyses see different demo.Entry types")
	}
	if demoA != demoB {
		t.Error("two analyses import different demo packages")
	}
}

// Guest source can reuse host names and wrap host types in its own, and can
// fail in every phase. None of it may change what later compiles see.
func TestEnvironmentIsUnchangedByCompiles(t *testing.T) {
	f := newEnvironmentFixture()
	env, err := evmcompiler.NewEnvironment(f.packages...)
	if err != nil {
		t.Fatal(err)
	}
	before := env.Fingerprint()
	if !strings.Contains(before, "package example.test/demo") || !strings.Contains(before, "method func (*example.test/util.Stack[T]).Push(v T)") {
		t.Fatalf("fingerprint misses the host packages:\n%s", before)
	}
	reference, err := env.CompileFiles(environmentCorpus[0].sources, evmcompiler.Options{})
	if err != nil {
		t.Fatal(err)
	}
	hostile := []map[string]string{
		// Packages named and "located" like host packages.
		{"evm/_stdlib/example.test/util/util.go": "package util\ntype Stack[T any] struct{}\nfunc (s *Stack[T]) Push(v T) {}\nfunc Shout() string { return \"guest\" }\nfunc Run() string { return Shout() }\n"},
		{"example.test/demo/demo.go": "package demo\ntype Entry struct{ Hijacked bool }\ntype Mode string\nconst On Mode = \"guest\"\nfunc Describe(e *Entry) string { return \"guest\" }\nfunc Run() string { return Describe(&Entry{}) }\n"},
		// Dot imports put host objects in the guest's file scope.
		{"main.go": "import . \"example.test/demo\"\nimport . \"example.test/util\"\nfunc Run() string { var s Stack[*Entry]; s.Push(&Entry{Title: \"x\"}); return Describe(s.Pop()) + Shout(Greeter{\"g\"}) }\n"},
		{"main.go": "import . \"example.test/demo\"\ntype Entry int\nfunc Run() string { return \"\" }\n"},
		// Instances of host generics with guest and host arguments.
		{"main.go": "import (\"example.test/util\"; \"example.test/demo\"; \"db\")\ntype T struct{ N int }\ntype Deep = util.Stack[util.Stack[map[T][]*demo.Entry]]\nfunc Run() string { var d Deep; var c util.Stack[*db.Customer]; var g util.Stack[util.Greeter]; g.Push(util.Greeter{\"a\"}); _ = c.Len(); return util.Shout(g.Pop()) + string(rune('0'+d.Len())) }\n"},
		{"main.go": "import \"example.test/util\"\nfunc Run() string { n := util.Map([]string{\"a\"}, func(s string) util.Stack[string] { var st util.Stack[string]; st.Push(s); return st }); return n[0].Pop() }\n"},
		// Aliases and new types over host types, including model rows.
		{"main.go": "import (\"db\"; \"example.test/demo\")\ntype Row = db.Customer\ntype E demo.Entry\ntype P *demo.Entry\ntype Rows [4]demo.Entry\nfunc Run() string { var r Rows; e := E(r[0]); var p P = &r[1]; _ = p; return e.Title }\n"},
		{"main.go": "import \"db\"\ntype Mine db.Customer\nfunc Run() string { return \"\" }\n"},
		// Interfaces that host types implement, and host interfaces guest types implement.
		{"main.go": "import (\"fmt\"; \"example.test/util\")\ntype Lener interface{ Len() int }\ntype me struct{}\nfunc (me) Name() string { return \"me\" }\nfunc Run() string { var l Lener = &util.Stack[int]{}; var n util.Namer = me{}; return fmt.Sprint(l.Len(), util.Shout(n), fmt.Sprintf(\"%v\", util.Greeter{\"g\"})) }\n"},
	}
	for _, c := range environmentCorpus {
		hostile = append(hostile, c.sources)
	}
	for _, sources := range hostile {
		for _, options := range []evmcompiler.Options{{}, {NoOptimization: true}} {
			env.CompileFiles(sources, options) // Many of these fail; that is part of the test.
		}
		files := env.NewFileSet()
		var syntax []*ast.File
		for _, name := range slices.Sorted(maps.Keys(sources)) {
			source := sources[name]
			if !strings.HasPrefix(source, "package") {
				source = "package plugin\n" + source
			}
			if file, err := parser.ParseFile(files, name, source, parser.SkipObjectResolution); err == nil {
				syntax = append(syntax, file)
			}
		}
		if len(syntax) != 0 {
			env.AnalyzeFiles(files, syntax, evmcompiler.Options{})
		}
	}
	if after := env.Fingerprint(); after != before {
		a, b := strings.Split(before, "\n"), strings.Split(after, "\n")
		for i := range min(len(a), len(b)) {
			if a[i] != b[i] {
				t.Fatalf("compiles changed the environment at line %d:\nbefore: %s\nafter:  %s", i+1, a[i], b[i])
			}
		}
		t.Fatalf("compiles changed the environment: %d lines before, %d after", len(a), len(b))
	}
	again, err := env.CompileFiles(environmentCorpus[0].sources, evmcompiler.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if again.Disassemble() != reference.Disassemble() {
		t.Fatal("a compile differs after the hostile sources")
	}
	if got := runEnvironmentModule(t, f, again); got != environmentCorpus[0].want {
		t.Fatalf("result after the hostile sources = %q", got)
	}
}

// A prepared environment imports everything up front, so a package that
// cannot be imported fails NewEnvironment even though no source uses it. The
// package-list entry points still fail only the source that imports it.
func TestEnvironmentRejectsBrokenPackage(t *testing.T) {
	broken := evm.Package{Path: "broken", Source: "package broken\nfunc F() Missing\n"}
	if _, err := evmcompiler.NewEnvironment(broken); err == nil || !strings.Contains(err.Error(), `import "broken"`) || !strings.Contains(err.Error(), "undefined: Missing") {
		t.Fatalf("NewEnvironment error = %v", err)
	}
	if _, err := evmcompiler.Compile("main.go", "func Run() int { return 1 }", broken); err != nil {
		t.Fatalf("a source that does not import the broken package: %v", err)
	}
	if _, err := evmcompiler.Compile("main.go", "import \"broken\"\nfunc Run() { broken.F() }", broken); err == nil || !strings.Contains(err.Error(), "undefined: Missing") {
		t.Fatalf("a source that imports the broken package: %v", err)
	}
	if _, err := evmcompiler.NewEnvironment(evm.Package{Path: "fmt"}); err == nil || !strings.Contains(err.Error(), "reserved virtual package") {
		t.Fatalf("reserved package error = %v", err)
	}
}

// Host sources keep their positions in every file set of the environment,
// and syntax from an unrelated file set is refused instead of misreported.
func TestEnvironmentFileSet(t *testing.T) {
	f := newEnvironmentFixture()
	env, err := evmcompiler.NewEnvironment(f.packages...)
	if err != nil {
		t.Fatal(err)
	}
	const source = "package p\nimport \"example.test/util\"\nvar S util.Stack[int]\nvar G = util.Greeter{}\n"
	positions := make([]string, 2)
	for i := range positions {
		files := env.NewFileSet()
		file, err := parser.ParseFile(files, "main.go", source, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		_, info, err := env.Analyze(files, file, evmcompiler.Options{})
		if err != nil {
			t.Fatal(err)
		}
		for id, obj := range info.Uses {
			if id.Name == "Greeter" {
				positions[i] = files.Position(obj.Pos()).String()
			}
		}
		if got := files.Position(file.Pos()).String(); got != "main.go:1:1" {
			t.Fatalf("guest position = %s", got)
		}
	}
	if positions[0] != "evm/_stdlib/example.test/util/util.go:16:6" || positions[1] != positions[0] {
		t.Fatalf("host positions = %q", positions)
	}

	foreign := token.NewFileSet()
	file, err := parser.ParseFile(foreign, "main.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.Analyze(foreign, file, evmcompiler.Options{}); err == nil || !strings.Contains(err.Error(), "NewFileSet") {
		t.Fatalf("foreign file set error = %v", err)
	}
	if _, _, err := evmcompiler.Analyze(foreign, file, f.packages...); err != nil {
		t.Fatalf("package-list Analyze with its own file set: %v", err)
	}

	if got := len(env.Packages()); got != len(f.packages) {
		t.Fatalf("Packages() has %d packages, want %d", got, len(f.packages))
	}
}

// A dynamic result type that no declaration names is declared only when
// lowering reaches its function, so source cannot name it. A prepared
// environment resolves it up front and must keep it just as unnameable.
func TestEnvironmentLateDynamicTypes(t *testing.T) {
	token := values.NewType("host.Token", values.Raw)
	host := evm.Package{Path: "host", Functions: []evm.ExternalFunction{{Name: "Make", Signature: evm.Signature{Results: []*values.Type{values.AnyType}}, DynamicTypes: []*values.Type{token}, Call: func(c *evm.Call) error {
		return c.SetInterface(0, token, values.Int(7))
	}}}}
	env, err := evmcompiler.NewEnvironment(host)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ source, wantErr string }{
		{"import \"host\"\nfunc Run() bool { v := host.Make(); _, isInt := v.(int64); return v != nil && !isInt }", ""},
		{"import \"host\"\nfunc Run() bool { var t host.Token; return t == 0 }", "undefined: host.Token"},
	} {
		lazy, lazyErr := evmcompiler.Compile("main.go", c.source, host)
		prepared, err := env.Compile("main.go", c.source, evmcompiler.Options{})
		if fmt.Sprint(err) != fmt.Sprint(lazyErr) {
			t.Fatalf("errors differ\npackage list: %v\nprepared:     %v", lazyErr, err)
		}
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error = %v, want %q", err, c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if difference := evmcompiler.ModuleDifference(lazy, prepared); difference != "" {
			t.Fatal(difference)
		}
		for _, m := range []*evm.Module{lazy, prepared} {
			p, err := evm.Link(m, host)
			if err != nil {
				t.Fatal(err)
			}
			out := make([]values.Value, 1)
			if err := evm.New(p).Run(context.Background(), "Run", nil, out); err != nil || out[0].R != 1 {
				t.Fatalf("Run = %v, %v", out[0], err)
			}
		}
	}
}
