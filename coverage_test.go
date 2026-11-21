package evm

import (
	"context"
	"math"
	"os/exec"
	"reflect"
	"testing"

	"github.com/andreyvit/evm/values"
)

func TestCoverageBranchesMergeAndDisabledExecution(t *testing.T) {
	m := &Module{Functions: []Function{{Name: "choose", Signature: Signature{Params: []*values.Type{values.Int64}, Results: []*values.Type{values.Int64}}, RCount: 1, Lines: []int{2, 3, 4, 5}, Code: []Instruction{
		WithBC(OpJz, 0, 2), ABC(OpAddI64, 0, 0, 0), ABC(OpReturn, 0, 0, 0), ABC(OpReturn, 0, 0, 0),
	}}}}
	m.SourceBranches = map[string]map[int]SourceBranch{"choose": {0: {PC: 99}}}
	if _, err := Link(m); err == nil {
		t.Fatal("accepted invalid source branch")
	}
	m.SourceBranches["choose"][0] = SourceBranch{PC: 0}
	m.SourceBranches["missing"] = nil
	if _, err := Link(m); err == nil {
		t.Fatal("accepted source branches for an unknown function")
	}
	delete(m.SourceBranches, "missing")
	vm := linked(t, m)
	m.SourceBranches["choose"][0] = SourceBranch{PC: 99}
	c := NewCoverage(vm.program)
	if err := vm.SetCoverage(c); err != nil {
		t.Fatal(err)
	}
	out := make([]values.Value, 1)
	for _, n := range []int64{0, 7} {
		if err := vm.Run(context.Background(), "choose", []values.Value{values.Int(n)}, out); err != nil {
			t.Fatal(err)
		}
		if got := int64(out[0].R); got != 2*n {
			t.Fatalf("result %d", got)
		}
	}
	for _, line := range c.Lines() {
		if !line.Covered() {
			t.Fatalf("incomplete source coverage: %+v", line)
		}
	}
	got := c.Instructions()
	if got[0].Count != 2 || got[0].Taken != 1 || got[0].NotTaken != 1 || got[1].Count != 1 || got[3].Count != 1 {
		t.Fatalf("counts: %+v", got)
	}
	if err := vm.SetCoverage(nil); err != nil {
		t.Fatal(err)
	}
	if err := vm.Run(context.Background(), "choose", []values.Value{values.Int(9)}, out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, c.Instructions()) {
		t.Fatal("disabled execution collected coverage")
	}
	merged := NewCoverage(vm.program)
	if err := merged.Merge(c); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, merged.Instructions()) {
		t.Fatal("merge changed counts")
	}
	m.SourceBranches = nil
	other := linked(t, m)
	if err := other.SetCoverage(c); err == nil {
		t.Fatal("accepted another program")
	}
	if err := c.Merge(NewCoverage(other.program)); err == nil {
		t.Fatal("merged another program")
	}
}

func TestCoverageInterpreterCurrent(t *testing.T) {
	if out, err := exec.Command("go", "run", "./internal/coveragegen", "-check").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestCoverageCountsOptimizedFloatLoop(t *testing.T) {
	m := &Module{Functions: []Function{{Name: "run", RCount: 5, Code: []Instruction{
		WithBC(OpConst, 0, 0), WithBC(OpConst, 1, 1), WithBC(OpConst, 2, 2),
		ABC(OpAddF64, 3, 3, 2), ABC(OpMulF64, 4, 3, 2), ABC(OpLoopLtI64, 0, 1, 0xfffd), ABC(OpReturn, 0, 0, 0),
	}}}, Words: []uint64{0, 8, math.Float64bits(1.5)}}
	fast, _ := referenceFloatProgram(t, m)
	vm := New(fast)
	c := NewCoverage(fast)
	if err := vm.SetCoverage(c); err != nil {
		t.Fatal(err)
	}
	if err := vm.Run(context.Background(), "run", nil, nil); err != nil {
		t.Fatal(err)
	}
	counts := c.Instructions()
	if counts[3].Count != 8 || counts[4].Count != 8 || counts[5].Count != 8 || counts[5].Taken != 7 || counts[5].NotTaken != 1 {
		t.Fatalf("counts: %+v", counts)
	}
}

func TestCoverageNativeCallback(t *testing.T) {
	vm := linked(t, &Module{Functions: []Function{{Name: "callback", Code: []Instruction{ABC(OpReturn, 0, 0, 0)}}}, FunctionRefs: []FunctionRef{{Index: 0}}})
	c := NewCoverage(vm.program)
	if err := vm.SetCoverage(c); err != nil {
		t.Fatal(err)
	}
	if err := vm.RunNative(context.Background(), func(call *VM) error { return call.runValue(values.Value{R: 1}, nil, nil) }, Budget{}); err != nil {
		t.Fatal(err)
	}
	if got := c.Instructions()[0].Count; got != 1 {
		t.Fatalf("callback executions = %d", got)
	}
}

func TestCoverageFusedComparisonKeepsBothJumpOutcomes(t *testing.T) {
	vm := linked(t, &Module{Functions: []Function{{Name: "less", RCount: 3, Signature: Signature{Params: []*values.Type{values.Int64, values.Int64}}, Code: []Instruction{
		ABC(OpLtI64, 2, 0, 1), WithBC(OpJz, 2, 1), ABC(OpReturn, 0, 0, 0), ABC(OpReturn, 0, 0, 0),
	}}}})
	if vm.program.functions[0].Code[0].Op() != opBranchLtI64 {
		t.Fatal("comparison was not fused")
	}
	coverage := NewCoverage(vm.program)
	if err := vm.SetCoverage(coverage); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int64{1, 3} {
		if err := vm.Run(context.Background(), "less", []values.Value{values.Int(n), values.Int(2)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	counts := coverage.Instructions()
	if counts[0].Count != 2 || counts[1].Count != 2 || counts[1].Taken != 1 || counts[1].NotTaken != 1 {
		t.Fatalf("fused comparison: %+v", counts)
	}
}
