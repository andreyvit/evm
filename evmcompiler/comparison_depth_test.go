package evmcompiler_test

import (
	"context"
	"errors"
	"testing"

	"github.com/andreyvit/evm/values"
)

func TestGoDeeplyNestedInterfaceValues(t *testing.T) {
	vm := compileGoVM(t, `type Node struct{ next any }
func chain(depth int) any { var x any; for i := 0; i < depth; i++ { x = Node{next: x} }; return x }
func Equal(depth int) bool { x := chain(depth); return x == x }
func Key(depth int) int { m := map[any]int{}; m[chain(depth)] = 1; return len(m) }`)
	out := make([]values.Value, 1)
	if err := vm.Run(context.Background(), "Equal", []values.Value{values.Int(1000)}, out); err != nil || !out[0].AsBool() {
		t.Fatalf("shallow comparison: %v %v", out, err)
	}
	if err := vm.Run(context.Background(), "Key", []values.Value{values.Int(1000)}, out); err != nil || out[0].AsInt() != 1 {
		t.Fatalf("shallow key: %v %v", out, err)
	}
	for _, entry := range []string{"Equal", "Key"} {
		err := vm.Run(context.Background(), entry, []values.Value{values.Int(20_000)}, out)
		if !errors.Is(err, values.ErrComparisonDepth) {
			t.Fatalf("%s: got %v, want %v", entry, err, values.ErrComparisonDepth)
		}
		if err := vm.Reset(); err != nil {
			t.Fatal(err)
		}
	}
}
