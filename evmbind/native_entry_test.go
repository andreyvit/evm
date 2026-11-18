package evmbind

import (
	"context"
	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
	"reflect"
	"testing"
	"testing/fstest"
)

type EntryData struct{ Count int }

func TestNativeEntryArgumentWithoutMethods(t *testing.T) {
	schema, err := New("native", fstest.MapFS{"types.go": {Data: []byte(`package native;type EntryData struct{Count int}`)}}, Options{Selective: true, Types: []reflect.Type{reflect.TypeFor[EntryData]()}})
	if err != nil {
		t.Fatal(err)
	}
	module, err := evmcompiler.Compile("run.go", `import "native";func Run(data *native.EntryData){data.Count++};func Any(x any){};func Error(x error){}`, schema.Package())
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(module, schema.Bind(nil))
	if err != nil {
		t.Fatal(err)
	}
	data := &EntryData{Count: 4}
	var arg values.Value
	if err := evm.New(p).RunNative(context.Background(), func(c *evm.Call) error {
		packed, err := c.AnyValue(data)
		if err != nil {
			return err
		}
		_, value, err := p.UnpackInterface(packed)
		if err != nil {
			return err
		}
		original, err := schema.NativePointer(data)
		if err != nil {
			return err
		}
		arg = value
		if value != original {
			t.Fatal("native entry pointer changed")
		}
		return nil
	}, evm.Budget{}); err != nil {
		t.Fatal(err)
	}
	if err := evm.New(p).Run(context.Background(), "Run", []values.Value{arg}, nil); err != nil {
		t.Fatal(err)
	}
	if data.Count != 5 {
		t.Fatalf("native count = %d", data.Count)
	}
}
