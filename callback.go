package evm

import (
	"errors"
	"unsafe"

	"github.com/andreyvit/evm/values"
)

func (vm *VM) loadCaptures(f frame, fn *linkedFunction, env unsafe.Pointer) {
	if len(fn.Captures) == 0 {
		return
	}
	captures := values.CellSlice(env, uint64(len(fn.Captures)))
	for i, c := range fn.Captures {
		vm.write(f, Operand{c.Slot, c.Type.Repr()}, captures[i])
	}
}

// RunCallback runs a borrowed func() on the native call's goroutine. Its
// registers are separate so Go can keep using the original Call window.
// Globals, captured cells, transaction scopes and all budgets stay shared.
func (c *Call) RunCallback(v values.Value) error {
	return c.RunCallbackArgs(v, nil, nil)
}

// RunCallbackArgs invokes a borrowed function with its declared arguments and
// results. The caller supplies values matching the function's static signature.
// Captures, globals and budgets remain shared with the suspended invocation.
// Results are written only on success.
func (c *Call) RunCallbackArgs(v values.Value, args, results []values.Value) (err error) {
	if v.R == 0 {
		return ErrNilFunction
	}
	var ref FunctionRef
	var native *nativeFunction
	var sig Signature
	if v.R == nativeFunctionValue {
		var err error
		native, err = c.program.nativeFunction(v)
		if err != nil {
			return err
		}
		ref = FunctionRef{Native: true, Index: nativeFunctionIndex}
		sig = native.typ.native.Signature
	} else {
		if v.R > uint64(len(c.program.functionRefs)) {
			return ErrFunction
		}
		ref = c.program.functionRefs[v.R-1]
		var ok bool
		sig, ok = c.program.functionSignature(ref)
		if !ok {
			return ErrFunction
		}
	}
	return c.runCallback(ref, native, sig, v.P, args, results)
}

func (c *Call) runCallback(ref FunctionRef, native *nativeFunction, sig Signature, captures unsafe.Pointer, args, results []values.Value) (err error) {
	if len(sig.Params) != len(args) || len(sig.Results) != len(results) {
		return ErrFunction
	}
	b := c.budget
	if err := b.check(b.remaining); err != nil {
		return err
	}
	parent := b.limits
	if depth := len(c.vm.stack) + 1; depth < parent.Depth {
		b.limits.Depth -= depth
	} else {
		return ErrDepth
	}
	b.limits.RSlots -= c.usedR
	b.limits.PSlots -= c.usedP
	defer func() {
		b.limits = parent
		c.Context = b.ctx
		var panicError *PanicError
		if err != nil && !errors.As(err, &panicError) {
			b.err = err
		}
	}()
	if err := charge(uint64(unsafe.Sizeof(VM{})), &b.allocated, b.limits); err != nil {
		return err
	}
	vm := &VM{coverage: c.vm.coverage, program: c.program, globals: c.vm.globals, callbackParent: c.vm, Output: c.output, deferGeneration: 1}
	vm.call = Call{vm: vm, program: c.program, budget: b, Context: b.ctx, Position: c.Position, output: c.output}
	defer recoverGuestErrorPanic(&vm.call, &err)
	if ref.Native {
		var g *linkedNative
		var call Native
		if native != nil {
			g, call = &native.typ.native, native.call
		} else {
			if captures != nil {
				return ErrFunction
			}
			g = c.program.native(ref.Index)
			call = g.Call
		}
		if err := vm.grow(g.rSize, g.pSize, b); err != nil {
			return err
		}
		vm.call.native = g
		vm.call.R, vm.call.P = vm.r[:g.rSize], vm.p[:g.pSize]
		vm.call.usedR, vm.call.usedP = g.rSize, g.pSize
		for i, arg := range args {
			if arg.R == values.NativeBorrow && arg.P != nil {
				arg, err = vm.call.importBorrow(sig.Params[i], arg)
				if err != nil {
					return err
				}
			}
			vm.write(frame{}, Operand{g.params.Slots[i], sig.Params[i].Repr()}, arg)
		}

		leaveNativeCall := vm.call.enterNativeCall()
		defer leaveNativeCall()
		if err := call(&vm.call); err != nil {
			return err
		}
		if err := b.check(b.remaining); err != nil {
			return err
		}
		for i := range results {
			results[i] = vm.read(frame{}, Operand{g.results.Slots[i], sig.Results[i].Repr()})
		}
		return nil
	}
	if len(c.program.functions[ref.Index].Captures) != 0 && captures == nil {
		return ErrFunction
	}
	return vm.executeSelected(int(ref.Index), captures, args, results, b)
}

// RunInterfaceMethod invokes a guest interface method synchronously with the
// active invocation's globals and budget. Arguments exclude the receiver.
func (c *Call) RunInterfaceMethod(value values.Value, name string, signature Signature, args, results []values.Value) error {
	if !c.program.hasInterfaceMethod(value, name, signature) {
		return ErrFunction
	}
	shape, receiver, err := c.program.typeShapes.Unpack(value)
	if err != nil {
		return err
	}
	if shape == nil {
		return ErrNil
	}
	id := c.program.typeShapes.Index(value)
	methodID, ok := c.program.lookupMethodID(name)
	if !ok {
		return ErrFunction
	}
	method := c.program.types[id].methods[methodID]
	if method == nil {
		return ErrFunction
	}
	sig, ok := c.program.functionSignature(method.Function)
	if !ok {
		return ErrFunction
	}
	if err := c.ChargeAllocation(uint64(len(args)+1) * uint64(unsafe.Sizeof(values.Value{}))); err != nil {
		return err
	}
	params := make([]values.Value, len(args)+1)
	params[0] = receiver
	copy(params[1:], args)
	return c.runCallback(method.Function, nil, sig, nil, params, results)
}
