package evm

import (
	"github.com/andreyvit/evm/values"
	"go/constant"
)

// ExternalValue exposes a host-owned package value. Scripts may copy the value
// and use its methods, but cannot assign or take the address of its binding.
// Read must not invalidate borrowed records or change transaction scopes.
type ExternalValue struct {
	Name string
	Type *values.Type
	Read Native
}

func (v ExternalValue) LinkName() string { return "$value:" + v.Name }

// TypeAlias gives an existing type another source name without new storage
// or runtime identity. The target must be declared in the same environment.
type TypeAlias struct {
	Name   string
	Target *values.Type
}

type ExternalConstant struct {
	// Untyped preserves exact Go constants until the consuming expression supplies a type.
	Untyped constant.Value
	Rune    bool
	Name    string
	Type    *values.Type
	Value   values.Value
}

// TypeDecl exposes a named type, or an unnamed support type when Name is empty.
// Shape is declarative storage; native wrappers use the same cells as VM code.
// NativeValueCodec translates between native field values and VM values.
// Conversions may inspect the current program and charge allocations. They
// must not retain Call or change its argument/result window.
type NativeValueCodec struct {
	Decode func(*Call, values.Value) (values.Value, error)
	Encode func(*Call, values.Value) (values.Value, error)
}

type NativeSliceDecl struct {
	Layout, Element *values.NativeLayout
}

type TypeDecl struct {
	// NativeSlice describes a host representation without changing guest storage.
	NativeSlice *NativeSliceDecl
	TypeArgs    []*values.Type
	// SourceName identifies a type declared in package sources. TypeArgs
	// instantiates it using existing native type identities.
	SourceName  string
	NativeCodec *NativeValueCodec
	// ScalarBase distinguishes Go int from int64 when their storage shapes match.
	ScalarBase *values.Type
	Name       string
	Shape      *values.Shape
}

// ExternalMethod has a receiver-free source signature. Its native ABI prepends
// Receiver to the argument list, reserving incoming V0 via ParameterLayout.
type ExternalMethod struct {
	// Instantiate returns a receiver-free signature for a generic source method.
	// Ownership parameter indexes include the receiver at zero.
	Instantiate  func([]*values.Type) (ExternalFunction, error)
	Ownership    *OwnershipEffects
	Direct       DirectBinding
	Name         string
	Receiver     *values.Type
	Signature    Signature
	Variadic     bool
	Call         Native
	DynamicTypes []*values.Type
}

// Binding returns the native ABI declaration, including its receiver.
func (m ExternalMethod) Binding() ExternalFunction {
	f := ExternalFunction{Ownership: m.Ownership, Direct: m.Direct, Name: m.Name, Signature: Signature{Params: append([]*values.Type{m.Receiver}, m.Signature.Params...), Results: m.Signature.Results}, Variadic: m.Variadic, Call: m.Call, DynamicTypes: m.DynamicTypes}
	if m.Instantiate != nil {
		f.Instantiate = func(args []*values.Type) (ExternalFunction, error) {
			bound, err := m.Instantiate(args)
			if err != nil {
				return ExternalFunction{}, err
			}
			bound.Signature.Params = append([]*values.Type{m.Receiver}, bound.Signature.Params...)
			return bound, err
		}
	}
	return f
}

// OwnsMethodReceiver reports whether t is a named concrete type declared by p,
// or an explicitly declared pointer to one.
func (p *Package) OwnsMethodReceiver(t *values.Type) bool {
	for _, shared := range p.SharedTypes {
		if shared.Type == t {
			return true
		}
	}
	for _, r := range p.Records {
		if r != nil && r.Pointer == t {
			return true
		}
	}
	var base *values.Type
	for i := range p.Types {
		d := &p.Types[i]
		s := d.Shape
		if s == nil || s.Type != t {
			continue
		}
		if d.Name != "" || d.SourceName != "" {
			return s.Kind != values.InterfaceKind && s.Kind != values.PointerKind
		}
		if s.Kind == values.PointerKind && s.Elem != nil {
			base = s.Elem.Type
		}
	}
	if base != nil {
		for i := range p.Types {
			d := &p.Types[i]
			if (d.Name != "" || d.SourceName != "") && d.Shape != nil && d.Shape.Type == base {
				return d.Shape.Kind != values.InterfaceKind && d.Shape.Kind != values.PointerKind
			}
		}
	}
	return false
}

// methodReceivers answers OwnsMethodReceiver for every type of one package
// after a single pass over its declarations.
type methodReceivers struct {
	owned    map[*values.Type]bool         // shared types and record pointers
	concrete map[*values.Type]bool         // first named declaration: neither interface nor pointer
	pointers map[*values.Type]*values.Type // last unnamed pointer declaration to its element
}

func (p *Package) methodReceivers() methodReceivers {
	r := methodReceivers{owned: make(map[*values.Type]bool), concrete: make(map[*values.Type]bool), pointers: make(map[*values.Type]*values.Type)}
	for _, shared := range p.SharedTypes {
		r.owned[shared.Type] = true
	}
	for _, record := range p.Records {
		if record != nil {
			r.owned[record.Pointer] = true
		}
	}
	for i := range p.Types {
		d := &p.Types[i]
		s := d.Shape
		if s == nil {
			continue
		}
		if d.Name != "" || d.SourceName != "" {
			if _, seen := r.concrete[s.Type]; !seen {
				r.concrete[s.Type] = s.Kind != values.InterfaceKind && s.Kind != values.PointerKind
			}
		} else if s.Kind == values.PointerKind && s.Elem != nil {
			r.pointers[s.Type] = s.Elem.Type
		}
	}
	return r
}

func (r methodReceivers) owns(t *values.Type) bool {
	if r.owned[t] {
		return true
	}
	if concrete, named := r.concrete[t]; named {
		return concrete
	}
	if base, ok := r.pointers[t]; ok && base != nil {
		return r.concrete[base]
	}
	return false
}
