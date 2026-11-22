package evmbind

import (
	"bytes"
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
		Decode: func(c *evm.VM, v values.Value) (values.Value, error) {
			if v.P == nil {
				return values.Value{}, nil
			}
			return nativeFunctionResult(func() values.Value {
				f := &Frame{VM: c, s: s}
				return f.encodeNativeFunction(n, reflect.NewAt(t, v.P).Elem())
			})
		},
		Encode: func(c *evm.VM, v values.Value) (values.Value, error) {
			if v.R == 0 {
				return values.Value{}, nil
			}
			if err := c.ChargeAllocation(uint64(t.Size())); err != nil {
				return values.Value{}, err
			}
			return nativeFunctionResult(func() values.Value {
				f := &Frame{VM: c, s: s}
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
	return f.decodeCallback(n, v, "")
}

func (f *Frame) decodeCallback(n *node, v values.Value, domain string) reflect.Value {
	b := f.s.nativeFunctions[n]
	if v.R == 0 {
		return reflect.Zero(b.typ)
	}
	fn := f.Function(v)
	if _, err := fn.Value(f.Program(), n.functionSignature); err != nil {
		panic(err)
	}
	if original, ok := f.Program().NativeFunctionData(v).(reflect.Value); ok && domain == "" && original.Type() == b.typ {
		return original
	}
	if err := f.ChargeAllocation(128); err != nil {
		panic(err)
	}
	acceptsBorrow := domain != "" && fn.AcceptsBorrowedBytes(domain)
	scope := f.VM.CallbackScope()
	schema := f.s
	return reflect.MakeFunc(b.typ, func(in []reflect.Value) []reflect.Value {
		var results []reflect.Value
		err := scope.WithCall(func(c *evm.VM) (err error) {
			defer func() {
				if err != nil {
					c.PanicCallback(err)
				}
			}()
			frame := &Frame{VM: c, s: schema}
			if err := c.ChargeAllocation(uint64(len(in)+b.typ.NumOut()) * uint64(unsafe.Sizeof(values.Value{})+unsafe.Sizeof(reflect.Value{}))); err != nil {
				return err
			}
			slots := make([]values.Value, len(in)+b.typ.NumOut())
			args, out := slots[:len(in)], slots[len(in):]
			for i, x := range in {
				if domain != "" && n.functionArgs[i].shape.Type == evm.ByteSliceType {
					if acceptsBorrow {
						args[i], err = c.BorrowBytes(x.Bytes())
						if err != nil {
							return err
						}
					} else {
						// Native and previously compiled targets may retain an ordinary slice.
						if err := c.ChargeAllocation(uint64(x.Len())); err != nil {
							return err
						}
						data := bytes.Clone(x.Bytes())
						args[i] = encode(frame, n.functionArgs[i], reflect.ValueOf(data[:len(data):len(data)]))
					}
				} else {
					args[i] = encode(frame, n.functionArgs[i], x)
				}
			}
			if err := c.Run(*fn, slots); err != nil {
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
	return f.encodeFunction(n, x, "")
}

func (f *Frame) encodeFunction(n *node, x reflect.Value, domain string) values.Value {
	if x.IsNil() {
		return values.Value{}
	}
	b := f.s.nativeFunctions[n]
	// Copy the function value; never retain the address of a mutable field.
	fn := reflect.ValueOf(x.Interface())
	schema := f.s
	var original any = fn
	if domain != "" {
		// Preserve the adapter across native round trips.
		original = nil
	}
	v, err := f.VM.NativeFunctionWithData(b.signature, func(c *evm.VM) (err error) {
		defer Catch("native function", &err)
		if err := c.ChargeAllocation(uint64(len(n.functionArgs)+len(n.functionOuts)) * uint64(unsafe.Sizeof(reflect.Value{}))); err != nil {
			return err
		}
		frame := &Frame{VM: c, s: schema}
		types := b.types
		in := make([]reflect.Value, len(n.functionArgs))
		for i, a := range n.functionArgs {
			if domain != "" {
				in[i] = frame.decodeCallback(a, c.CallArgument(i), domain)
			} else {
				in[i] = decodeArgument(frame, a, c.CallArgument(i), types)
			}
		}
		out := fn.Call(in)
		for i, value := range out {
			c.SetCallResult(i, encode(frame, n.functionOuts[i], value))
		}
		return nil
	}, original)
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

// EncodeBorrowedIterator adapts the []byte arguments of a returned iterator's yield function.
func (f *Frame) EncodeBorrowedIterator(name string, x any, domain string) values.Value {
	return f.encodeFunction(f.s.src.nodes[name], reflect.ValueOf(x), domain)
}

// DecodeBorrowedCallback adapts a method callback whose []byte arguments are borrowed.
func (f *Frame) DecodeBorrowedCallback(name string, v values.Value, domain string) any {
	return f.decodeCallback(f.s.src.nodes[name], v, domain).Interface()
}
