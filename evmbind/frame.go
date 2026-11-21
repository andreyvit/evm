package evmbind

import (
	"fmt"
	"reflect"
	"time"
	"unsafe"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/values"
)

type Frame struct {
	*evm.Call
	Host, Receiver any
	s              *Schema
}

func (f *Frame) New(cells uint64) unsafe.Pointer {
	p, err := f.AllocCells(cells)
	if err != nil {
		panic(err)
	}
	return p
}
func (f *Frame) String(x string) values.Value {
	if err := f.ChargeAllocation(uint64(len(x))); err != nil {
		panic(err)
	}
	return values.String(x)
}

// Callback binds a guest func() to its originating VM.
func (f *Frame) Callback(v values.Value) func() {
	if v.R == 0 {
		return nil
	}
	scope := f.Call.CallbackScope()
	return func() {
		if err := scope.WithCall(func(c *evm.Call) error {
			if err := c.RunCallback(v); err != nil {
				c.PanicCallback(err)
			}
			return nil
		}); err != nil {
			panic(err)
		}
	}
}
func (f *Frame) DecodeInterface(v values.Value) any {
	x, err := f.s.nativeInterface(f.Call, v)
	if err != nil {
		panic(err)
	}
	return x
}
func (f *Frame) EncodeInterface(x any) values.Value {
	if f.s.pointers[reflect.TypeOf(x)] == nil {
		v, err := f.Call.TypedInterfaceValue(x)
		if err != nil {
			panic(err)
		}
		return v
	}
	v, e := f.s.Interface(f.Program(), x)
	if e != nil {
		panic(e)
	}
	return v
}

// BoxedNamedFunction resolves an explicitly signature-polymorphic named handle.
// Ordinary any parameters do not use this conversion.
func (f *Frame) BoxedNamedFunction(v values.Value) *evm.NamedFunction {
	if v.R == 0 {
		return nil
	}
	fn, err := f.Program().NamedFunction(v)
	if err != nil {
		panic(err)
	}
	return fn
}

// NamedFunction retains a typed public guest function without interface boxing.
func (f *Frame) NamedFunction(v values.Value) *evm.NamedFunction {
	if v.R == 0 {
		return nil
	}
	fn, err := f.Program().NamedFunctionValue(v)
	if err != nil {
		panic(err)
	}
	return fn
}

// Function retains a typed callable, including its captured state.
func (f *Frame) Function(v values.Value) *evm.FunctionHandle {
	if v.R == 0 {
		return nil
	}
	if err := f.ChargeAllocation(uint64(unsafe.Sizeof(evm.FunctionHandle{}))); err != nil {
		panic(err)
	}
	fn, err := f.Program().FunctionValue(v)
	if err != nil {
		panic(err)
	}
	return fn
}

// EncodeFunction checks program ownership and the declared result signature.
func (f *Frame) EncodeFunction(name string, fn *evm.FunctionHandle) values.Value {
	n := f.s.src.nodes[name]
	if n == nil || n.kind != nodeRetainedFunction {
		panic(evm.ErrFunction)
	}
	v, err := fn.Value(f.Program(), n.functionSignature)
	if err != nil {
		panic(err)
	}
	return v
}

func (f *Frame) DecodeAny(v values.Value) any {
	x, err := f.Call.NativeAny(v)
	if err != nil {
		panic(err)
	}
	return x
}
func (f *Frame) EncodeAny(x any) values.Value {
	v, err := f.Call.AnyValue(x)
	if err != nil {
		panic(err)
	}
	return v
}

// DecodeTypedInterface uses the schema that owns the declared interface.
func (f *Frame) DecodeTypedInterface(name string, v values.Value) any {
	s := f.interfaceSchema(name)
	x, err := s.nativeInterface(f.Call, v)
	if err != nil {
		panic(err)
	}
	t := f.s.src.nodes[name].shape.Native.GoType
	if x != nil && !reflect.TypeOf(x).AssignableTo(t) {
		panic(fmt.Errorf("value %T does not implement native interface %s", x, t))
	}
	return x
}
func (f *Frame) EncodeTypedInterface(name string, x any) values.Value {
	s := f.interfaceSchema(name)
	if s.pointers[reflect.TypeOf(x)] == nil {
		v, err := f.Call.TypedInterfaceValue(x)
		if err != nil {
			panic(err)
		}
		return v
	}
	v, err := s.Interface(f.Program(), x)
	if err != nil {
		panic(err)
	}
	return v
}
func (f *Frame) interfaceSchema(name string) *Schema {
	n := f.s.src.nodes[name]
	if n == nil || n.kind != nodeInterface {
		panic("unknown interface binding: " + name)
	}
	if n.imported != nil {
		return n.imported
	}
	return f.s
}

// DecodeError preserves native and guest error identity across the binding.
func (f *Frame) DecodeError(v values.Value) error {
	x, err := f.Call.NativeError(v)
	if err != nil {
		panic(err)
	}
	return x
}

// Time preserves an exact native time value and accounts for its owned storage.
func (f *Frame) Time(t time.Time) values.Value {
	if err := f.ChargeAllocation(values.NativeTimeAllocBytes(t)); err != nil {
		panic(err)
	}
	return values.NativeTime(t)
}

// BoxedFunction retains a signature-polymorphic callable and its captures.
// The receiving host checks its signature. Ordinary any stays ordinary any.
func (f *Frame) BoxedFunction(v values.Value) *evm.FunctionHandle {
	if v.R == 0 {
		return nil
	}
	shape, value, err := f.Program().UnpackInterface(v)
	if err != nil {
		panic(err)
	}
	if shape == nil || shape.Kind != values.FunctionKind {
		panic(evm.ErrFunction)
	}
	return f.Function(value)
}

// CellField reads a logical field from a compact struct value.
func (f *Frame) CellField(name string, v values.Value, index int) values.Value {
	return f.s.src.nodes[name].shape.CellStruct.Get(v, index)
}

// PackCell constructs a compact struct value from its logical fields.
func (f *Frame) PackCell(name string, fields [2]values.Value) values.Value {
	return f.s.src.nodes[name].shape.CellStruct.Pack(fields)
}

// SetBorrowedBytesResult returns a slice backed by a declared borrow source.
func (f *Frame) SetBorrowedBytesResult(index int, data []byte) {
	v, err := f.Call.BorrowBytes(data)
	if err != nil {
		panic(err)
	}
	f.SetResult(index, v)
}
