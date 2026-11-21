package evm

import (
	"testing"
)

// Keep the guest body empty to expose native-to-guest invocation overhead.
func BenchmarkNativeGuestCallback(b *testing.B) {
	p, err := Link(&Module{
		Functions:    []Function{{Name: "callback", Code: []Instruction{ABC(OpReturn, 0, 0, 0)}}},
		FunctionRefs: []FunctionRef{{Index: 0}},
	})
	if err != nil {
		b.Fatal(err)
	}
	guest, err := p.LookupFunction("callback", nil)
	if err != nil {
		b.Fatal(err)
	}
	v, err := p.NativeFunction(nativeEntryType, func(vm *VM) error { return vm.Run(guest, nil) })
	if err != nil {
		b.Fatal(err)
	}
	host, err := p.Callable(v)
	if err != nil {
		b.Fatal(err)
	}
	vm := p.AcquireVM(b.Context())
	defer vm.Release()
	vm.SetBudget(Limits{})
	b.ReportAllocs()
	for b.Loop() {
		if err := vm.Run(host, nil); err != nil {
			b.Fatal(err)
		}
	}
}
