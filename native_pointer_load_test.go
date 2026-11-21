package evm

import (
	"context"
	"reflect"
	"testing"
	"unsafe"

	"github.com/andreyvit/evm/nativebind"
	"github.com/andreyvit/evm/values"
)

func TestLinkedNativePointerLoad(t *testing.T) {
	for _, mode := range []string{"plain", "unoptimized", "layout-codec", "program-codec"} {
		t.Run(mode, func(t *testing.T) {
			scalar := &values.Shape{Type: values.IntType, Kind: values.IntKind, Bits: 64, Cells: 1}
			ptr := &values.Shape{Type: values.NewType("*int", values.Pair), Kind: values.PointerKind, Elem: scalar, Cells: 1, Native: nativebind.Layout(reflect.TypeFor[*int](), nil, false)}
			decoded := false
			decode := func(v values.Value) (values.Value, error) {
				decoded = true
				return values.Value{R: values.NativeAddress, P: v.P}, nil
			}
			identity := func(v values.Value, _ func(uint64) error) (values.Value, error) { return v, nil }
			var packages []Package
			if mode == "layout-codec" {
				ptr.Native.Codec = &values.NativeValueCodec{Decode: decode, Encode: identity}
			}
			if mode == "program-codec" {
				packages = []Package{{Path: "native", Types: []TypeDecl{{Shape: ptr, NativeCodec: &NativeValueCodec{Decode: func(_ *VM, v values.Value) (values.Value, error) { return decode(v) }, Encode: func(_ *VM, v values.Value) (values.Value, error) { return v, nil }}}}}}
			}
			module := &Module{NoOptimization: mode == "unoptimized", Shapes: []*values.Shape{ptr}, ExtendedOps: []ExtendedOp{{Kind: ExtendedAddressLoad, Shape: ptr, In: []Operand{{Slot: 0, Repr: values.Pair}}, Out: []Operand{{Slot: 0, Repr: values.Pair}}}}, Functions: []Function{{Name: "read", Signature: Signature{Params: []*values.Type{ptr.Type}, Results: []*values.Type{ptr.Type}}, RCount: 1, PCount: 1, Code: []Instruction{WithBC(OpExtended, 0, 0), ABC(OpReturn, 0, 0, 0)}}}}
			program, err := Link(module, packages...)
			if err != nil {
				t.Fatal(err)
			}
			if got := program.functions[0].Code[0].Op() == opNativePointerLoad; got != (mode == "plain") {
				t.Fatalf("rewrite=%v", got)
			}
			if module.Functions[0].Code[0].Op() != OpExtended {
				t.Fatal("link changed module")
			}
			target := 42
			slot := &target
			var out [1]values.Value
			if err := New(program).Run(context.Background(), "read", []values.Value{{R: values.NativeAddress, P: unsafe.Pointer(&slot)}}, out[:]); err != nil {
				t.Fatal(err)
			}
			if out[0].P != unsafe.Pointer(&target) || out[0].R != values.NativeAddress {
				t.Fatalf("loaded %v", out[0])
			}
			if decoded != (mode == "layout-codec" || mode == "program-codec") {
				t.Fatalf("codec called=%v", decoded)
			}
		})
	}
}
