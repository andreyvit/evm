package evm

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"unsafe"

	"github.com/andreyvit/evm/values"
)

// ErrorSliceType is shared by native error methods and guest interfaces.
var ErrorSliceType = values.NewType("[]error", values.Pair)

var nativeErrorShapes = func() [6]*values.Shape {
	var shapes [6]*values.Shape
	for i := range shapes {
		shapes[i] = &values.Shape{Type: values.NewType(fmt.Sprintf("runtime.nativeError%d", i), values.Pointer), Kind: values.NativeValueKind, Cells: 1}
	}
	return shapes
}()

func nativeErrorProtocol(err error) int {
	n := 0
	if _, ok := err.(interface{ Unwrap() error }); ok {
		n = 1
	} else if _, ok := err.(interface{ Unwrap() []error }); ok {
		n = 2
	}
	if _, ok := err.(interface{ Is(error) bool }); ok {
		n += 3
	}
	return n
}

func (p *Program) linkNativeErrors(shapes map[*values.Shape]*values.Shape) error {
	needed := false
	for _, s := range shapes {
		if s.Kind == values.InterfaceKind {
			needed = true
			break
		}
	}
	if !needed {
		return nil
	}
	for i, s := range nativeErrorShapes {
		d := linkedDynamicType{shape: s, methods: make(map[uint32]*DynamicMethod)}
		names := []string{"Error"}
		if i%3 != 0 {
			names = append(names, "Unwrap")
		}
		if i >= 3 {
			names = append(names, "Is")
		}
		for _, name := range names {
			sig := Signature{Params: []*values.Type{s.Type}, Results: []*values.Type{values.StringType}}
			var call Native
			switch name {
			case "Error":
				call = func(c *Call) error {
					text := c.Argument(0).AsNativeValue().(error).Error()
					if err := c.ChargeAllocation(uint64(len(text))); err != nil {
						return err
					}
					c.SetResult(0, values.String(text))
					return nil
				}
			case "Unwrap":
				sig.Results = []*values.Type{values.ErrorType}
				if i%3 == 1 {
					call = func(c *Call) error {
						return c.SetError(0, c.Argument(0).AsNativeValue().(interface{ Unwrap() error }).Unwrap())
					}
				} else {
					sig.Results = []*values.Type{ErrorSliceType}
					call = func(c *Call) error {
						errs := c.Argument(0).AsNativeValue().(interface{ Unwrap() []error }).Unwrap()
						if uint64(len(errs)) > math.MaxUint32 {
							return ErrAllocation
						}
						if errs == nil {
							c.SetResult(0, values.Value{})
							return nil
						}
						ptr, err := c.AllocCells(uint64(max(1, len(errs))))
						if err != nil {
							return err
						}
						for j, e := range errs {
							v, err := c.ErrorValue(e)
							if err != nil {
								return err
							}
							*(*values.Value)(values.CellAt(ptr, uint64(j))) = v
						}
						c.SetResult(0, values.SliceValue(ptr, uint32(len(errs)), uint32(len(errs))))
						return nil
					}
				}
			case "Is":
				sig.Params = append(sig.Params, values.ErrorType)
				sig.Results = []*values.Type{values.Boolean}
				call = func(c *Call) error {
					e, err := c.nativeErrorArgument(c.Argument(1))
					if err != nil {
						return err
					}
					c.SetResult(0, values.Bool(c.Argument(0).AsNativeValue().(interface{ Is(error) bool }).Is(e)))
					return nil
				}
			}
			params, err := ParameterLayout(sig.Params, true)
			if err != nil {
				return err
			}
			results, err := LayoutOf(sig.Results)
			if err != nil {
				return err
			}
			ref := FunctionRef{Native: true, Index: uint32(len(p.natives))}
			p.natives = append(p.natives, linkNative(ExternalFunction{Name: s.Type.Name() + "." + name, Signature: sig, Call: nativeErrorCall(call), panics: true}, params, results))
			d.methods[p.methodID(name)] = &DynamicMethod{ID: name, Signature: Signature{Params: sig.Params[1:], Results: sig.Results}, Function: ref}
		}
		p.typeIDs[s.Type] = uint32(len(p.types))
		p.types = append(p.types, d)
	}
	return nil
}

// ErrorValue preserves a native error's identity and optional error methods.
func (c *Call) ErrorValue(err error) (values.Value, error) {
	if err == nil {
		return values.Value{}, nil
	}
	if guest, ok := err.(guestErrorCarrier); ok {
		e := guest.guestErrorValue()
		if e.host.program != c.program {
			return values.Value{}, fmt.Errorf("guest error belongs to another program")
		}
		return e.value, nil
	}

	// Use a linked concrete type only when its exposed methods include Error.
	if s := c.program.nativeGoShapes[reflect.TypeOf(err)]; s != nil {
		id := c.program.typeIDs[s.Type]
		methodID, exists := c.program.methodIDs["Error"]
		if method := c.program.types[id].methods[methodID]; exists && method != nil && len(method.Signature.Params) == 0 && len(method.Signature.Results) == 1 && method.Signature.Results[0] == values.StringType {
			return c.anyValue(err)
		}
	}
	return c.nativeErrorValue(err)
}
func (c *Call) nativeErrorValue(err error) (values.Value, error) {
	s := nativeErrorShapes[nativeErrorProtocol(err)]
	id := c.program.typeIDs[s.Type]
	if id == 0 {
		return values.Value{}, fmt.Errorf("native error types are not linked")
	}
	if err := c.ChargeAllocation(uint64(unsafe.Sizeof(any(nil)))); err != nil {
		return values.Value{}, err
	}
	return c.program.packInterface(uint64(id), values.NativeValue(err), &c.budget.allocated, c.budget.limits)
}

// SetError writes an error result by result index, rather than ABI slot.
func (c *Call) SetError(result int, err error) error {
	v, e := c.ErrorValue(err)
	if e != nil {
		return e
	}
	c.SetResult(result, v)
	return nil
}

type nativePanic struct {
	value any
	guest *PanicError
}

func (p *nativePanic) Error() string {
	if p.guest != nil {
		return p.guest.Error()
	}
	return fmt.Sprint(p.value)
}

func nativeErrorCall(fn Native) Native {
	return func(c *Call) (err error) {
		defer func() {
			if p := recover(); p != nil {
				if guest, ok := p.(guestErrorPanic); ok {
					err = c.guestErrorFailure(guest.err)
				} else {
					err = &nativePanic{value: p}
				}
			}
		}()
		return fn(c)
	}
}

func (c *Call) guestErrorFailure(err error) error {
	var p *PanicError
	if errors.As(err, &p) && c.program.typeIDs[p.Type] != 0 {
		return &nativePanic{guest: p}
	}
	return err
}
func recoverGuestErrorPanic(c *Call, err *error) {
	if p := recover(); p != nil {
		if guest, ok := p.(guestErrorPanic); ok {
			*err = c.guestErrorFailure(guest.err)
		} else {
			panic(p)
		}
	}
}

// NativeErrorCodec stores native error interfaces using their Go layout.
var NativeErrorCodec = &NativeValueCodec{
	Decode: func(c *Call, v values.Value) (values.Value, error) { return c.ErrorValue(*(*error)(v.P)) },
	Encode: func(c *Call, v values.Value) (values.Value, error) {
		result, err := c.NativeError(v)
		if err != nil {
			return values.Value{}, err
		}
		if err := c.ChargeAllocation(uint64(unsafe.Sizeof(result))); err != nil {
			return values.Value{}, err
		}
		return values.Value{P: unsafe.Pointer(&result)}, nil
	},
}

// CallbackError preserves an in-flight guest panic across a native callback.
// Execution failures retain their trap behavior.
func (c *Call) CallbackError(err error) error { return c.guestErrorFailure(err) }

// PreserveCallbackPanic returns an in-flight callback panic unchanged.
// Wrapped or replaced native panics must not recover the original guest value.
func PreserveCallbackPanic(value any) error {
	if p, ok := value.(*nativePanic); ok {
		return p
	}
	return nil
}

// PanicCallback propagates callback execution failure through a native function
// whose Go signature cannot return that failure. VM native dispatch recovers it.
func (c *Call) PanicCallback(err error) {
	var guest *PanicError
	if !errors.As(err, &guest) {
		c.budget.err = err
	}
	panic(guestErrorPanic{err})
}
