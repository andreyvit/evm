package evm

import (
	"fmt"
	"unsafe"

	"github.com/andreyvit/evm/values"
)

// Guest error methods use the same execution lifetime as stored callbacks.
type guestErrorHost struct {
	program *Program
	scope   *CallbackScope
}
type guestError struct {
	host  *guestErrorHost
	value values.Value
}
type guestErrorCarrier interface{ guestErrorValue() guestError }

func (e guestError) guestErrorValue() guestError { return e }

type guestSingleError struct{ guestError }
type guestMultiError struct{ guestError }

func (e guestError) call(fn Native) error { return e.host.scope.WithCall(fn) }

func (e guestError) Error() string {
	var result [1]values.Value
	err := e.call(func(c *VM) error {
		return runErrorMethod(c, e.value, "Error", Signature{Results: []*values.Type{values.StringType}}, result[:])
	})
	if err != nil {
		panic(guestErrorPanic{err})
	}
	return result[0].AsString()
}

func (e guestError) Is(target error) bool {
	if other, ok := target.(guestErrorCarrier); ok {
		b := other.guestErrorValue()
		if e.host.program == b.host.program {
			shape, _, err := e.host.program.typeShapes.Unpack(b.value)
			if err != nil {
				panic(guestErrorPanic{err})
			}
			if shape != nil && shape.Comparable() {
				iface := values.Shape{Type: values.AnyType, Kind: values.InterfaceKind, Cells: 1}
				equal, err := iface.Equal(e.value, b.value, e.host.program.typeShapes)
				if err != nil {
					panic(guestErrorPanic{err})
				}
				if equal {
					return true
				}
			}
		}
	}
	signature := Signature{Params: []*values.Type{values.ErrorType}, Results: []*values.Type{values.Boolean}}
	if !e.host.program.hasInterfaceMethod(e.value, "Is", signature) {
		return false
	}
	var out [1]values.Value
	err := e.call(func(c *VM) error {
		arg, err := c.ErrorValue(target)
		if err != nil {
			return err
		}
		slots := [2]values.Value{arg}
		err = runErrorMethod(c, e.value, "Is", signature, slots[:])
		out[0] = slots[1]
		return err
	})
	if err != nil {
		panic(guestErrorPanic{err})
	}
	return out[0].R != 0
}

func (e guestSingleError) Unwrap() error {
	var result error
	err := e.call(func(c *VM) error {
		var out [1]values.Value
		if err := runErrorMethod(c, e.value, "Unwrap", Signature{Results: []*values.Type{values.ErrorType}}, out[:]); err != nil {
			return err
		}
		var err error
		result, err = c.nativeErrorArgument(out[0])
		return err
	})
	if err != nil {
		panic(guestErrorPanic{err})
	}
	return result
}
func (e guestMultiError) Unwrap() []error {
	var result []error
	err := e.call(func(c *VM) error {
		var out [1]values.Value
		if err := runErrorMethod(c, e.value, "Unwrap", Signature{Results: []*values.Type{ErrorSliceType}}, out[:]); err != nil {
			return err
		}
		v := out[0]
		if v.P == nil {
			return nil
		}
		result = make([]error, int(v.SliceLen()))
		for i := range result {
			var err error
			result[i], err = c.nativeErrorArgument(*(*values.Value)(values.CellAt(v.P, uint64(i))))
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		panic(guestErrorPanic{err})
	}
	return result
}

type guestErrorPanic struct{ err error }

func (p guestErrorPanic) Error() string { return p.err.Error() }
func (p guestErrorPanic) Unwrap() error { return p.err }

func (c *VM) nativeErrorArgument(value values.Value) (error, error) {
	x, err := c.NativeAny(value)
	if err != nil {
		return nil, err
	}
	if x == nil {
		return nil, nil
	}
	if e, ok := x.(error); ok {
		return e, nil
	}
	return nil, fmt.Errorf("value does not implement error")
}

func (c *VM) guestErrorHost() (*guestErrorHost, error) {
	vm := c
	if vm.guestErrors == nil {
		if err := c.ChargeAllocation(uint64(unsafe.Sizeof(guestErrorHost{}))); err != nil {
			return nil, err
		}
		vm.guestErrors = &guestErrorHost{program: c.program, scope: c.CallbackScope()}
	}
	return vm.guestErrors, nil
}

func (c *VM) adaptGuestError(value values.Value) (error, error) {
	host, err := c.guestErrorHost()
	if err != nil {
		return nil, err
	}
	if err := c.ChargeAllocation(uint64(unsafe.Sizeof(guestError{}))); err != nil {
		return nil, err
	}
	e := guestError{host: host, value: value}
	if c.program.hasInterfaceMethod(value, "Unwrap", Signature{Results: []*values.Type{values.ErrorType}}) {
		return guestSingleError{e}, nil
	}
	if c.program.hasInterfaceMethod(value, "Unwrap", Signature{Results: []*values.Type{ErrorSliceType}}) {
		return guestMultiError{e}, nil
	}
	return e, nil
}

func (p *Program) hasInterfaceMethod(value values.Value, name string, signature Signature) bool {
	id := p.typeShapes.Index(value)
	if id == 0 || id >= uint64(len(p.types)) {
		return false
	}
	methodID, ok := p.lookupMethodID(name)
	if !ok {
		return false
	}
	method := p.types[id].methods[methodID]
	return method != nil && !method.Variadic && sameSignature(method.Signature, signature)
}

// NativeError converts an error argument while preserving its identity.
// The second result reports conversion failure, not the argument's value.
func (c *VM) NativeError(value values.Value) (error, error) {
	return c.nativeErrorArgument(value)
}

func runErrorMethod(vm *VM, receiver values.Value, name string, sig Signature, slots []values.Value) error {
	fn, err := vm.LookupMethod(receiver, name, sig)
	if err != nil {
		return err
	}
	return vm.Run(fn, slots)
}
