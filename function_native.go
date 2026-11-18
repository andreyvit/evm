package evm

import (
	"context"
	"fmt"
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
		inputs := make([]values.Value, len(args))
		returns := make([]values.Value, len(results))
		for i, arg := range args {
			v, err := c.AnyValue(arg)
			if err != nil {
				return err
			}
			expected := p.Shape(f.signature.Params[i])
			if expected != nil && expected.Kind == values.InterfaceKind {
				if v != (values.Value{}) {
					_, ok, err := p.assert(v, expected, 0)
					if err != nil {
						return err
					}
					if !ok {
						return fmt.Errorf("native callback argument %d does not implement %s", i, expected.Type.Name())
					}
				}
				inputs[i] = v
				continue
			}
			shape, value, err := p.UnpackInterface(v)
			if err != nil {
				return err
			}
			if shape == nil || shape.Type != f.signature.Params[i] {
				return fmt.Errorf("native callback argument %d has incompatible type", i)
			}
			inputs[i] = value
		}
		if err := c.RunCallbackArgs(f.value, inputs, returns); err != nil {
			return err
		}
		output = make([]any, len(results))
		for i, value := range returns {
			if shape := p.Shape(f.signature.Results[i]); shape == nil || shape.Kind != values.InterfaceKind {
				var err error
				value, err = p.PackInterface(f.signature.Results[i], value)
				if err != nil {
					return err
				}
			}
			var err error
			output[i], err = c.NativeAny(value)
			if err != nil {
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
