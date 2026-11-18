package evm

import (
	"context"
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

// RunFunctionNative invokes a retained function on this VM with native values. Argument
// conversion, guest execution and result conversion share the supplied budget.
// Results are published only after successful execution and conversion.
func (vm *VM) RunFunctionNative(ctx context.Context, f *FunctionHandle, args []any, results []any, budget Budget) error {
	if f == nil {
		return ErrNilFunction
	}
	if len(args) != len(f.signature.Params) || len(results) != len(f.signature.Results) {
		return ErrFunction
	}
	p := f.program
	if vm.program != p {
		return ErrFunction
	}
	var output []any
	entry, err := p.NativeFunction(nativeEntryType, func(c *Call) error {
		if err := c.ChargeAllocation(uint64(len(args)+len(results))*uint64(unsafe.Sizeof(values.Value{})) + uint64(len(results))*uint64(unsafe.Sizeof(any(nil)))); err != nil {
			return err
		}
		returns := make([]values.Value, len(results))
		inputs, err := c.nativeArguments(f.signature.Params, args)
		if err != nil {
			return err
		}
		if err := c.RunCallbackArgs(f.value, inputs, returns); err != nil {
			return err
		}
		output = make([]any, len(results))
		for i, value := range returns {
			var err error
			if output[i], err = c.nativeResult(f.signature.Results[i], value); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	root, err := p.FunctionValue(entry)
	if err != nil {
		return err
	}
	if err := vm.RunFunctionWithBudget(ctx, root, nil, nil, budget); err != nil {
		return err
	}
	copy(results, output)
	return nil
}

// RunEntryNative runs a named entry function with ordinary Go values, the way
// RunFunctionNative runs a retained function: arguments must correspond to the
// entry's parameter types, and results come back as Go values. Conversion and
// execution share the supplied budget.
func (vm *VM) RunEntryNative(ctx context.Context, entry string, args []any, results []any, budget Budget) error {
	signature, ok := vm.program.EntrySignature(entry)
	if !ok {
		return fmt.Errorf("unknown entry %q", entry)
	}
	if len(results) != len(signature.Results) {
		return fmt.Errorf("argument/result count mismatch")
	}
	var output []any
	err := vm.RunNative(ctx, func(c *Call) error {
		inputs, err := c.NativeArguments(signature.Params, args)
		if err != nil {
			return err
		}
		if err := c.ChargeAllocation(uint64(len(results)) * uint64(unsafe.Sizeof(values.Value{})+unsafe.Sizeof(any(nil)))); err != nil {
			return err
		}
		returns := make([]values.Value, len(results))
		if err := c.RunEntry(entry, inputs, returns); err != nil {
			return err
		}
		output = make([]any, len(results))
		for i, value := range returns {
			if output[i], err = c.nativeResult(signature.Results[i], value); err != nil {
				return err
			}
		}
		return nil
	}, budget)
	if err != nil {
		return err
	}
	copy(results, output)
	return nil
}

// EntrySignature returns the signature of a named entry function.
func (p *Program) EntrySignature(entry string) (Signature, bool) {
	index, ok := p.entries[entry]
	if !ok {
		return Signature{}, false
	}
	return copySignature(p.functions[index].Signature), true
}

// RunEntry runs a named entry function inside this native call. It shares the
// call's VM state, context and budget, like a synchronous callback.
func (c *Call) RunEntry(entry string, args, results []values.Value) error {
	index, ok := c.program.entries[entry]
	if !ok {
		return fmt.Errorf("unknown entry %q", entry)
	}
	return c.runCallback(FunctionRef{Index: uint32(index)}, nil, c.program.functions[index].Signature, nil, args, results)
}

// nativeResult converts a result of the given type to a Go value. Scalars
// convert by their static type, so the program need not link an interface
// form of that type.
func (c *Call) nativeResult(t *values.Type, value values.Value) (any, error) {
	if shape := c.program.Shape(t); shape != nil && shape.Kind == values.InterfaceKind {
		return c.NativeAny(value)
	}
	if x, ok, err := c.nativeScalarAny(t, value); ok || err != nil {
		return x, err
	}
	packed, err := c.program.PackInterface(t, value)
	if err != nil {
		return nil, err
	}
	return c.NativeAny(packed)
}

// NativeArguments converts ordinary Go values into arguments for parameters
// of the given types: scalars, strings, time values, pointers to bound native
// structs, registered interface implementations and guest values. A Go slice
// or map converts when a linked package binds its type. A values.Value passes
// through unchanged. Native references keep their backing storage. A value whose
// Go type does not correspond to its parameter type is an error; there are no
// implicit numeric conversions. Conversion charges this call's allocation
// budget, and the results are valid for this program only.
func (c *Call) NativeArguments(params []*values.Type, args []any) ([]values.Value, error) {
	if len(args) != len(params) {
		return nil, fmt.Errorf("got %d arguments for %d parameters", len(args), len(params))
	}
	if err := c.ChargeAllocation(uint64(len(args)) * uint64(unsafe.Sizeof(values.Value{}))); err != nil {
		return nil, err
	}
	return c.nativeArguments(params, args)
}

func (c *Call) nativeArguments(params []*values.Type, args []any) ([]values.Value, error) {
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

// RunNative runs host work inside this VM's execution context. Stored script
// callbacks invoked by call share this VM's globals and the supplied budget.
// Like Run, it requires an idle VM and is not safe for concurrent use.
func (vm *VM) RunNative(ctx context.Context, call Native, budget Budget) error {
	entry, err := vm.program.NativeFunction(nativeEntryType, call)
	if err != nil {
		return err
	}
	fn, err := vm.program.FunctionValue(entry)
	if err != nil {
		return err
	}
	return vm.RunFunctionWithBudget(ctx, fn, nil, nil, budget)
}
