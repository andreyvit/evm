package evm

import (
	"context"
	"testing"

	"github.com/andreyvit/evm/values"
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
	vm := New(p)
	vm.Limits.AllocationBytes, vm.Limits.Allocations = ^uint64(0), ^uint64(0)
	callback := func(call *Call) error { return call.RunCallback(values.Value{R: 1}) }
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := vm.RunNative(ctx, callback, Budget{}); err != nil {
			b.Fatal(err)
		}
	}
}
