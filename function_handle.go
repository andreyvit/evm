package evm

import (
	"fmt"
	"strings"
	"unsafe"

	"github.com/andreyvit/evm/values"
)

// Callable retains a linked function and its optional captures or bound receiver.
// Copies share the same captures. The zero value is a nil function.
type Callable struct {
	*callTarget
	captures unsafe.Pointer
}

type callTarget struct {
	program   *Program
	index     int
	number    uint64
	signature *Signature
}

// FunctionOf annotates a retained callable in a binding declaration. S is the
// required function signature. Use any for host-checked registrations.
type FunctionOf[S any] = Callable

// linkCallables gives host entries stable function references, including entries
// that never occur as function values in bytecode. Existing numbers stay valid.
func (p *Program) linkCallables() {
	p.entryNumbers = make([]uint64, len(p.functions))
	for i, ref := range p.functionRefs {
		if !ref.Native {
			p.entryNumbers[ref.Index] = uint64(i + 1)
		}
	}
	for i := range p.functions {
		f := &p.functions[i]
		if index, ok := p.entries[f.Name]; ok && index == i && p.entryNumbers[i] == 0 {
			p.functionRefs = append(p.functionRefs, FunctionRef{Index: uint32(i)})
			p.entryNumbers[i] = uint64(len(p.functionRefs))
		}
	}
	p.targets = make([]callTarget, len(p.functionRefs))
	for i, ref := range p.functionRefs {
		var sig *Signature
		idx := -1
		if ref.Native {
			sig = &p.native(ref.Index).Signature
		} else {
			idx = int(ref.Index)
			sig = &p.functions[idx].Signature
		}
		p.targets[i] = callTarget{p, idx, uint64(i + 1), sig}
	}
}

// LookupFunction resolves a named entry and checks its full signature when want is non-nil.
func (p *Program) LookupFunction(name string, want *Signature) (Callable, error) {
	idx, ok := p.entries[name]
	if !ok {
		return Callable{}, fmt.Errorf("unknown function %q: %w", name, ErrFunction)
	}
	fn := Callable{callTarget: &p.targets[p.entryNumbers[idx]-1]}
	if want != nil {
		if err := fn.CheckSignature(*want); err != nil {
			return Callable{}, err
		}
	}
	return fn, nil
}

func signatureText(sig Signature) string {
	names := func(types []*values.Type) string {
		out := make([]string, len(types))
		for i, t := range types {
			if t == nil {
				out[i] = "<nil>"
			} else {
				out[i] = t.Name()
			}
		}
		return strings.Join(out, ", ")
	}
	return "func(" + names(sig.Params) + ") (" + names(sig.Results) + ")"
}

func (f Callable) CheckSignature(want Signature) error {
	if f.callTarget == nil {
		return ErrNilFunction
	}
	if !sameSignature(*f.signature, want) {
		return fmt.Errorf("%w: %s has %s, want %s", ErrFunction, f.Name(), signatureText(*f.signature), signatureText(want))
	}
	return nil
}

func (f Callable) guestValue() values.Value {
	return values.Value{R: f.number, P: f.captures}
}

func (f *Callable) Value(p *Program, signature Signature) (values.Value, error) {
	if f == nil || f.callTarget == nil {
		return values.Value{}, nil
	}
	if f.program != p || !sameSignature(*f.signature, signature) {
		return values.Value{}, ErrFunction
	}
	if f.number == boundMethodValue {
		b := (*boundMethod)(f.captures)
		typ, err := NewNativeFunctionType(*f.signature)
		if err != nil {
			return values.Value{}, err
		}
		nargs, nresults := len(f.signature.Params), len(f.signature.Results)
		return p.NativeFunction(typ, func(vm *VM) error {
			if err := vm.ChargeAllocation(uint64(nargs+nresults) * uint64(unsafe.Sizeof(values.Value{}))); err != nil {
				return err
			}
			args := make([]values.Value, nargs)
			for i := range args {
				args[i] = vm.CallArgument(i)
			}
			out := make([]values.Value, nresults)
			if err := vm.runBoundMethod(b, args, out); err != nil {
				return err
			}
			for i, v := range out {
				vm.SetCallResult(i, v)
			}
			return nil
		})
	}
	return f.guestValue(), nil
}

// Callable retains an argument whose declared guest type is a function.
func (p *Program) Callable(v values.Value) (Callable, error) {
	if v.R == 0 {
		return Callable{}, ErrNilFunction
	}
	if v.R == nativeFunctionValue {
		n, err := p.nativeFunction(v)
		if err != nil {
			return Callable{}, err
		}
		return Callable{&n.target, v.P}, nil
	}
	if v.R > uint64(len(p.targets)) {
		return Callable{}, ErrFunction
	}
	t := &p.targets[v.R-1]
	if (t.index >= 0 && len(p.functions[t.index].Captures) != 0) != (v.P != nil) {
		return Callable{}, ErrFunction
	}
	return Callable{t, v.P}, nil
}

func (f Callable) HasCaptures() bool { return f.callTarget != nil && f.captures != nil }
func (f Callable) Program() *Program {
	if f.callTarget == nil {
		return nil
	}
	return f.program
}
func (f Callable) Name() string {
	if f.callTarget == nil || f.index < 0 {
		return ""
	}
	return f.program.functions[f.index].Name
}
func (f Callable) Signature() Signature {
	if f.callTarget == nil {
		return Signature{}
	}
	return copySignature(*f.signature)
}
