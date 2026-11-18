package evmbind

import (
	"fmt"
	"reflect"
	"unsafe"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/nativebind"
	"github.com/andreyvit/evm/values"
)

type nativeFunctionBinding struct {
	types     map[*node]reflect.Type
	typ       reflect.Type
	signature *evm.NativeFunctionType
}

func (s *Schema) installNativeFunction(n *node, t reflect.Type, types map[*node]reflect.Type) {
	typ, err := evm.NewNativeFunctionType(n.functionSignature)
	if err != nil {
		panic(err)
	}
	s.nativeFunctions[n] = &nativeFunctionBinding{types: types, typ: t, signature: typ}
	n.shape.Native = nativebind.Layout(t, nil, s.static)
	codec := &evm.NativeValueCodec{
		Decode: func(c *evm.Call, v values.Value) (values.Value, error) {
			if v.P == nil {
				return values.Value{}, nil
			}
			return nativeFunctionResult(func() values.Value {
				f := &Frame{Call: c, s: s}
				return f.encodeNativeFunction(n, reflect.NewAt(t, v.P).Elem())
			})
		},
		Encode: func(c *evm.Call, v values.Value) (values.Value, error) {
			if v.R == 0 {
				return values.Value{}, nil
			}
			if err := c.ChargeAllocation(uint64(t.Size())); err != nil {
				return values.Value{}, err
			}
			return nativeFunctionResult(func() values.Value {
				f := &Frame{Call: c, s: s}
				x := reflect.New(t)
				x.Elem().Set(f.decodeNativeFunction(n, v))
				return values.Value{P: x.UnsafePointer()}
			})
		},
	}
	for i := range s.src.pkg.Types {
		if s.src.pkg.Types[i].Shape.Type == n.shape.Type {
			s.src.pkg.Types[i].NativeCodec = codec
		}
	}
}

func (f *Frame) decodeNativeFunction(n *node, v values.Value) reflect.Value {
	return f.decodeFunction(n, v)
}

func (f *Frame) decodeFunction(n *node, v values.Value) reflect.Value {
	b := f.s.nativeFunctions[n]
	if v.R == 0 {
		return reflect.Zero(b.typ)
	}
	fn := f.Function(v)
	if _, err := fn.Value(f.Program(), n.functionSignature); err != nil {
		panic(err)
	}
	if original, ok := f.Program().NativeFunctionData(v).(reflect.Value); ok && original.Type() == b.typ {
		return original
	}
	if err := f.ChargeAllocation(128); err != nil {
		panic(err)
	}
	scope := f.Call.CallbackScope()
	schema := f.s
	return reflect.MakeFunc(b.typ, func(in []reflect.Value) []reflect.Value {
		var results []reflect.Value
		err := scope.WithCall(func(c *evm.Call) (err error) {
			defer func() {
				if err != nil {
					c.PanicCallback(err)
				}
			}()
			frame := &Frame{Call: c, s: schema}
			if err := c.ChargeAllocation(uint64(len(in)+b.typ.NumOut()) * uint64(unsafe.Sizeof(values.Value{})+unsafe.Sizeof(reflect.Value{}))); err != nil {
				return err
			}
			args := make([]values.Value, len(in))
			out := make([]values.Value, b.typ.NumOut())
			for i, x := range in {
				args[i] = encode(frame, n.functionArgs[i], x)
			}
			value, err := fn.Value(c.Program(), n.functionSignature)
			if err != nil {
				return err
			}
			if err := c.RunCallbackArgs(value, args, out); err != nil {
				return err
			}
			results = make([]reflect.Value, len(out))
			for i, x := range out {
				results[i] = decode(frame, n.functionOuts[i], x, b.types)
			}
			return nil
		})
		if err != nil {
			panic(err)
		}
		return results
	})
}

func (f *Frame) encodeNativeFunction(n *node, x reflect.Value) values.Value {
	if x.IsNil() {
		return values.Value{}
	}
	b := f.s.nativeFunctions[n]
	// Copy the function value; never retain the address of a mutable field.
	fn := reflect.ValueOf(x.Interface())
	schema := f.s
	v, err := f.Call.NativeFunctionWithData(b.signature, func(c *evm.Call) (err error) {
		defer Catch("native function", &err)
		if err := c.ChargeAllocation(uint64(len(n.functionArgs)+len(n.functionOuts)) * uint64(unsafe.Sizeof(reflect.Value{}))); err != nil {
			return err
		}
		frame := &Frame{Call: c, s: schema}
		types := b.types
		in := make([]reflect.Value, len(n.functionArgs))
		for i, a := range n.functionArgs {
			in[i] = decodeArgument(frame, a, c.Argument(i), types)
		}
		out := fn.Call(in)
		for i, value := range out {
			c.SetResult(i, encode(frame, n.functionOuts[i], value))
		}
		return nil
	}, fn)
	if err != nil {
		panic(err)
	}
	return v
}

// DecodeNativeFunction converts a script callable to its declared native type.
func (f *Frame) DecodeNativeFunction(name string, v values.Value) any {
	n := f.s.src.nodes[name]
	if n == nil || n.kind != nodeNativeFunction {
		panic(evm.ErrFunction)
	}
	return f.decodeNativeFunction(n, v).Interface()
}

// EncodeNativeFunction makes a native Go function callable by the script.
func (f *Frame) EncodeNativeFunction(name string, x any) values.Value {
	n := f.s.src.nodes[name]
	if n == nil || n.kind != nodeNativeFunction {
		panic(evm.ErrFunction)
	}
	return f.encodeNativeFunction(n, reflect.ValueOf(x))
}

func nativeFunctionResult(fn func() values.Value) (out values.Value, err error) {
	defer func() {
		if failure := recover(); failure != nil {
			out = values.Value{}
			if e, ok := failure.(error); ok {
				err = e
			} else {
				err = fmt.Errorf("native function binding: %v", failure)
			}
		}
	}()
	return fn(), nil
}

// DecodeNativeCallback binds a typed callback to the originating VM.
func (f *Frame) DecodeNativeCallback(name string, v values.Value) any {
	n := f.s.src.nodes[name]
	if n == nil || n.kind != nodeNativeFunction {
		panic(evm.ErrFunction)
	}
	return f.decodeFunction(n, v).Interface()
}
