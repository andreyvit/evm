package evm

import (
	"fmt"
	"github.com/andreyvit/evm/values"
	"slices"
)

// FunctionRef selects a linked code or host function. Function values store its
// module-local index plus one in R, with zero reserved for nil. Runtime native
// closures use a separate tagged value and retain their owning program.
//
// A native reference indexes Module.Imports. One at or above MethodImportBase
// names Module.MethodImports instead; only the method tables of Module.Types
// hold such references.
type FunctionRef struct {
	Native bool
	Index  uint32
}

// MethodImportBase is the Index of the reference to Module.MethodImports[0].
const MethodImportBase = 1 << 31

// MethodImportRef refers to Module.MethodImports[index].
func MethodImportRef(index int) FunctionRef {
	return FunctionRef{Native: true, Index: MethodImportBase + uint32(index)}
}

// MethodImport returns the index into Module.MethodImports that the
// reference names, and whether it names one.
func (r FunctionRef) MethodImport() (int, bool) {
	if !r.Native || r.Index < MethodImportBase || r.Index == nativeFunctionIndex {
		return 0, false
	}
	return int(r.Index - MethodImportBase), true
}

// native returns the native a linked reference names: one of the natives
// bytecode calls by index, or one that only method tables name.
func (p *Program) native(index uint32) *linkedNative {
	if index >= MethodImportBase {
		return &p.methodNatives[index-MethodImportBase]
	}
	return &p.natives[index]
}

type CaptureSlot struct {
	Type *values.Type
	Slot uint16
}
type Closure struct {
	Function uint32
	Captures []Operand
}
type linkedSignature struct {
	Signature
	params, results Layout
}

func rValid(r Operand, f *linkedFunction) bool { return int(r.Slot) < f.rSize }
func pValid(r Operand, f *linkedFunction) bool { return int(r.Slot) < f.pSize }
func (p *Program) functionSignature(ref FunctionRef) (Signature, bool) {
	if ref.Native {
		// Kept small enough to inline: an indirect call asks for the
		// signature of its target. A native function value has the
		// largest index and is in neither table.
		natives, index := p.natives, ref.Index
		if index >= MethodImportBase {
			natives, index = p.methodNatives, index-MethodImportBase
		}
		if uint64(index) >= uint64(len(natives)) {
			return Signature{}, false
		}
		return natives[index].Signature, true
	}
	if uint64(ref.Index) >= uint64(len(p.functions)) {
		return Signature{}, false
	}
	return p.functions[ref.Index].Signature, true
}
func (p *Program) linkFunctions(m *Module) error {
	p.functionRefs = slices.Clone(m.FunctionRefs)
	for _, ref := range p.functionRefs {
		// Function values name natives among the imports.
		if _, ok := p.functionSignature(ref); !ok || ref.Native && uint64(ref.Index) >= uint64(len(m.Imports)) {
			return fmt.Errorf("invalid function reference")
		}
		// Method expressions use a callable thunk with the function-value ABI.
		if ref.Native && m.Imports[ref.Index].Receiver != nil || !ref.Native && m.Functions[ref.Index].Method {
			return fmt.Errorf("method function reference requires a thunk")
		}
	}
	for i := range p.functions {
		f := &p.functions[i]
		if len(f.Captures) > 0 && !f.Internal {
			return fmt.Errorf("capturing function cannot be a public entry")
		}
		usedR, usedP := map[uint16]bool{}, map[uint16]bool{}
		for _, c := range f.Captures {
			if c.Type == nil || c.Type.Repr() < values.Raw || c.Type.Repr() > values.Pair || c.Type.Repr() != values.Pointer && int(c.Slot) >= f.RCount || c.Type.Repr() != values.Raw && int(c.Slot) >= f.PCount {
				return fmt.Errorf("invalid capture slot")
			}
			if c.Type.Repr() != values.Pointer {
				if int(c.Slot) < max(f.params.RCount, f.results.RCount) || usedR[c.Slot] {
					return fmt.Errorf("overlapping capture slot")
				}
				usedR[c.Slot] = true
			}
			if c.Type.Repr() != values.Raw {
				if int(c.Slot) < max(f.params.PCount, f.results.PCount) || usedP[c.Slot] {
					return fmt.Errorf("overlapping capture slot")
				}
				usedP[c.Slot] = true
			}
		}
	}
	for _, s := range m.Signatures {
		s = copySignature(s)
		args, err := LayoutOf(s.Params)
		if err != nil {
			return err
		}
		results, err := LayoutOf(s.Results)
		if err != nil {
			return err
		}
		p.signatures = append(p.signatures, linkedSignature{s, args, results})
	}
	for _, c := range m.Closures {
		if uint64(c.Function) >= uint64(len(p.functionRefs)) {
			return fmt.Errorf("invalid closure function")
		}
		ref := p.functionRefs[c.Function]
		if ref.Native {
			return fmt.Errorf("native functions cannot capture")
		}
		fn := &p.functions[ref.Index]
		if len(fn.Captures) != len(c.Captures) {
			return fmt.Errorf("closure capture count mismatch")
		}
		for i, r := range c.Captures {
			if r.Repr != fn.Captures[i].Type.Repr() {
				return fmt.Errorf("closure capture representation mismatch")
			}
		}
		c.Captures = slices.Clone(c.Captures)
		p.closures = append(p.closures, c)
	}
	return nil
}
