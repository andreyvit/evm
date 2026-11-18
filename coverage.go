package evm

import (
	"cmp"
	"fmt"
	"slices"
	"unsafe"

	"github.com/andreyvit/evm/values"
)

//go:generate go run ./internal/coveragegen

// Coverage collects one linked program's instruction and branch counts.
// Use a separate collector per concurrent execution and Merge after completion.
type Coverage struct {
	program *Program
	counts  [][]InstructionCoverage
}

type InstructionCoverage struct {
	Function        string
	PC              int
	Position        SourcePosition
	Count           uint64
	Taken, NotTaken uint64
	Branch          bool
}

func NewCoverage(p *Program) *Coverage {
	c := &Coverage{program: p, counts: make([][]InstructionCoverage, len(p.functions))}
	for i, f := range p.functions {
		c.counts[i] = make([]InstructionCoverage, len(f.Code))
		for pc, ins := range f.Code {
			if ins.Op() == OpExtended && ins.A() == floatLoopMarker {
				ins = p.floatLoops[ins.BC()].branch
			}
			c.counts[i][pc] = InstructionCoverage{Function: f.Name, PC: pc, Position: f.position(pc), Branch: coverageBranch(ins.Op())}
		}
	}
	return c
}

// SetCoverage changes instrumentation between runs. Nil disables collection.
func (vm *VM) SetCoverage(c *Coverage) error {
	if vm.running {
		return ErrReentry
	}
	if c != nil && c.program != vm.program {
		return fmt.Errorf("coverage belongs to another program")
	}
	vm.coverage = c
	return nil
}

// Instructions returns a snapshot, including instructions never executed.
func (c *Coverage) Instructions() []InstructionCoverage {
	var result []InstructionCoverage
	for _, f := range c.counts {
		result = append(result, f...)
	}
	return result
}

func (c *Coverage) Merge(other *Coverage) error {
	if c.program != other.program {
		return fmt.Errorf("coverage belongs to another program")
	}
	for i, f := range other.counts {
		for pc, count := range f {
			dst := &c.counts[i][pc]
			dst.Count += count.Count
			dst.Taken += count.Taken
			dst.NotTaken += count.NotTaken
		}
	}
	return nil
}

func (c *Coverage) instruction(fn, pc int) Instruction {
	c.counts[fn][pc].Count++
	ins := c.program.functions[fn].Code[pc]
	if ins.Op() == OpExtended && ins.A() == floatLoopMarker {
		return c.program.floatLoops[ins.BC()].branch
	}
	return ins
}
func (c *Coverage) branch(fn, pc int, taken bool) bool {
	if taken {
		c.counts[fn][pc].Taken++
	} else {
		c.counts[fn][pc].NotTaken++
	}
	return taken
}
func (vm *VM) executeSelected(idx int, env unsafe.Pointer, args, results []values.Value, budget *executionBudget) error {
	if vm.coverage != nil {
		return vm.executeCovered(idx, env, args, results, budget)
	}
	return vm.execute(idx, env, args, results, budget)
}

// LineCoverage summarizes executable instructions at an original source line.
// A partially covered line was entered but contains unexecuted instructions or
// a conditional jump whose outcomes were not both observed.
type LineCoverage struct {
	File                                       string
	Line                                       int
	Instructions, Executed, Branches, Outcomes int
}

func (l LineCoverage) Covered() bool {
	return l.Instructions == l.Executed && l.Outcomes == 2*l.Branches
}
func (c *Coverage) Lines() []LineCoverage {
	type key struct {
		file string
		line int
	}
	lines := map[key]*LineCoverage{}
	for _, f := range c.counts {
		type branch struct {
			line     key
			outcomes uint8
		}
		branches := map[int]branch{}
		for _, ins := range f {
			if ins.Position.Line <= 0 {
				continue
			}
			k := key{ins.Position.File, ins.Position.Line}
			l := lines[k]
			if l == nil {
				l = &LineCoverage{File: k.file, Line: k.line}
				lines[k] = l
			}
			l.Instructions++
			if ins.Count > 0 {
				l.Executed++
			}
			if ins.Branch {
				source, ok := c.program.sourceBranches[ins.Function][ins.PC]
				if !ok {
					source.PC = ins.PC
				}
				pos := f[source.PC].Position
				if pos.Line <= 0 {
					continue
				}
				item := branches[source.PC]
				item.line = key{pos.File, pos.Line}
				taken, notTaken := ins.Taken, ins.NotTaken
				if source.Inverted {
					taken, notTaken = notTaken, taken
				}
				if taken > 0 {
					item.outcomes |= 1
				}
				if notTaken > 0 {
					item.outcomes |= 2
				}
				branches[source.PC] = item
			}
		}
		for _, branch := range branches {
			line := lines[branch.line]
			line.Branches++
			if branch.outcomes&1 != 0 {
				line.Outcomes++
			}
			if branch.outcomes&2 != 0 {
				line.Outcomes++
			}
		}
	}
	result := make([]LineCoverage, 0, len(lines))
	for _, l := range lines {
		result = append(result, *l)
	}
	slices.SortFunc(result, func(a, b LineCoverage) int {
		if n := cmp.Compare(a.File, b.File); n != 0 {
			return n
		}
		return cmp.Compare(a.Line, b.Line)
	})
	return result
}
func coverageBranch(op Opcode) bool {
	switch op {
	case OpJz, OpJnz, OpJNil, OpJNotNil, OpLoopLeI64, OpLoopLtI64, OpLoopAddLtI64, OpLoopAddLeI64:
		return true
	default:
		return false
	}
}

// Coverage returns the enclosing invocation collector, or nil when disabled.
func (c *Call) Coverage() *Coverage { return c.vm.coverage }
