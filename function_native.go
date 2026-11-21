package evm

import (
	"fmt"
	"reflect"
	"unsafe"

	"github.com/andreyvit/evm/values"
)

var nativeEntryType = func() *NativeFunctionType {
	t, err := NewNativeFunctionType(Signature{})
	if err != nil {
		panic(err)
	}
	return t
}()

// NativeArguments converts ordinary Go values into arguments for parameters
// of the given types: scalars, strings, time values, pointers to bound native
// structs, registered interface implementations and guest values. A Go slice
// or map converts when a linked package binds its type. A values.Value passes
// through unchanged. Native references keep their backing storage. A value whose
// Go type does not correspond to its parameter type is an error; there are no
// implicit numeric conversions. Conversion charges this call's allocation
// budget, and the results are valid for this program only.
func (c *VM) NativeArguments(params []*values.Type, args []any) ([]values.Value, error) {
	if len(args) != len(params) {
		return nil, fmt.Errorf("got %d arguments for %d parameters", len(args), len(params))
	}
	if err := c.ChargeAllocation(uint64(len(args)) * uint64(unsafe.Sizeof(values.Value{}))); err != nil {
		return nil, err
	}
	return c.nativeArguments(params, args)
}

func (c *VM) nativeArguments(params []*values.Type, args []any) ([]values.Value, error) {
	p := c.program
	inputs := make([]values.Value, len(args))
	for i, arg := range args {
		if v, ok := arg.(values.Value); ok {
			inputs[i] = v // already in VM form; the caller vouches for its type
			continue
		}
		expected := p.Shape(params[i])
		if typ := nativeScalarVMTypes[reflect.TypeOf(arg)]; typ != nil && (expected == nil || expected.Kind != values.InterfaceKind) {
			// A scalar needs no linked interface type; match it directly.
			if typ != params[i] {
				return nil, fmt.Errorf("argument %d: Go value of type %T does not match parameter type %s", i, arg, params[i].Name())
			}
			v, err := c.nativeScalar(reflect.ValueOf(arg))
			if err != nil {
				return nil, fmt.Errorf("argument %d: %w", i, err)
			}
			inputs[i] = v
			continue
		}
		v, err := c.AnyValue(arg)
		if err != nil {
			return nil, fmt.Errorf("argument %d: %w", i, err)
		}
		if expected != nil && expected.Kind == values.InterfaceKind {
			if v != (values.Value{}) {
				_, ok, err := p.assert(v, expected, 0)
				if err != nil {
					return nil, err
				}
				if !ok {
					return nil, fmt.Errorf("argument %d: Go value of type %T does not implement %s", i, arg, params[i].Name())
				}
			}
			inputs[i] = v
			continue
		}
		shape, value, err := p.UnpackInterface(v)
		if err != nil {
			return nil, err
		}
		if shape == nil || shape.Type != params[i] {
			return nil, fmt.Errorf("argument %d: Go value of type %T does not match parameter type %s", i, arg, params[i].Name())
		}
		inputs[i] = value
	}
	return inputs, nil
}
