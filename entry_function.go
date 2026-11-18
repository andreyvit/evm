package evm

import (
	"fmt"

	"github.com/andreyvit/evm/values"
)

// NamedFunction identifies a public, noncapturing guest function. It contains no
// VM state and may be retained by a host registration after the call returns.
type NamedFunction struct {
	Name      string
	Signature Signature
}

// NamedFunctionOf annotates the guest signature of a native function handle. It is an
// alias, so native callers retain an ordinary *NamedFunction. Binding generators
// expose *NamedFunctionOf[func(...)] as that function type rather than a Go pointer.
// Use NamedFunctionOf[any] for signatures checked by the host at registration.
// Scripts pass any; only public noncapturing guest functions are accepted.
type NamedFunctionOf[Signature any] = NamedFunction

// NamedFunction resolves a function passed through an interface. Captures,
// methods and native functions cannot become independently callable entries.
func (p *Program) NamedFunction(v values.Value) (*NamedFunction, error) {
	shape, value, err := p.typeShapes.Unpack(v)
	if err != nil {
		return nil, err
	}
	if shape == nil || shape.Kind != values.FunctionKind {
		return nil, fmt.Errorf("expected a named, noncapturing guest function")
	}
	return p.NamedFunctionValue(value)
}

// NamedFunctionValue resolves a statically typed guest function argument without
// first boxing it in an interface. The same restrictions as NamedFunction apply.
func (p *Program) NamedFunctionValue(value values.Value) (*NamedFunction, error) {
	if value.R == 0 || value.R > uint64(len(p.functionRefs)) || value.P != nil {
		return nil, fmt.Errorf("expected a named, noncapturing guest function")
	}
	ref := p.functionRefs[value.R-1]
	if ref.Native {
		return nil, fmt.Errorf("expected a guest function")
	}
	f := &p.functions[ref.Index]
	if f.Internal || f.Method || len(f.Captures) != 0 {
		return nil, fmt.Errorf("expected a public, noncapturing guest function")
	}
	return &NamedFunction{Name: f.Name, Signature: copySignature(f.Signature)}, nil
}

// EntryFunction resolves a registered named function in this linked program.
// The function must have been referenced as a value when the program was built.
// Resolution does not modify the program or retain invocation state.
func (p *Program) EntryFunction(named *NamedFunction) (*FunctionHandle, error) {
	if named == nil {
		return nil, ErrNilFunction
	}
	index, ok := p.entries[named.Name]
	if !ok {
		return nil, ErrFunction
	}
	fn := &p.functions[index]
	if !sameSignature(fn.Signature, named.Signature) {
		return nil, ErrFunction
	}
	for i, ref := range p.functionRefs {
		if !ref.Native && int(ref.Index) == index {
			return p.FunctionValue(values.Value{R: uint64(i + 1)})
		}
	}
	return nil, ErrFunction
}
