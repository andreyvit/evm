package evm

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"unsafe"

	"github.com/andreyvit/evm/values"
)

func TestEntryFunction(t *testing.T) {
	sig := Signature{Params: []*values.Type{values.Int64}, Results: []*values.Type{values.Int64}}
	p, err := Link(&Module{
		Functions: []Function{
			{Name: "Identity", Signature: sig, RCount: 1, Code: []Instruction{ABC(OpReturn, 0, 0, 0)}},
			{Name: "Unused", Code: []Instruction{ABC(OpReturn, 0, 0, 0)}},
		},
		FunctionRefs: []FunctionRef{{Index: 0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if unsafe.Sizeof(Callable{}) != 2*unsafe.Sizeof(uintptr(0)) {
		t.Fatal("callable must be two words")
	}
	if _, err := p.LookupFunction("Unused", &Signature{}); err != nil {
		t.Fatal(err)
	}
	f, err := p.LookupFunction("Identity", &sig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.LookupFunction("Identity", &Signature{Params: sig.Params}); !errors.Is(err, ErrFunction) || !strings.Contains(err.Error(), "Identity") {
		t.Fatalf("wrong result signature: %v", err)
	}
	if _, err := p.LookupFunction("missing", nil); !errors.Is(err, ErrFunction) {
		t.Fatal(err)
	}
	if v, err := f.Value(p, sig); err != nil || v.R != 1 {
		t.Fatal(v, err)
	}
	handle := &f
	value, err := handle.Value(p, sig)
	if err != nil {
		t.Fatal(err)
	}
	if value != (values.Value{R: 1}) {
		t.Fatalf("function value = %v", value)
	}
	inCallResult := make([]values.Value, 1)
	if err := New(p).RunNative(context.Background(), func(c *VM) error {
		return c.runValue(value, []values.Value{values.Int(17)}, inCallResult)
	}, Budget{}); err != nil {
		t.Fatal(err)
	}
	if inCallResult[0].R != 17 {
		t.Fatalf("in-call result = %v", inCallResult)
	}
	result := make([]values.Value, 1)
	if err := New(p).RunFunction(context.Background(), handle, []values.Value{values.Int(42)}, result); err != nil {
		t.Fatal(err)
	}
	if result[0].R != 42 {
		t.Fatalf("result = %v", result)
	}
}

// Link named guest functions that the guest never uses as function values.
func BenchmarkLinkCallables(b *testing.B) {
	const n = 256
	m := &Module{Functions: make([]Function, n)}
	for i := range m.Functions {
		m.Functions[i] = Function{Name: "Entry" + strconv.Itoa(i), Code: []Instruction{ABC(OpReturn, 0, 0, 0)}}
	}
	var p *Program
	b.ReportAllocs()
	for b.Loop() {
		var err error
		p, err = Link(m)
		if err != nil {
			b.Fatal(err)
		}
	}
	// These three backing arrays remain live with the linked program.
	storage := uintptr(cap(p.targets))*unsafe.Sizeof(callTarget{}) +
		uintptr(cap(p.entryNumbers))*unsafe.Sizeof(uint64(0)) +
		uintptr(cap(p.functionRefs))*unsafe.Sizeof(FunctionRef{})
	b.ReportMetric(float64(storage)/n, "B/entry")
	b.ReportMetric(float64(unsafe.Sizeof(Callable{})), "B/callable")
}

func TestRunClearsPointerRegistersOnHostPanic(t *testing.T) {
	p, err := Link(&Module{Functions: []Function{{Name: "Empty", Code: []Instruction{ABC(OpReturn, 0, 0, 0)}}}})
	if err != nil {
		t.Fatal(err)
	}
	typ, err := NewNativeFunctionType(Signature{Params: []*values.Type{values.StringType}})
	if err != nil {
		t.Fatal(err)
	}
	v, err := p.NativeFunction(typ, func(vm *VM) error {
		if vm.CallArgument(0).AsString() != "held" {
			t.Fatal("missing host argument")
		}
		panic("stop")
	})
	if err != nil {
		t.Fatal(err)
	}
	fn, err := p.Callable(v)
	if err != nil {
		t.Fatal(err)
	}
	vm := p.AcquireVM(t.Context())
	defer vm.Release()
	func() {
		defer func() {
			if recover() != "stop" {
				t.Fatal("expected host panic")
			}
		}()
		_ = vm.Run(fn, []values.Value{values.String("held")})
	}()
	registers := vm.p[:cap(vm.p)]
	if len(registers) == 0 {
		t.Fatal("expected pointer registers in the leased VM")
	}
	for i, ptr := range registers {
		if ptr != nil {
			t.Fatalf("pointer register %d still holds the argument", i)
		}
	}
	if vm.P != nil || vm.native != nil || vm.running {
		t.Fatal("host call still active after panic")
	}
}
