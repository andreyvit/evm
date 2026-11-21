package evmbind_test

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
)

type NativeUnary func(int) int
type NativeFunctionBox struct{ Apply NativeUnary }
type nativeFunctionFieldHost struct{ box NativeFunctionBox }

func (h *nativeFunctionFieldHost) Box(_ *evm.VM) *NativeFunctionBox { return &h.box }
func (h *nativeFunctionFieldHost) Apply(_ *evm.VM, n int) int       { return h.box.Apply(n) }

// A native page's ViewFunc must remain callable after its producer returns.
func TestNativeFunctionFieldRetainsGuestClosure(t *testing.T) {
	files := fstest.MapFS{"api.go": {Data: []byte(`package callbacks
 type NativeUnary func(int) int
 type NativeFunctionBox struct { Apply NativeUnary }
 func Box() *NativeFunctionBox { panic("host") }
 `)}}
	schema, err := evmbind.New("callbacks", files, evmbind.Options{
		Host:  reflect.TypeFor[*nativeFunctionFieldHost](),
		Types: []reflect.Type{reflect.TypeFor[NativeFunctionBox](), reflect.TypeFor[NativeUnary]()},
	})
	if err != nil {
		t.Fatal(err)
	}
	module, err := evmcompiler.Compile("callback.go", `import "callbacks"
 func Install(){n:=40;callbacks.Box().Apply=func(x int)int{n+=x;return n}}
 func Clear(){callbacks.Box().Apply=nil}
 func InstallBudget(){callbacks.Box().Apply=func(x int)int{for i:=0;i<500;i++{x++};return x}}
 func CallBudget()int{return callbacks.Box().Apply(1)}
 func CallNative()int{f:=callbacks.Box().Apply;callbacks.Box().Apply=nil;return f(4)}
 `, schema.Package())
	if err != nil {
		t.Fatal(err)
	}
	host := &nativeFunctionFieldHost{}
	program, err := evm.Link(module, schema.Bind(host))
	if err != nil {
		t.Fatal(err)
	}
	vm := evmtest.New(program)
	if err := vm.Run(context.Background(), "Install", nil, nil); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	if host.box.Apply == nil {
		t.Fatal("missing retained function")
	}
	if err := vm.RunNative(context.Background(), func(*evm.VM) error {
		if got := host.box.Apply(2); got != 42 {
			t.Fatalf("first call: %d", got)
		}
		if got := host.box.Apply(3); got != 45 {
			t.Fatalf("lost capture: %d", got)
		}
		return nil
	}, evm.Budget{}); err != nil {
		t.Fatal(err)
	}
	if err := vm.Run(context.Background(), "Clear", nil, nil); err != nil {
		t.Fatal(err)
	}
	if host.box.Apply != nil {
		t.Fatal("nil function was not preserved")
	}
	host.box.Apply = func(n int) int { return n * 3 }
	nativeResult := make([]values.Value, 1)
	if err := vm.Run(context.Background(), "CallNative", nil, nativeResult); err != nil || nativeResult[0].AsInt() != 12 {
		t.Fatalf("native function snapshot: %v %v", nativeResult, err)
	}

	if err := vm.Run(context.Background(), "InstallBudget", nil, nil); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	result := make([]values.Value, 1)
	if err := vm.RunWithBudget(context.Background(), "CallBudget", nil, result, evm.Budget{Steps: 100}); err == nil {
		t.Fatal("native field round trip reset the caller's execution budget")
	}

	saved := host.box.Apply
	if err := vm.Reset(); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if err, ok := recover().(error); !ok || !errors.Is(err, evm.ErrCallbackExpired) {
				t.Fatalf("expired callback: %v", err)
			}
		}()
		saved(1)
	}()
	limited := evmtest.New(program)
	scope, err := limited.OpenCallbackScope()
	if err != nil {
		t.Fatal(err)
	}
	defer scope.Close()
	if err := limited.Run(context.Background(), "InstallBudget", nil, nil); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			err, ok := recover().(error)
			if !ok || !errors.Is(err, evm.ErrSteps) {
				t.Fatalf("idle callback budget: %v", err)
			}
		}()
		limited.SetBudget(evm.Limits{Steps: 10})
		host.box.Apply(1)
	}()
	if err := limited.Run(context.Background(), "Clear", nil, nil); err != nil {
		t.Fatalf("failed idle session: %v", err)
	}

}

func TestNativeFunctionFieldSharesCallingVMGlobals(t *testing.T) {
	files := fstest.MapFS{"api.go": {Data: []byte(`package callbacks
 type NativeUnary func(int)int
 type NativeFunctionBox struct{Apply NativeUnary}
 func Box()*NativeFunctionBox
 func Apply(n int)int
 `)}}
	schema, err := evmbind.New("callbacks", files, evmbind.Options{Host: reflect.TypeFor[*nativeFunctionFieldHost](), Types: []reflect.Type{reflect.TypeFor[NativeFunctionBox](), reflect.TypeFor[NativeUnary]()}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := evmcompiler.Compile("globals.go", `import "callbacks"
 var total int
 func Run(){total=40;callbacks.Box().Apply=func(n int)int{total+=n;return total};for i:=0;i<1000;i++{callbacks.Box().Apply=callbacks.Box().Apply};if callbacks.Apply(2)!=42 || total!=42{panic("stored callback lost calling VM globals")}}
 `, schema.Package())
	if err != nil {
		t.Fatal(err)
	}
	host := &nativeFunctionFieldHost{}
	p, err := evm.Link(m, schema.Bind(host))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vm := evmtest.New(p)
	if err := vm.Run(ctx, "Run", nil, nil); err != nil {
		t.Fatal(err)
	}
	scope, err := vm.OpenCallbackScope()
	if err != nil {
		t.Fatal(err)
	}
	if err := vm.RunNative(ctx, func(*evm.VM) error {
		if err := scope.Close(); !errors.Is(err, evm.ErrReentry) {
			t.Fatalf("close running scope: %v", err)
		}
		return nil
	}, evm.Budget{}); err != nil {
		t.Fatal(err)
	}
	if got := host.box.Apply(1); got != 1 {
		t.Fatalf("idle callback globals: %d", got)
	}
	cancel()
	func() {
		defer func() {
			err, ok := recover().(error)
			if !ok || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled scope: %v", err)
			}
		}()
		host.box.Apply(1)
	}()
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recover() != evm.ErrCallbackExpired {
				t.Fatal("closed scope accepted callback")
			}
		}()
		host.box.Apply(1)
	}()

}

func BenchmarkNativeFunctionField100Calls(b *testing.B) {
	files := fstest.MapFS{"api.go": {Data: []byte(`package callbacks
 type NativeUnary func(int)int
 type NativeFunctionBox struct{Apply NativeUnary}
 func Box()*NativeFunctionBox
 func Apply(n int)int
 `)}}
	schema, err := evmbind.New("callbacks", files, evmbind.Options{Host: reflect.TypeFor[*nativeFunctionFieldHost](), Types: []reflect.Type{reflect.TypeFor[NativeFunctionBox](), reflect.TypeFor[NativeUnary]()}})
	if err != nil {
		b.Fatal(err)
	}
	m, err := evmcompiler.Compile("callback_bench.go", `import "callbacks"
 func Install(){callbacks.Box().Apply=func(n int)int{return n+1}}
 func RoundTrip(){for i:=0;i<1000;i++{callbacks.Box().Apply=callbacks.Box().Apply}}
 func Run()int{sum:=0;for i:=0;i<100;i++{sum+=callbacks.Apply(i)};return sum}
 `, schema.Package())
	if err != nil {
		b.Fatal(err)
	}
	for _, mode := range []string{"native", "guest", "guest_after_1000_round_trips"} {
		b.Run(mode, func(b *testing.B) {
			host := &nativeFunctionFieldHost{box: NativeFunctionBox{Apply: func(n int) int { return n + 1 }}}
			p, err := evm.Link(m, schema.Bind(host))
			if err != nil {
				b.Fatal(err)
			}
			vm := evmtest.New(p)
			vm.Limits.AllocationBytes, vm.Limits.Allocations = ^uint64(0), ^uint64(0)
			if mode != "native" {
				if err := vm.Run(context.Background(), "Install", nil, nil); err != nil {
					b.Fatal(err)
				}
			}
			if mode == "guest_after_1000_round_trips" {
				if err := vm.Run(context.Background(), "RoundTrip", nil, nil); err != nil {
					b.Fatal(err)
				}
			}
			var out [1]values.Value
			b.ReportAllocs()
			for b.Loop() {
				if err := vm.Run(context.Background(), "Run", nil, out[:]); err != nil {
					b.Fatal(err)
				}
			}
			if out[0].AsInt() != 5050 {
				b.Fatal(out)
			}
		})
	}
}
