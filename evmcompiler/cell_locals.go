package evmcompiler

import (
	"go/token"
	"go/types"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/values"
	"golang.org/x/tools/go/ssa"
)

// A compact local whose address never escapes can live in a dedicated register.
// Keep a slot for the entire function: aliases through field addresses need not
// have the same SSA live range as the original allocation.
func (f *lowerer) findCellLocals() map[*ssa.Alloc]vreg {
	result := map[*ssa.Alloc]vreg{}
	for _, block := range f.fn.Blocks {
		for _, ins := range block.Instrs {
			a, ok := ins.(*ssa.Alloc)
			if !ok {
				continue
			}
			elem := a.Type().Underlying().(*types.Pointer).Elem()
			if _, ok := elem.Underlying().(*types.Struct); !ok {
				continue
			}
			s := f.shape(elem)
			if s == nil || s.CellStruct == nil {
				continue
			}
			safe := true
			for _, ref := range *a.Referrers() {
				switch ref := ref.(type) {
				case *ssa.Store:
					safe = ref.Addr == a
				case *ssa.UnOp:
					safe = ref.Op == token.MUL
				case *ssa.FieldAddr:
					for _, use := range *ref.Referrers() {
						switch use := use.(type) {
						case *ssa.Store:
							safe = use.Addr == ref
						case *ssa.UnOp:
							safe = use.Op == token.MUL
						default:
							safe = false
						}
						if !safe {
							break
						}
					}
				default:
					safe = false
				}
				if !safe {
					break
				}
			}
			if safe {
				result[a] = f.alloc(s.Type)
			}
		}
	}
	return result
}
func (f *lowerer) cellLocal(v ssa.Value) (vreg, *values.Shape, int, bool) {
	index := -1
	if field, ok := v.(*ssa.FieldAddr); ok {
		index = field.Field
		v = field.X
	}
	if a, ok := v.(*ssa.Alloc); ok {
		if reg, ok := f.cellLocals[a]; ok {
			return reg, f.shape(a.Type().Underlying().(*types.Pointer).Elem()), index, true
		}
	}
	return vreg{}, nil, 0, false
}
func (f *lowerer) cellInstruction(ins ssa.Instruction) bool {
	switch ins := ins.(type) {
	case *ssa.Alloc:
		if r, _, _, ok := f.cellLocal(ins); ok {
			f.emit(evm.WithBC(evm.OpConst, r.slot, f.c.word(0)))
			f.emit(evm.ABC(evm.OpClearP, r.slot, 0, 0))
			return true
		}
	case *ssa.FieldAddr:
		_, _, _, ok := f.cellLocal(ins)
		return ok
	case *ssa.UnOp:
		if ins.Op != token.MUL {
			return false
		}
		if r, s, index, ok := f.cellLocal(ins.X); ok {
			if index < 0 {
				f.move(f.regs[ins][0], r)
			} else {
				f.extended(evm.ExtendedField, s, []vreg{r}, f.regs[ins], evm.VMFieldBase|uint64(index), evm.FieldValueCopy, nil)
			}
			return true
		}
	case *ssa.Store:
		if r, s, index, ok := f.cellLocal(ins.Addr); ok {
			if index < 0 {
				f.move(r, f.regs[ins.Val][0])
			} else {
				f.extended(evm.ExtendedFieldStore, s, []vreg{r, f.regs[ins.Val][0]}, []vreg{r}, evm.VMFieldBase|uint64(index), evm.FieldValueCopy, nil)
			}
			return true
		}
	}
	return false
}
