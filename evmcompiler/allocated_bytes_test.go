package evmcompiler_test

import (
	"context"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
)

func TestVMReportsAllocatedBytesDuringAndAfterRun(t *testing.T) {
	var vm *evmtest.Session
	var during uint64
	host := evm.Package{Path: "host", Functions: []evm.ExternalFunction{
		{Name: "Probe", Call: func(*evm.VM) error { during = vm.AllocatedBytes(); return nil }},
	}}
	module, err := evmcompiler.Compile("main.go", `import "host"
func Run() int { s := make([]int, 1000); host.Probe(); t := make([]int, 1000); return len(s) + len(t) }`, host)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, host)
	if err != nil {
		t.Fatal(err)
	}
	vm = evmtest.New(program)
	if got := vm.AllocatedBytes(); got != 0 {
		t.Fatalf("new VM reports %d bytes", got)
	}
	if err := vm.Run(context.Background(), "Run", nil, make([]values.Value, 1)); err != nil {
		t.Fatal(err)
	}
	after := vm.AllocatedBytes()
	if during < 8000 || after < during+8000 {
		t.Fatalf("during %d, after %d; each slice takes at least 8000 bytes", during, after)
	}
	if err := vm.Reset(); err != nil {
		t.Fatal(err)
	}
	if got := vm.AllocatedBytes(); got != 0 {
		t.Fatalf("reset VM reports %d bytes", got)
	}
}
