package evmstd

import (
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
)

func TestReplaceChargesOutputBeforeReturningToBinding(t *testing.T) {
	m, err := evmcompiler.Compile("budget.go", "func Run() {}")
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m)
	if err != nil {
		t.Fatal(err)
	}
	test := func(t *testing.T, limit int) {
		vm := p.AcquireVM(t.Context())
		defer vm.Release()
		vm.SetBudget(evm.Limits{AllocationBytes: 3})
		defer func() {
			if got := recover(); got != evm.ErrAllocation {
				t.Fatalf("want allocation limit, got %v", got)
			}
		}()
		byteGuards{}.Replace(vm, []byte("data"), []byte("a"), []byte("ab"), limit)
	}
	t.Run("copy", func(t *testing.T) { test(t, 0) })
	t.Run("one replacement", func(t *testing.T) { test(t, 1) })
}
