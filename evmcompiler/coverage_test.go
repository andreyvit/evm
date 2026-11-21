package evmcompiler_test

import (
	"context"
	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
	"testing"
)

func TestCoverageUnrolledLoopHasNoImpossibleEntryOutcome(t *testing.T) {
	m, err := evmcompiler.Compile("loop.go", `package example
func Run(x int) int {
 for i := 0; i < 3; i++ {
  x += i
 }
 return x
}`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m)
	if err != nil {
		t.Fatal(err)
	}
	c := evm.NewCoverage(p)
	vm := evmtest.New(p)
	vm.SetCoverage(c)
	var out [1]values.Value
	if err := vm.Run(context.Background(), "Run", []values.Value{values.Int(10)}, out[:]); err != nil {
		t.Fatal(err)
	}
	if out[0].AsInt() != 13 {
		t.Fatal(out[0])
	}
	for _, line := range c.Lines() {
		if line.File == "loop.go" && !line.Covered() {
			t.Errorf("incomplete: %+v", line)
		}
	}
	rawBranch := false
	for _, ins := range c.Instructions() {
		rawBranch = rawBranch || ins.Branch
	}
	if !rawBranch {
		t.Fatal("test must retain the constant entry guard in raw coverage")
	}
}

func TestCoverageBooleanUsesMapToConditions(t *testing.T) {
	for _, raw := range []bool{false, true} {
		m, err := (evmcompiler.Options{NoOptimization: raw}).Compile("conditions.go", `package example
func Run(flag bool) int {
 total := 0
 if flag {
  total++
 }
 if flag {
  total += 2
 }
 return total
}`)
		if err != nil {
			t.Fatal(err)
		}
		p, err := evm.Link(m)
		if err != nil {
			t.Fatal(err)
		}
		c := evm.NewCoverage(p)
		vm := evmtest.New(p)
		vm.SetCoverage(c)
		for _, flag := range []bool{false, true} {
			var out [1]values.Value
			if err := vm.Run(context.Background(), "Run", []values.Value{values.Bool(flag)}, out[:]); err != nil {
				t.Fatal(err)
			}
		}
		branches := map[int]int{}
		for _, line := range c.Lines() {
			if line.File == "conditions.go" {
				branches[line.Line] += line.Branches
				if !line.Covered() {
					t.Errorf("raw=%v: incomplete %+v", raw, line)
				}
			}
		}
		if branches[2] != 0 || branches[4] != 1 || branches[7] != 1 {
			t.Fatalf("raw=%v: branches mapped to %v", raw, branches)
		}
	}
}
