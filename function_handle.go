package evm

import (
	"context"

	"github.com/andreyvit/evm/values"
)

// FunctionHandle retains a callable and its captures after an invocation returns.
// It belongs to one linked Program and can run in any VM of that program.
// Captures remain shared; callers must serialize access to mutable captures.
// Host resources in captures retain their own lifetime requirements.
type FunctionHandle struct {
	program   *Program
	value     values.Value
	index     int
	signature Signature
}

// FunctionOf annotates a native handle with its script signature.
// Binding generators expose *FunctionOf[func(...)] as the function itself.
// FunctionOf[any] is an input-only binding for host-checked signatures.
// It accepts named functions and closures; it does not convert arbitrary values.
type FunctionOf[Signature any] = FunctionHandle

// Value returns the callable for a matching program and signature.
func (f *FunctionHandle) Value(p *Program, signature Signature) (values.Value, error) {
	if f == nil {
		return values.Value{}, nil
	}
	if f.program != p || !sameSignature(f.signature, signature) {
		return values.Value{}, ErrFunction
	}
	return f.value, nil
}

// FunctionValue retains a statically typed function value, including closures,
// bound methods and native functions. It does not copy captured data.
func (p *Program) FunctionValue(v values.Value) (*FunctionHandle, error) {
	if v.R == 0 {
		return nil, ErrNilFunction
	}
	f := &FunctionHandle{program: p, value: v, index: -1}
	if v.R == nativeFunctionValue {
		n, err := p.nativeFunction(v)
		if err != nil {
			return nil, err
		}
		f.signature = n.typ.native.Signature
	} else {
		if v.R > uint64(len(p.functionRefs)) {
			return nil, ErrFunction
		}
		ref := p.functionRefs[v.R-1]
		if ref.Native {
			if v.P != nil {
				return nil, ErrFunction
			}
		} else {
			fn := &p.functions[ref.Index]
			if len(fn.Captures) > 0 && v.P == nil {
				return nil, ErrFunction
			}
			f.index = int(ref.Index)
		}
		var ok bool
		f.signature, ok = p.functionSignature(ref)
		if !ok {
			return nil, ErrFunction
		}
	}
	return f, nil
}

// HasCaptures reports whether the callable carries cells shared by all of its
// calls: closure captures or a bound receiver. A VM is no boundary for them,
// so a host that runs such a handle from several goroutines serializes it.
func (f *FunctionHandle) HasCaptures() bool { return f.index >= 0 && f.value.P != nil }

// Program returns the linked program that owns the callable and its captures.
// Hosts can use it to select program-specific execution metadata.
func (f *FunctionHandle) Program() *Program { return f.program }

// Name returns the compiled function name, or empty for a native callable.
func (f *FunctionHandle) Name() string {
	if f.index < 0 {
		return ""
	}
	return f.program.functions[f.index].Name
}

// Signature returns a copy of the callable's signature.
func (f *FunctionHandle) Signature() Signature { return copySignature(f.signature) }

// RunFunction invokes a retained callable using this VM's globals and limits.
func (vm *VM) RunFunction(ctx context.Context, f *FunctionHandle, args, results []values.Value) error {
	return vm.RunFunctionWithBudget(ctx, f, args, results, Budget{})
}

// RunFunctionWithBudget applies the same invocation budgets and initialization
// rules as RunWithBudget. A handle from another linked program is rejected.
func (vm *VM) RunFunctionWithBudget(ctx context.Context, f *FunctionHandle, args, results []values.Value, budget Budget) error {
	if f == nil {
		return ErrNilFunction
	}
	if f.program != vm.program {
		return ErrFunction
	}
	return vm.runFunction(ctx, f.index, f.value, f.signature, args, results, budget)
}
