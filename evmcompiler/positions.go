package evmcompiler

import (
	"go/token"
	"slices"

	"golang.org/x/tools/go/ssa"
)

// A parameter's SSA position is its declaration, not a later `if parameter`.
// Capture use positions from debug references, then remove every debug reference
// before optimization. Keeping them would change sole-use and escape analysis.
func (c *typedCompiler) captureBranchPositions(functions []*ssa.Function) {
	c.branchPositions = make(map[*ssa.If]token.Pos)
	for _, fn := range functions {
		cleaned := make(map[ssa.Value]bool)
		for _, block := range fn.Blocks {
			positions := make(map[ssa.Value]token.Pos)
			instructions := block.Instrs[:0]
			for _, instruction := range block.Instrs {
				if ref, ok := instruction.(*ssa.DebugRef); ok {
					if !ref.IsAddr {
						positions[ref.X] = ref.Expr.Pos()
					}
					if !cleaned[ref.X] {
						cleaned[ref.X] = true
						if refs := ref.X.Referrers(); refs != nil {
							*refs = slices.DeleteFunc(*refs, func(ins ssa.Instruction) bool { _, debug := ins.(*ssa.DebugRef); return debug })
						}
					}
					continue
				}
				if branch, ok := instruction.(*ssa.If); ok {
					if pos := positions[branch.Cond]; pos.IsValid() {
						c.branchPositions[branch] = pos
					}
				}
				instructions = append(instructions, instruction)
			}
			clear(block.Instrs[len(instructions):])
			block.Instrs = instructions
		}
	}
}
