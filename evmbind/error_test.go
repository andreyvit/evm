//go:build !evmreflect

package evmbind_test

import (
	"bytes"
	"context"
	"errors"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/errorbindings"
	"github.com/andreyvit/evm/evmbind/internal/errorfixture"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

func TestNativeErrorBindings(t *testing.T) {
	generated, err := evmbind.Generate("native", "github.com/andreyvit/evm/evmbind/internal/errorfixture", "errorbindings", errorfixture.Source, evmbind.Options{})
	if err != nil {
		t.Fatal(err)
	}
	checked, err := os.ReadFile("internal/errorbindings/native_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, checked) {
		t.Fatal("regenerate error bindings")
	}
	for _, mode := range []string{"reflected", "generated"} {
		t.Run(mode, func(t *testing.T) {
			opt := evmbind.Options{Types: []reflect.Type{reflect.TypeFor[errorfixture.ErrorBox]()}}
			if mode == "generated" {
				opt.Static = &errorbindings.Bindings
				opt.Mode = evmbind.StaticOnly
			}
			schema, err := evmbind.New("native", errorfixture.Source, opt)
			if err != nil {
				t.Fatal(err)
			}
			testNativeErrorBindings(t, schema)
		})
	}
}
func testNativeErrorBindings(t *testing.T, schema *evmbind.Schema) {
	pkg := schema.Bind(nil)
	module, err := evmcompiler.Compile("error.go", `import("native";"errors")
 type GuestError struct{Text string}
 func(e *GuestError)Error()string{return e.Text}
 func Check(b *native.ErrorBox) {
  original:=b.Err
  if original==nil || original.Error()!="native"{panic("native error")}
  if b.Set(original)!=original || !errors.Is(b.Err,original){panic("native identity")}
  guest:=&GuestError{"guest"}
  if b.Set(guest)!=guest || b.Err!=guest {panic("guest identity")}
  if !errors.Is(b.Wrap(guest),guest){panic("wrapped guest identity")}
  local:=struct{Err error}{guest}
  b.Err=local.Err
  if b.Err!=guest || b.Err.Error()!="guest"{panic("field layout")}
  b.Errors=map[string]error{"native":original,"guest":guest,"nil":nil}
  if b.Errors["guest"]!=guest || b.Errors["native"]!=original{panic("map identity")}
  b.Err=nil
  if b.Set(nil)!=nil || b.Err!=nil{panic("nil error")}
 }
 `, pkg)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, pkg)
	if err != nil {
		t.Fatal(err)
	}
	original := errors.New("native")
	box := &errorfixture.ErrorBox{Err: original}
	arg, err := schema.NativePointer(box)
	if err != nil {
		t.Fatal(err)
	}
	vm := evm.New(program)
	scope, err := vm.OpenCallbackScope(context.Background(), evm.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	defer scope.Close()
	if err := vm.Run(context.Background(), "Check", []values.Value{arg}, nil); err != nil {
		t.Fatal(err)
	}
	if box.Err != nil || box.Errors["native"] != original || box.Errors["guest"].Error() != "guest" || box.Errors["nil"] != nil {
		t.Fatalf("native results: %+v", box)
	}
}

func TestErrorBindingWithImports(t *testing.T) {
	dep, err := evmbind.New("dep", errorfixture.Source, evmbind.Options{Types: []reflect.Type{reflect.TypeFor[errorfixture.ErrorBox]()}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evmbind.New("imported", errorfixture.Source, evmbind.Options{Types: []reflect.Type{reflect.TypeFor[errorfixture.ErrorBox]()}, Imports: map[string]*evmbind.Schema{"dep": dep}}); err != nil {
		t.Fatal(err)
	}
}
func TestUnsupportedErrorSlice(t *testing.T) {
	_, err := evmbind.New("bad", fstest.MapFS{"api.go": {Data: []byte("package bad;type Input struct{Errors []error}")}}, evmbind.Options{})
	if err == nil || !strings.Contains(err.Error(), "native []error storage is not supported") {
		t.Fatalf("unexpected error: %v", err)
	}
}
