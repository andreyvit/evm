package evmbind_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

type mixedCallbackHost struct{ box SourceCallbackBox }

func (*mixedCallbackHost) Native(_ *evm.Call, fn func()) { fn() }
func (*mixedCallbackHost) Retained(c *evm.Call, fn *evm.FunctionOf[func()]) {
	v, err := fn.Value(c.Program(), evm.Signature{})
	if err != nil {
		panic(err)
	}
	if err := c.RunCallbackArgs(v, nil, nil); err != nil {
		panic(err)
	}
}

func (h *mixedCallbackHost) ZBox(*evm.Call) *SourceCallbackBox { return &h.box }
func (*mixedCallbackHost) ZEcho(_ *evm.Call, fn *evm.FunctionOf[func()]) *evm.FunctionOf[func()] {
	return fn
}

func (h *mixedCallbackHost) AHandle(c *evm.Call, fn *evm.FunctionOf[func()]) { h.Retained(c, fn) }

func TestNativeAndRetainedCallbackSameSignature(t *testing.T) {
	for _, handle := range []string{"Retained", "AHandle"} {
		t.Run(handle, func(t *testing.T) { testMixedCallbacks(t, handle) })
	}
}

func testMixedCallbacks(t *testing.T, handle string) {
	schema, err := evmbind.New("host", fstest.MapFS{"api.go": {Data: []byte(strings.ReplaceAll(`package host
 import "github.com/andreyvit/evm"
 type SourceCallbackBox struct { F *evm.FunctionOf[func()] }
 func ZBox() *SourceCallbackBox { panic("host") }
 func ZEcho(fn *evm.FunctionOf[func()]) *evm.FunctionOf[func()] { panic("host") }
 func Native(fn func()) { panic("host") }
 func Retained(fn *evm.FunctionOf[func()]) { panic("host") }
 `, "Retained", handle))}}, evmbind.Options{Host: reflect.TypeFor[*mixedCallbackHost]()})
	if err != nil {
		t.Fatal(err)
	}
	pkg := schema.Bind(&mixedCallbackHost{})
	module, err := evmcompiler.Compile("mixed.go", strings.ReplaceAll(`import "host"
 func Run() int { n:=0; fn:=func(){n++}; host.Native(fn);host.Retained(fn);box:=host.ZBox();box.F=fn;host.Retained(box.F);host.ZEcho(fn)();return n }
 `, "Retained", handle), pkg)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, pkg)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil || out[0].R != 4 {
		t.Fatal(out, err)
	}
}
