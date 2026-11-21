package evmcompiler

import (
	"go/token"
	"go/types"
	"sort"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/values"
	"golang.org/x/tools/go/ssa"
)

// The header still tests the initial counter. At the end of an iteration,
// combine a signed increment and the next upper-bound test, then branch to the body.
func (f *lowerer) fuseLoopBackedge(latch, header *ssa.BasicBlock) bool {
	if len(header.Succs) != 2 || !header.Dominates(latch) || len(header.Instrs) < 2 {
		return false
	}
	// Bypassing the header is safe only if the comparison is its entire work
	// and no other instruction needs the boolean (including on the exit edge).
	for _, ins := range header.Instrs[:len(header.Instrs)-2] {
		if _, ok := ins.(*ssa.Phi); !ok && !f.invariant[ins] {
			return false
		}
	}
	test, ok := header.Instrs[len(header.Instrs)-2].(*ssa.BinOp)
	if !ok || (test.Op != token.LSS && test.Op != token.LEQ) || len(*test.Referrers()) != 1 || header.Instrs[len(header.Instrs)-1].(*ssa.If).Cond != test {
		return false
	}
	t, ok := numericType(test.X.Type())
	if !ok || t.Kind != evm.NumericSigned || t.Bits != 64 {
		return false
	}
	body, exit := header.Succs[0], header.Succs[1]
	entry, emitted := f.labels[body.Index]
	if !emitted || len(body.Instrs) == 0 || len(latch.Instrs) < 2 {
		return false
	}
	if _, bodyPhis := body.Instrs[0].(*ssa.Phi); bodyPhis {
		return false
	}
	// All edge assignments must be in place by the increment. Narrowing
	// conversions or pending phi copies leave a different final instruction.
	pc := len(f.code.Code) - 1
	if pc < f.labels[latch.Index] {
		return false
	}
	add, ok := latch.Instrs[len(latch.Instrs)-2].(*ssa.BinOp)
	if !ok || add.Op != token.ADD {
		return false
	}

	ins := f.code.Code[pc]
	counter := f.regs[test.X][0].slot
	delta := entry - pc - 1
	if ins.A() != counter || ins.B() != counter {
		return false
	}
	limit, step := f.regs[test.Y][0].slot, ins.C()
	if ins.Op() == evm.OpAddImmI64 && int16(ins.C()) == 1 && int(int16(delta)) == delta {
		op := evm.OpLoopLtI64
		if test.Op == token.LEQ {
			op = evm.OpLoopLeI64
		}
		f.code.Code[pc] = evm.ABC(op, counter, limit, uint16(delta))
	} else {
		if ins.Op() != evm.OpAddI64 || (counter|limit|step) > 0xfff || delta < -2048 || delta > 2047 {
			return false
		}
		op := evm.OpLoopAddLtI64
		if test.Op == token.LEQ {
			op = evm.OpLoopAddLeI64
		}
		f.code.Code[pc] = evm.ABCD12(op, counter, limit, step, uint16(delta)&0xfff)
	}
	f.unroll(header, body, latch, test, add, entry, pc)
	// The entry test and fused back edge evaluate the same source condition.
	if pc < len(f.code.Code) && (f.code.Code[pc].Op() == evm.OpLoopLtI64 || f.code.Code[pc].Op() == evm.OpLoopLeI64 || f.code.Code[pc].Op() == evm.OpLoopAddLtI64 || f.code.Code[pc].Op() == evm.OpLoopAddLeI64) {
		// Branch patches are in code order: start at the header's, where a
		// scan from the first patch would make many loops cost their square.
		first := 0
		if !f.unorderedBranches {
			first = sort.Search(len(f.branches), func(i int) bool { return f.branches[i].pc >= f.labels[header.Index] })
		}
		for _, patch := range f.branches[first:] {
			if patch.pc >= entry && !f.unorderedBranches {
				break
			}
			if patch.pc < f.labels[header.Index] || patch.pc >= entry {
				continue
			}
			op := f.code.Code[patch.pc].Op()
			if op != evm.OpJz && op != evm.OpJnz {
				continue
			}
			if f.c.module.SourceBranches == nil {
				f.c.module.SourceBranches = make(map[string]map[int]evm.SourceBranch)
			}
			branches := f.c.module.SourceBranches[f.code.Name]
			if branches == nil {
				branches = make(map[int]evm.SourceBranch)
				f.c.module.SourceBranches[f.code.Name] = branches
			}
			branches[patch.pc] = evm.SourceBranch{PC: patch.pc, Inverted: op == evm.OpJz}
			branches[pc] = evm.SourceBranch{PC: patch.pc}
			break
		}
	}
	if target := f.edge(header, exit); target != latch.Index+1 {
		f.jump(evm.OpJump, 0, target)
	}
	return true
}

// Replace eq.p + a branch on its sole result with a direct nil test. The
// caller has already selected the successors, including inversion for !=.
func (f *lowerer) jumpIf(i *ssa.If, branch evm.Opcode, target int) {
	slot := f.regs[i.Cond][0].slot
	pc := len(f.code.Code) - 1
	if value := nilTestOperand(i); f.optimizing() && value != nil && pc >= f.labels[i.Block().Index] {
		ins := f.code.Code[pc]
		if ins.Op() == evm.OpEqP && ins.A() == slot {
			op := evm.OpJNil
			if branch == evm.OpJz {
				op = evm.OpJNotNil
			}
			f.patch(pc, target)
			f.code.Code[pc] = evm.WithBC(op, f.regs[value][0].slot, 0)
			return
		}
	}
	f.jump(branch, slot, target)
}

func nilTestOperand(i *ssa.If) ssa.Value {
	cmp, ok := i.Cond.(*ssa.BinOp)
	block := i.Block().Instrs
	if !ok || len(block) < 2 || block[len(block)-2] != cmp || len(*cmp.Referrers()) != 1 {
		return nil
	}
	if x, ok := cmp.X.(*ssa.Const); ok && x.IsNil() {
		return cmp.Y
	}
	if y, ok := cmp.Y.(*ssa.Const); ok && y.IsNil() {
		return cmp.X
	}
	return nil
}

// A value read from a borrowed slice must not expose a writable address,
// including when instruction optimizations are disabled.
func (f *lowerer) readByteIndex(first, next ssa.Instruction) bool {
	addr, ok := first.(*ssa.IndexAddr)
	if !ok || !f.c.env.nativeViewLeaves[values.Uint8] || f.packed[addr] || !isByteSlice(addr.X.Type()) || len(*addr.Referrers()) != 1 {
		return false
	}
	load, ok := next.(*ssa.UnOp)
	if !ok || load.Op != token.MUL || load.X != addr {
		return false
	}
	f.extended(evm.ExtendedIndex, f.shape(addr.X.Type()), []vreg{f.regs[addr.X][0], f.regs[addr.Index][0]}, f.regs[load], 0, 0, nil)
	return true
}

// Keep the bounds check at the address expression. If another instruction
// intervenes or the pointer escapes, emit the general address and access.
func (f *lowerer) fuseSliceAccess(addr *ssa.IndexAddr, next ssa.Instruction) bool {
	if f.fuseRecordList(addr, next) {
		return true
	}
	if f.packed[addr] {
		return f.packedIndex(addr, next)
	}
	slice, ok := addr.X.Type().Underlying().(*types.Slice)
	if !ok || len(*addr.Referrers()) != 1 {
		return false
	}
	if s := f.shape(addr.X.Type()); s.Native != nil && s.Native.Slice != nil || f.c.env.mayViewNative(slice.Elem()) {
		return false
	}
	elem := f.shape(slice.Elem())
	if elem == nil || elem.Cells != 1 || elem.Type.Repr() != values.Raw {
		return false
	}
	base, index := f.regs[addr.X][0].slot, f.regs[addr.Index][0].slot
	switch use := next.(type) {
	case *ssa.Store:
		if use.Addr == addr {
			f.emit(evm.ABC(evm.OpStoreIndexR, base, index, f.regs[use.Val][0].slot))
			return true
		}
	case *ssa.UnOp:
		if use.Op == token.MUL && use.X == addr {
			f.emit(evm.ABC(evm.OpLoadIndexR, f.regs[use][0].slot, base, index))
			return true
		}
	}
	return false
}

func (f *lowerer) fuseAdjacent(first, next ssa.Instruction) bool {
	switch x := first.(type) {
	case *ssa.FieldAddr:
		if _, _, _, ok := f.cellLocal(x); ok {
			return false
		}
		if len(*x.Referrers()) != 1 {
			return false
		}
		s := f.shape(x.X.Type().Underlying().(*types.Pointer).Elem())
		if s.Native == nil {
			return false
		}
		in := []vreg{f.regs[x.X][0]}
		switch use := next.(type) {
		case *ssa.UnOp:
			if use.Op == token.MUL && use.X == x {
				f.extended(evm.ExtendedField, s, in, f.regs[use], evm.VMFieldBase|uint64(x.Field), 0, nil)
				return true
			}
		case *ssa.Store:
			if use.Addr == x {
				f.extended(evm.ExtendedFieldStore, s, append(in, f.regs[use.Val][0]), nil, evm.VMFieldBase|uint64(x.Field), 0, nil)
				return true
			}
		}
	case *ssa.IndexAddr:
		return f.fuseSliceAccess(x, next)
	case *ssa.BinOp:
		y, ok := next.(*ssa.BinOp)
		if !ok || y.Op != token.ADD || len(*x.Referrers()) != 1 {
			return false
		}
		t, ok := numericType(x.Type())
		if !ok || t.Kind != evm.NumericFloat || t.Bits != 64 {
			return false
		}
		var op evm.Opcode
		b, c, d := x.X, x.Y, y.Y
		switch {
		case x.Op == token.MUL && y.X == x:
			op = evm.OpMulAddF64
		case x.Op == token.MUL && y.Y == x:
			op, b, c, d = evm.OpAddMulF64, y.X, x.X, x.Y
		case x.Op == token.SUB && y.X == x:
			op = evm.OpSubAddF64
		default:
			return false
		}
		dst, src1, src2, src3 := f.regs[y][0].slot, f.regs[b][0].slot, f.regs[c][0].slot, f.regs[d][0].slot
		if dst|src1|src2|src3 > 0xfff {
			return false
		}
		f.emit(evm.ABCD12(op, dst, src1, src2, src3))
		return true
	}
	return false
}

// Let the final arithmetic instruction write straight into the outgoing window.
// All argument expressions have run and no argument moves have been emitted yet.
func (f *lowerer) forwardWordArgument(call *ssa.Call, arg ssa.Value, dst uint16) bool {
	bin, ok := arg.(*ssa.BinOp)
	if !ok || bin.Block() != call.Block() || len(*bin.Referrers()) != 1 {
		return false
	}
	pc := len(f.code.Code) - 1
	if pc < f.labels[call.Block().Index] {
		return false
	}
	ins := f.code.Code[pc]
	if ins.A() != f.regs[bin][0].slot {
		return false
	}
	switch ins.Op() {
	case evm.OpAddImmI64, evm.OpAddI64, evm.OpSubI64, evm.OpMulI64,
		evm.OpAddF64, evm.OpSubF64, evm.OpMulF64, evm.OpDivF64:
		f.code.Code[pc] = evm.ABC(ins.Op(), dst, ins.B(), ins.C())
		f.outgoing = append(f.outgoing, outgoingPatch{pc: pc})
		return true
	}
	return false
}
