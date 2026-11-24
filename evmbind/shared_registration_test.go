package evmbind_test

import (
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

type IntegrationDescriptor struct {
	Build1 *evm.FunctionOf[func()]
}

// Model the two host registrations separately: Initialize installs a builder,
// then the host runs Build1 to construct the receiver and install its hooks.
func TestIntegrationHookRegistration(t *testing.T) {
	var descriptor *IntegrationDescriptor
	var hook *evm.Callable
	schema, err := evmbind.New("registry", fstest.MapFS{"api.go": {Data: []byte(`package registry
import "github.com/andreyvit/evm"
type IntegrationDescriptor struct { Build1 *evm.FunctionOf[func()] }
func RegisterIntegration(d *IntegrationDescriptor)
func AddHook(id string, hook *evm.ConcurrentFunctionOf[func(string) int])
`)}}, evmbind.Options{Selective: true, Types: []reflect.Type{reflect.TypeFor[IntegrationDescriptor]()}, Functions: map[string]any{
		"RegisterIntegration": func(d *IntegrationDescriptor) { descriptor = d },
		"AddHook":             func(id string, fn *evm.Callable) { hook = fn },
	}})
	if err != nil {
		t.Fatal(err)
	}
	pkg := schema.Bind(nil)
	const plugin = `import "registry"
type settings struct { byID map[string]int }
type myIntegration struct { cfg *settings }
func Initialize() { registry.RegisterIntegration(&registry.IntegrationDescriptor{Build1: build}) }
func build() {
	i := &myIntegration{cfg: &settings{byID: map[string]int{}}}
	i.cfg.byID["vip"] = 3
	registry.AddHook("orders", i.OnOrder)
}
func write(m map[string]int) { m["vip"]++ }
func (i *myIntegration) OnOrder(id string) int { BODY }
`
	for _, tc := range []struct {
		name, body string
		allowed    bool
	}{
		{"read and local write", `local := map[string]int{}; local[id] = i.cfg.byID[id]; return local[id]`, true},
		{"replace receiver field", `i.cfg = &settings{}; return 0`, false},
		{"write through nested map", `i.cfg.byID[id]++; return 0`, false},
		{"write through helper", `alias := i.cfg.byID; write(alias); return 0`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := evmcompiler.Compile("plugin.go", strings.Replace(plugin, "BODY", tc.body, 1), pkg)
			if !tc.allowed {
				if err == nil || !strings.Contains(err.Error(), "concurrent callback") {
					t.Fatalf("expected concurrent callback diagnostic, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			p, err := evm.Link(m, pkg)
			if err != nil {
				t.Fatal(err)
			}
			init, err := p.LookupFunction("Initialize", nil)
			if err != nil {
				t.Fatal(err)
			}
			run := func(fn evm.Callable, slots []values.Value) {
				t.Helper()
				vm := p.AcquireVM(t.Context())
				defer vm.Release()
				if err := vm.Run(fn, slots); err != nil {
					t.Fatal(err)
				}
			}
			run(init, nil)
			if descriptor == nil || descriptor.Build1 == nil {
				t.Fatal("missing builder")
			}
			run(*descriptor.Build1, nil)
			if hook == nil {
				t.Fatal("missing hook")
			}
			slots := [2]values.Value{values.String("vip")}
			run(*hook, slots[:])
			if slots[1].R != 3 {
				t.Fatal(slots)
			}
		})
	}
}

type BorrowedFoo interface {
	Read() int
	Title() string
}
type FooRegistry interface{ Register(BorrowedFoo) }
type testFooRegistry struct{ kept BorrowedFoo }

func (r *testFooRegistry) Register(foo BorrowedFoo) { r.kept = foo }

type fooAdapter struct {
	read  func() int
	title func() string
}

func (f fooAdapter) Read() int     { return f.read() }
func (f fooAdapter) Title() string { return f.title() }

func TestConcurrentGuestInterface(t *testing.T) {
	registry := &testFooRegistry{}
	var program *evm.Program
	source := fstest.MapFS{"api.go": {Data: []byte(`package reg
type BorrowedFoo interface { Read() int; Title() string }
type FooRegistry interface {
    //evm:concurrent impl
    Register(impl BorrowedFoo)
}
func Registry() FooRegistry
//evm:concurrent impl
func Register(impl BorrowedFoo)
`)}}
	adapter := func(methods map[string]evm.Callable) any {
		invoke := func(method string) values.Value {
			t.Helper()
			vm := program.AcquireVM(t.Context())
			defer vm.Release()
			var result [1]values.Value
			if err := vm.Run(methods[method], result[:]); err != nil {
				t.Fatal(err)
			}
			return result[0]
		}
		return fooAdapter{read: func() int { return int(invoke("Read").R) }, title: func() string { return invoke("Title").AsString() }}
	}
	opts := evmbind.Options{
		Selective:         true,
		Types:             []reflect.Type{reflect.TypeFor[BorrowedFoo](), reflect.TypeFor[FooRegistry]()},
		Functions:         map[string]any{"Register": registry.Register, "Registry": func() FooRegistry { return registry }},
		Implementations:   []evmbind.Implementation{evmbind.Pointer[testFooRegistry]()},
		InterfaceAdapters: map[reflect.Type]func(map[string]evm.Callable) any{reflect.TypeFor[BorrowedFoo](): adapter},
	}
	generated, err := evmbind.Generate("reg", "example/reg", "bindings", source, opts)
	if err != nil || !strings.Contains(string(generated), "ConcurrentInterfaceArgument[") {
		t.Fatalf("generated adapter call missing: %v", err)
	}
	schema, err := evmbind.New("reg", source, opts)
	if err != nil {
		t.Fatal(err)
	}
	pkg := schema.Bind(nil)
	for _, tc := range []struct{ name, read, title, after, compileErr, runErr string }{
		{"retained receiver", "return b.n", `return "ok"`, "", "", ""},
		{"interface method registration", "return b.n", `return "ok"`, "reg.Registry().Register(b)", "", ""},
		{"repeat registration", "return b.n", `return "ok"`, "reg.Register(b)", "", ""},
		{"method write", "b.n++;return b.n", `return "ok"`, "", "concurrent callback", ""},
		{"second method writes", "return b.n", `b.n++;return "ok"`, "", "concurrent callback", ""},
		{"later write", "return b.n", `return "ok"`, "b.n++", "after a concurrent borrow", ""},
		{"unchecked site", "return b.n", `return "ok"`, "other:=&Bar{};indirect(reg.Register,other)", "", "compiler-checked borrow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := `import "reg"
type Bar struct{n int}
func(b *Bar) Read() int {` + tc.read + `}
func(b *Bar) Title() string {` + tc.title + `}
func indirect(registrar func(reg.BorrowedFoo), value reg.BorrowedFoo) {registrar(value)}
func Run(){b:=&Bar{4};reg.Register(b);` + tc.after + `}`
			m, err := evmcompiler.Compile("impl.go", src, pkg)
			if tc.compileErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.compileErr) {
					t.Fatalf("want %s, got %v", tc.compileErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			program, err = evm.Link(m, pkg)
			if err != nil {
				t.Fatal(err)
			}
			fn, err := program.LookupFunction("Run", nil)
			if err != nil {
				t.Fatal(err)
			}
			vm := program.AcquireVM(t.Context())
			err = vm.Run(fn, nil)
			vm.Release()
			if tc.runErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.runErr) {
					t.Fatalf("want %s, got %v", tc.runErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if kept := registry.kept; kept == nil || kept.Read() != 4 || kept.Title() != "ok" {
				t.Fatal("adapter lost guest receiver")
			}
		})
	}
}

type HostFrame interface{ Render() string }
type HostSetup interface {
	AddFrame(name string) HostFrame
	AddHook(hook *evm.ConcurrentFunctionOf[func(int) any])
	Note(text string)
}
type HostOptions struct{ Limit int }
type testHostFrame struct{}

func (*testHostFrame) Render() string { return "" }

type testHostSetup struct{}

func (*testHostSetup) AddFrame(string) HostFrame                        { return &testHostFrame{} }
func (*testHostSetup) AddHook(*evm.ConcurrentFunctionOf[func(int) any]) {}
func (*testHostSetup) Note(string)                                      {}

// The host answers for native code. The compiler still checks guest code, and
// rejects the calls that hand native code a way to write shared guest state.
func TestConcurrentCallbackNativeCalls(t *testing.T) {
	schema, err := evmbind.New("host", fstest.MapFS{"api.go": {Data: []byte(`package host
import "github.com/andreyvit/evm"
type HostFrame interface { Render() string }
type HostSetup interface {
	AddFrame(name string) HostFrame
	AddHook(hook *evm.ConcurrentFunctionOf[func(int) any])
	Note(text string)
}
type HostOptions struct { Limit int }
func Fill(p *int)
func Total(xs []int) int
func Describe(o *HostOptions) string
`)}}, evmbind.Options{Selective: true,
		Types:           []reflect.Type{reflect.TypeFor[HostFrame](), reflect.TypeFor[HostSetup](), reflect.TypeFor[HostOptions]()},
		Implementations: []evmbind.Implementation{evmbind.Pointer[testHostFrame](), evmbind.Pointer[testHostSetup]()},
		Functions: map[string]any{
			"Fill":     func(p *int) { *p = 1 },
			"Total":    func(xs []int) int { return len(xs) },
			"Describe": func(o *HostOptions) string { return "" },
		}})
	if err != nil {
		t.Fatal(err)
	}
	pkg := schema.Bind(nil)
	for _, tc := range []struct{ name, body, want string }{
		{"method of a captured native handle", `s.AddHook(func(int) any { return frame.Render() })`, ""},
		{"captured pointer returned to the host", `s.AddHook(func(int) any { return c })`, ""},
		{"captured slice returned to the host", `s.AddHook(func(int) any { return c.xs })`, ""},
		{"shared data passed to native functions", `s.AddHook(func(int) any { return host.Total(c.xs) + len(host.Describe(c.o)) })`, ""},
		{"native call during setup", `_ = host.Describe(c.o); s.AddHook(func(int) any { return c.o.Limit })`, ""},
		{"helper result read", `s.AddHook(func(int) any { return pick(c)[0] })`, ""},
		{"helper result written", `s.AddHook(func(int) any { xs := pick(c); xs[0] = 1; return nil })`, "concurrent callback writes"},
		{"pointer to a shared field passed to native code", `s.AddHook(func(int) any { host.Fill(&c.n); return nil })`, "cannot prove read-only use"},
		{"pointer to a shared field retained during setup", `host.Fill(&c.n); s.AddHook(func(int) any { return c.n })`, "earlier call may retain a mutable alias"},
		{"registration object used later", `s.AddHook(func(int) any { s.Note("late"); return nil })`, "host.HostSetup it is registered on"},
		{"guest type behind a native interface", `var f host.HostFrame = &myFrame{}; s.AddHook(func(int) any { return f.Render() })`, "cannot prove read-only use"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := evmcompiler.Compile("plugin.go", `import "host"
type C struct{n int; xs []int; o *host.HostOptions}
type myFrame struct{n int}
func (f *myFrame) Render() string { f.n++; return "" }
func pick(c *C) []int { return c.xs }
func Initialize(s host.HostSetup) { c := &C{xs: []int{1}, o: &host.HostOptions{}}; frame := s.AddFrame("a"); _, _ = c, frame; `+tc.body+` }`, pkg)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}
