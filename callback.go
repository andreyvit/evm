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

func (c *VM) runValue(v values.Value, args, results []values.Value) (err error) {
	if v.R == 0 {
		return ErrNilFunction
	}
	var ref FunctionRef
	var native *nativeFunction
	var sig Signature
	if v.R == boundMethodValue {
		return c.runBoundMethod((*boundMethod)(v.P), args, results)
	}
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

func (c *VM) runCallback(ref FunctionRef, native *nativeFunction, sig Signature, captures unsafe.Pointer, args, results []values.Value) (err error) {
	if len(sig.Params) != len(args) || len(sig.Results) != len(results) {
		return ErrFunction
	}
	b := c.budget
	if err := b.check(b.remaining); err != nil {
		return err
	}
	vm := c
	// A native root uses the lease's own registers. Only a binding that
	// re-enters EVM has an outer register window to preserve.
	if c.inNativeCall {
		parent := b.limits
		if depth := len(c.stack) + 1; depth < parent.Depth {
			b.limits.Depth -= depth
		} else {
			return ErrDepth
		}
		b.limits.RSlots -= c.usedR
		b.limits.PSlots -= c.usedP
		defer func() {
			b.limits.Depth, b.limits.RSlots, b.limits.PSlots = parent.Depth, parent.RSlots, parent.PSlots
			c.Context = b.ctx
			if err != nil {
				var panicError *PanicError
				if !errors.As(err, &panicError) {
					b.err = err
				}
			}
		}()
		saved := vm.enterCallback()
		defer saved.restore(vm)
	}
	defer recoverGuestErrorPanic(vm, &err)
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
		vm.native = g
		vm.R, vm.P = vm.r[:g.rSize], vm.p[:g.pSize]
		vm.usedR, vm.usedP = g.rSize, g.pSize
		for i, arg := range args {
			if arg.R == values.NativeBorrow && arg.P != nil {
				arg, err = vm.importBorrow(sig.Params[i], arg)
				if err != nil {
					return err
				}
			}
			vm.write(frame{}, Operand{g.params.Slots[i], sig.Params[i].Repr()}, arg)
		}

		leaveNativeCall := vm.enterNativeCall()
		defer leaveNativeCall()
		if err := call(vm); err != nil {
			return err
		}
		if b.err != nil {
			return b.err
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

// LookupMethod resolves a method on this receiver and checks its receiver-free signature.
func (vm *VM) LookupMethod(value values.Value, name string, signature Signature) (Callable, error) {
	p := vm.program
	if !p.hasInterfaceMethod(value, name, signature) {
		return Callable{}, ErrFunction
	}
	_, receiver, err := p.UnpackInterface(value)
	if err != nil {
		return Callable{}, err
	}
	id, _ := p.lookupMethodID(name)
	method := p.types[p.typeShapes.Index(value)].methods[id]
	full, ok := p.functionSignature(method.Function)
	if !ok {
		return Callable{}, ErrFunction
	}
	if err := vm.ChargeAllocation(uint64(unsafe.Sizeof(boundMethod{}))); err != nil {
		return Callable{}, err
	}
	bound := &boundMethod{ref: method.Function, signature: full, receiver: receiver}
	bound.target = callTarget{program: p, index: -1, number: boundMethodValue, signature: &method.Signature}
	return Callable{&bound.target, unsafe.Pointer(bound)}, nil
}

type boundMethod struct {
	target    callTarget
	ref       FunctionRef
	signature Signature
	receiver  values.Value
}

const boundMethodValue = nativeFunctionValue - 1

func (vm *VM) runBoundMethod(method *boundMethod, args, results []values.Value) error {
	var local [8]values.Value
	n := len(args) + 1
	var inputs []values.Value
	if n <= len(local) {
		inputs = local[:n]
	} else {
		if err := vm.ChargeAllocation(uint64(n) * uint64(unsafe.Sizeof(values.Value{}))); err != nil {
			return err
		}
		inputs = make([]values.Value, n)
	}
	inputs[0] = method.receiver
	copy(inputs[1:], args)
	return vm.runCallback(method.ref, nil, method.signature, nil, inputs, results)
}
