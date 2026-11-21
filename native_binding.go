package evm

import (
	"context"
	"fmt"
	"math"
	"unsafe"

	"github.com/andreyvit/evm/values"
)

// AnySliceType is the shared VM slice identity for native packages exchanging []any.
var AnySliceType = values.NewType("[]any", values.Pair)

// ByteSliceType is the shared VM slice identity for native packages exchanging []byte.
var ByteSliceType = values.NewOwnedType("[]uint8", values.Pair, func(v values.Value, _ values.OwnershipBudget) (values.Value, error) {
	return v, v.OwnBytes()
})

// PointerInterface packs a trusted native pointer for an interface argument to Run.
// The type must be registered by the host and linked into this particular program.
func (p *Program) PointerInterface(t *values.Type, ptr unsafe.Pointer) (values.Value, error) {
	id, ok := p.typeIDs[t]
	if !ok || p.types[id].shape.Kind != values.PointerKind {
		return values.Value{}, fmt.Errorf("unlinked native pointer type")
	}
	if p.types[id].shape.Type.Repr() == values.Pair {
		return p.PackInterface(t, values.Value{R: values.NativeAddress, P: ptr})
	}
	return values.Value{R: uint64(id), P: ptr}, nil
}

// CallArgument reads an argument in the current Go binding.
func (vm *VM) CallArgument(i int) values.Value { return vm.argument(i) }

// SetCallResult writes a result in the current Go binding.
func (c *VM) SetCallResult(i int, v values.Value) {
	if v.R == values.NativeBorrow && v.P != nil {
		var err error
		v, err = c.importBorrow(c.native.Signature.Results[i], v)
		if err != nil {
			c.budget.err = err
			return
		}
	}
	c.result(i, v)
}
func (c *VM) AllocCells(n uint64) (unsafe.Pointer, error) {
	p, err := allocCells(n, &c.budget.allocated, c.budget.limits)
	if err != nil {
		c.budget.err = err
	}
	return p, err
}
func (c *VM) Program() *Program { return c.program }
func (p *Program) InterfaceType(v values.Value) *values.Type {
	id := p.typeShapes.Index(v)
	if id == 0 || id >= uint64(len(p.types)) {
		return nil
	}
	return p.types[id].shape.Type
}

// Shape returns immutable linked storage metadata for a host adapter, or nil
// for a type the module does not reach.
func (p *Program) Shape(t *values.Type) *values.Shape {
	// Small enough to inline, as native calls expect of a program without
	// shared shapes.
	if p.sharedShapes != nil {
		return p.sharedShape(t)
	}
	return p.shapeByType[t]
}

// sharedShape looks among the environment's snapshots first: a host type
// costs one map read and a bit test. Inlining it would make Shape too large
// to inline.
//
//go:noinline
func (p *Program) sharedShape(t *values.Type) *values.Shape {
	if s := p.sharedShapes.ofType(t, p.sharedSeen); s != nil {
		return s
	}
	return p.shapeByType[t]
}

// UnpackInterface reads a program-local interface payload for a host adapter.
func (p *Program) UnpackInterface(v values.Value) (*values.Shape, values.Value, error) {
	return p.typeShapes.Unpack(v)
}

// PackInterface prepares a host input. The caller owns allocation limits and
// snapshots aggregate values before passing them here. Native results should
// use Call.SetInterface, which charges the active VM budget.
func (p *Program) PackInterface(t *values.Type, v values.Value) (values.Value, error) {
	if t == nil {
		return values.Value{}, nil
	}
	id, ok := p.typeIDs[t]
	if !ok {
		return values.Value{}, fmt.Errorf("unlinked interface type %s", t.Name())
	}
	if v.R == values.NativeBorrow && v.P != nil {
		// Like other host-prepared inputs, packing happens outside a VM run.
		b := executionBudget{limits: Limits{AllocationBytes: math.MaxUint64, Allocations: math.MaxUint64}}
		c := VM{program: p, budget: &b, Context: context.Background()}
		var err error
		v, err = c.importBorrow(t, v)
		if err != nil {
			return values.Value{}, err
		}
	}
	if result, ok := p.typeShapes.Inline(uint64(id), v); ok {
		return result, nil
	}
	box := new(values.Value)
	*box = v
	return values.Value{R: uint64(id), P: unsafe.Pointer(box)}, nil
}

// ChargeAllocation accounts for native result payloads retained by the VM.
func (c *VM) ChargeAllocation(bytes uint64) error {
	err := c.reserve(bytes)
	if err != nil {
		c.budget.err = err
	}
	return err
}

// AssertValue performs a Go type assertion on a program-local interface value.
// Concrete targets return their payload; interface targets retain the interface.
// It neither converts nor copies the underlying value.
func (p *Program) AssertValue(v values.Value, target *values.Type) (values.Value, bool, error) {
	if target == nil {
		return values.Value{}, false, fmt.Errorf("nil assertion type")
	}
	s := p.Shape(target)
	if s == nil {
		return values.Value{}, false, fmt.Errorf("unlinked assertion type %s", target.Name())
	}
	result, ok, err := p.assert(v, s, uint64(p.typeIDs[target]))
	if !ok {
		result = values.Value{}
	}
	return result, ok, err
}
