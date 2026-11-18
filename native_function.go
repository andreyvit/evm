package evm

import (
	"math"
	"unsafe"

	"github.com/andreyvit/evm/values"
)

// NativeFunctionType caches the signature and register layout of runtime native
// functions. Build it once with the bindings and reuse it across invocations.
type NativeFunctionType struct{ native linkedNative }

func NewNativeFunctionType(signature Signature) (*NativeFunctionType, error) {
	signature = copySignature(signature)
	params, err := LayoutOf(signature.Params)
	if err != nil {
		return nil, err
	}
	results, err := LayoutOf(signature.Results)
	if err != nil {
		return nil, err
	}
	return &NativeFunctionType{native: linkNative(ExternalFunction{Signature: signature}, params, results)}, nil
}

const nativeFunctionValue = math.MaxUint64
const nativeFunctionIndex = math.MaxUint32

type nativeFunction struct {
	program *Program
	typ     *NativeFunctionType
	call    Native
	data    any
}

// NativeFunction binds a native closure to the current program. The closure may
// retain owned host state, but must not capture Call or its borrowed registers.
// Calls from guest code and RunCallbackArgs share the active execution budget.
// Host code remains responsible for validating captured transaction lifetimes.
func (c *Call) NativeFunction(typ *NativeFunctionType, fn Native) (values.Value, error) {
	if fn == nil {
		return values.Value{}, nil
	}
	if typ == nil {
		return values.Value{}, ErrFunction
	}
	if err := charge(uint64(unsafe.Sizeof(nativeFunction{})), &c.budget.allocated, c.budget.limits); err != nil {
		return values.Value{}, err
	}
	return c.program.NativeFunction(typ, fn)
}

// NativeFunction prepares a host-owned callable for an argument to Run. The
// caller owns allocation accounting for this input. Calls from the VM share
// its active execution limits; host code must honor cancellation and meter
// any work or result allocations it performs. Use Call.NativeFunction for
// values created during a VM invocation so the holder allocation is metered.
func (p *Program) NativeFunction(typ *NativeFunctionType, fn Native) (values.Value, error) {
	if fn == nil {
		return values.Value{}, nil
	}
	if p == nil || typ == nil {
		return values.Value{}, ErrFunction
	}
	f := &nativeFunction{program: p, typ: typ, call: fn}
	return values.Value{R: nativeFunctionValue, P: unsafe.Pointer(f)}, nil
}

func (p *Program) nativeFunction(v values.Value) (*nativeFunction, error) {
	if v.R != nativeFunctionValue || v.P == nil {
		return nil, ErrFunction
	}
	f := (*nativeFunction)(v.P)
	if f.program != p || f.typ == nil || f.call == nil {
		return nil, ErrFunction
	}
	return f, nil
}

// NativeFunctionWithData retains immutable host metadata with a callable.
// Bindings use it to preserve the original native function across round trips.
func (c *Call) NativeFunctionWithData(typ *NativeFunctionType, fn Native, data any) (values.Value, error) {
	v, err := c.NativeFunction(typ, fn)
	if err != nil || v.P == nil {
		return v, err
	}
	(*nativeFunction)(v.P).data = data
	return v, nil
}

// NativeFunctionData returns metadata attached to a native callable from this
// program. Guest callables and native functions without metadata return nil.
func (p *Program) NativeFunctionData(v values.Value) any {
	f, err := p.nativeFunction(v)
	if err != nil {
		return nil
	}
	return f.data
}
