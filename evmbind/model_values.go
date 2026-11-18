package evmbind

import (
	"fmt"
	"reflect"
	"unsafe"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/values"
)

// ModelArgument converts a typed immutable row to the native declaration named
// by the generated binding. Native-backed pointers retain identity; encoded
// records require an explicit conversion before this call.
func (f *Frame) ModelArgument(name string, v values.Value) any {
	return decodeModel(f, f.modelNode(name), v).Interface()
}

// ModelResult exposes the native declaration as its immutable model row.
func (f *Frame) ModelResult(name string, x any) values.Value {
	return encodeModel(f, f.modelNode(name), reflect.ValueOf(x))
}

func (f *Frame) modelNode(name string) *node {
	n := f.s.src.nodes[name]
	if n == nil || n.kind != nodeRecord {
		panic(fmt.Errorf("unregistered native model declaration %s", name))
	}
	return n
}

// ModelValueArgument avoids boxing a generated native value argument in any.
func ModelValueArgument[T any](f *Frame, name string, v values.Value) T {
	var result T
	n := f.modelNode(name)
	decodeModelValue(f, n, v, reflect.ValueOf(&result).Elem())
	return result
}

// ModelValueResult retains the ordinary Go argument copy as the row storage.
func ModelValueResult[T any](f *Frame, name string, x T) values.Value {
	p := f.New(1)
	StoreModelValue(f, name, p, x)
	return values.Value{P: p}
}

// StoreModelValue writes a native value snapshot into its aggregate cell.
func StoreModelValue[T any](f *Frame, name string, p unsafe.Pointer, x T) {
	n := f.modelNode(name)
	if err := f.ChargeAllocation(uint64(n.native.Size())); err != nil {
		panic(err)
	}
	*(*values.Value)(p) = values.Value{P: unsafe.Pointer(&x)}
}

func decodeModel(f *Frame, n *node, v values.Value) reflect.Value {
	if n.native.Kind() != reflect.Pointer {
		p := reflect.New(n.native)
		decodeModelValue(f, n, v, p.Elem())
		return p.Elem()
	}
	var record kvo.Record
	var err error
	if n.model.row.Mutable {
		record, err = evm.NativeMutableRecord(v, n.model.row.Model)
	} else {
		record, err = f.Call.Record(v, n.model.row.Model)
	}
	if err != nil {
		panic(err)
	}
	if p := record.NativePointer(); p != nil && reflect.TypeOf(p) == n.native {
		return reflect.ValueOf(p)
	}
	if record.Missing() {
		return reflect.Zero(n.native)
	}
	panic(fmt.Errorf("model row requires explicit conversion to %s", n.native))
}

func decodeModelValue(f *Frame, n *node, v values.Value, dst reflect.Value) {
	if v.P == nil || (*values.Value)(v.P).P == nil {
		dst.SetZero()
		return
	}
	dst.Set(reflect.NewAt(n.native, (*values.Value)(v.P).P).Elem())
}

func encodeModel(f *Frame, n *node, x reflect.Value) values.Value {
	if x.Kind() != reflect.Pointer {
		if err := f.ChargeAllocation(uint64(x.Type().Size())); err != nil {
			panic(err)
		}
		p := reflect.New(x.Type())
		p.Elem().Set(x)
		cell := f.New(1)
		*(*values.Value)(cell) = values.Value{P: p.UnsafePointer()}
		return values.Value{P: cell}
	}
	r := n.model.binding.Record(x)
	if n.model.row.Mutable {
		v, err := evm.NativeMutableRecordValue(n.model.row.Model, r)
		if err != nil {
			panic(err)
		}
		return v
	}
	return r.Value()
}

func modelPointerLayout(n *node) *values.NativeLayout {
	return evm.NativeModelPointerLayout(n.model.row.Model, n.model.native)
}
