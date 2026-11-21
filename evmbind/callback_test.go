//go:build !evmreflect

package evmbind_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/callbackbindings"
	"github.com/andreyvit/evm/evmbind/internal/callbackfixture"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
)

func TestTypedSynchronousCallbacks(t *testing.T) {
	generated, err := evmbind.Generate("native", "github.com/andreyvit/evm/evmbind/internal/callbackfixture", "callbackbindings", callbackfixture.Source, evmbind.Options{})
	if err != nil {
		t.Fatal(err)
	}
	checked, err := os.ReadFile("internal/callbackbindings/native_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, checked) {
		t.Fatal("regenerate callback bindings")
	}
	for _, mode := range []string{"reflected", "generated"} {
		for _, retained := range []bool{false, true} {
			t.Run(mode+map[bool]string{true: "/retained", false: "/borrowed"}[retained], func(t *testing.T) {
				opt := evmbind.Options{Types: []reflect.Type{reflect.TypeFor[callbackfixture.Runner]()}}
				if mode == "generated" {
					opt.Static = &callbackbindings.Bindings
					opt.Mode = evmbind.StaticOnly
				}
				schema, err := evmbind.New("native", callbackfixture.Source, opt)
				if err != nil {
					t.Fatal(err)
				}
				pkg := schema.Bind(nil)
				module, err := evmcompiler.Compile("callbacks.go", `import("native";"errors")
  var total int
  func Run(r *native.Runner){
   total=10
   failure:=errors.New("failure")
   f:=func(n int)error{total+=n;return failure}
   if r.Call(f)!=failure || total!=17{panic("synchronous globals")}
   if r.OnThread(f)!=failure || total!=24{panic("thread callback")}
   if r.Echo(f)(1)!=failure || total!=25{panic("returned callback")}
   if r.Echo(nil)!=nil{panic("nil callback")}
   local:=0
   r.Store(func(n int)error{local+=n;if local!=2{panic("retained captures")};return failure})
  }
  func Panic(r *native.Runner)(caught any){defer func(){caught=recover()}();r.Call(func(n int)error{panic(n)});return}
  func Budget(r *native.Runner){r.Suppress(func(n int)error{for i:=0;i<1000000;i++{total++};return nil});panic("budget suppressed")}
  `, pkg)
				if err != nil {
					t.Fatal(err)
				}
				program, err := evm.Link(module, pkg)
				if err != nil {
					t.Fatal(err)
				}
				runner := &callbackfixture.Runner{}
				arg, err := schema.NativePointer(runner)
				if err != nil {
					t.Fatal(err)
				}
				args := []values.Value{arg}
				vm := evmtest.New(program)
				if err := vm.Run(context.Background(), "Run", args, nil); err != nil {
					t.Fatal(err)
				}
				if runner.Closed != 1 {
					t.Fatalf("native call cleanup: %d", runner.Closed)
				}
				if retained {
					if err := vm.RunNative(context.Background(), func(*evm.VM) error {
						if err := runner.Saved(2); err == nil || err.Error() != "failure" {
							t.Fatalf("retained invocation: %v", err)
						}
						return nil
					}, evm.Budget{}); err != nil {
						t.Fatal(err)
					}
				} else {
					func() {
						defer func() {
							if recover() == nil {
								t.Fatal("expired borrowed callback ran")
							}
						}()
						runner.Saved(2)
					}()
				}
				var out [1]values.Value
				if err := evmtest.New(program).Run(context.Background(), "Panic", args, out[:]); err != nil {
					t.Fatal(err)
				}
				_, value, err := program.UnpackInterface(out[0])
				if err != nil || value.AsInt() != 7 || runner.Closed != 2 {
					t.Fatalf("panic: %v/%v/%d", value, err, runner.Closed)
				}
				if err := evmtest.New(program).RunWithBudget(context.Background(), "Budget", args, nil, evm.Budget{Steps: 1000}); !errors.Is(err, evm.ErrSteps) {
					t.Fatalf("budget: %v", err)
				}
				if runner.Closed != 3 {
					t.Fatal("native cleanup skipped")
				}
			})
		}
	}
}
