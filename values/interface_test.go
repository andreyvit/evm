package values

import (
	"testing"
	"unsafe"
)

func TestInterfaceHeaderBounds(t *testing.T) {
	text := &Shape{Type: StringType, Kind: StringKind, Cells: 1}
	namedText := &Shape{Type: NewType("Text", Pair), Kind: StringKind, Cells: 1}
	array := &Shape{Type: NewType("[]any", Pair), Kind: SliceKind, Cells: 1, Elem: &Shape{Type: AnyType, Kind: InterfaceKind, Cells: 1}}
	bytes := &Shape{Type: NewType("[]byte", Pair), Kind: SliceKind, Cells: 1, Elem: &Shape{Type: Uint8, Kind: UintKind, Bits: 8, Cells: 1}}
	ts := NewTypeTable([]DynamicType{{}, {Shape: text, Comparable: true}, {Shape: namedText, Comparable: true}, {Shape: array}, {Shape: bytes}})
	// Only headers are inspected; no multi-gigabyte backing allocation is needed.
	for _, tc := range []struct {
		name    string
		id      uint64
		payload Value
		inline  bool
	}{
		{"string60", 1, Uint(1<<60 - 1), true},
		{"string fallback", 1, Uint(1 << 60), false},
		{"defined string44", 2, Uint(1<<44 - 1), true},
		{"defined string fallback", 2, Uint(1 << 44), false},
		{"any slice spare30", 3, SliceValue(nil, 1, 1<<30), true},
		{"any slice fallback", 3, SliceValue(nil, 1, 1<<30+1), false},
		{"byte slice length32", 4, SliceValue(nil, 1<<32-1, 1<<32-1), true},
		{"byte slice spare30", 4, SliceValue(nil, 1, 1<<30), true},
		{"byte slice fallback", 4, SliceValue(nil, 1, 1<<30+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, inline := ts.Inline(tc.id, tc.payload)
			if inline != tc.inline {
				t.Fatalf("inline=%v, want %v", inline, tc.inline)
			}
			if !inline {
				v = Value{R: tc.id, P: unsafe.Pointer(&tc.payload)}
			}
			shape, got, err := ts.Unpack(v)
			if err != nil || got != tc.payload || shape != ts.types[tc.id].Shape || ts.Index(v) != tc.id {
				t.Fatalf("round trip: %v %v %v", shape, got, err)
			}
		})
	}
	if _, _, err := ts.Unpack(Value{R: interfaceAnySlice | 1<<32 | (1<<32 - 1)}); err == nil {
		t.Fatal("accepted overflowing slice capacity")
	}

	types := make([]DynamicType, 1<<16+1)
	for i := 1; i < len(types); i++ {
		types[i] = DynamicType{Shape: namedText, Comparable: true}
	}
	wide := NewTypeTable(types)
	for _, id := range []uint64{1<<16 - 1, 1 << 16} {
		payload := String("value")
		v, inline := wide.Inline(id, payload)
		if inline != (id < 1<<16) {
			t.Fatalf("type %d: inline=%v", id, inline)
		}
		if !inline {
			v = Value{R: id, P: unsafe.Pointer(&payload)}
		}
		_, got, err := wide.Unpack(v)
		if err != nil || got != payload || wide.Index(v) != id {
			t.Fatalf("wide type %d: %v %v", id, got, err)
		}
	}
}

func TestNativePointerInterface(t *testing.T) {
	pointer := &Shape{Type: NewType("*Native", Pair), Kind: PointerKind, Cells: 1}
	other := &Shape{Type: NewType("*Other", Pair), Kind: PointerKind, Cells: 1}
	text := &Shape{Type: StringType, Kind: StringKind, Cells: 1}
	ts := NewTypeTable([]DynamicType{{}, {Shape: pointer, Comparable: true}, {Shape: other, Comparable: true}, {Shape: text, Comparable: true}})
	iface := &Shape{Type: AnyType, Kind: InterfaceKind, Cells: 1}
	object := new(int)
	*object = 42
	for _, p := range []unsafe.Pointer{unsafe.Pointer(object), nil} {
		original := Value{R: NativeAddress, P: p}
		encoded, ok := ts.Inline(1, original)
		if !ok {
			t.Fatal("native pointer requires boxing")
		}
		shape, decoded, err := ts.Unpack(encoded)
		if err != nil || shape != pointer || decoded != original || ts.Index(encoded) != 1 {
			t.Fatal("native pointer round trip", decoded, err)
		}
		boxed := Value{R: 1, P: unsafe.Pointer(&original)}
		if equal, err := iface.Equal(encoded, boxed, ts); err != nil || !equal {
			t.Fatal("boxed equality", equal, err)
		}
		a, err := iface.Hash(encoded, ts)
		if err != nil {
			t.Fatal(err)
		}
		b, err := iface.Hash(boxed, ts)
		if err != nil || a != b {
			t.Fatal("boxed hash", a, b, err)
		}
		second, ok := ts.Inline(2, original)
		if !ok {
			t.Fatal("second type requires boxing")
		}
		if equal, err := iface.Equal(encoded, second, ts); err != nil || equal {
			t.Fatal("different types compare equal", err)
		}
		if equal, err := iface.Equal(encoded, Value{}, ts); err != nil || equal {
			t.Fatal("typed pointer equals nil interface", err)
		}
		if p == nil {
			nilPayload := Value{}
			boxedNil := Value{R: 1, P: unsafe.Pointer(&nilPayload)}
			if equal, err := iface.Equal(encoded, boxedNil, ts); err != nil || !equal {
				t.Fatal("typed nil equality", err)
			}
			h, err := iface.Hash(boxedNil, ts)
			if err != nil || h != a {
				t.Fatal("typed nil hash", err)
			}
		}
	}
	for _, flags := range []uint64{0, 2} {
		if _, ok := ts.Inline(1, Value{R: flags, P: unsafe.Pointer(object)}); ok {
			t.Fatal("unsupported pointer representation inlined")
		}
	}
	for _, id := range []uint64{0, 3, 99} {
		if _, _, err := ts.Unpack(Value{R: uint64(2)<<60 | id, P: unsafe.Pointer(object)}); err == nil {
			t.Fatal("invalid native pointer header accepted", id)
		}
	}
	if n := testing.AllocsPerRun(1000, func() {
		v, ok := ts.Inline(1, Value{R: NativeAddress, P: unsafe.Pointer(object)})
		if !ok {
			t.Fatal("boxing")
		}
		if _, _, err := ts.Unpack(v); err != nil {
			t.Fatal(err)
		}
	}); n != 0 {
		t.Fatalf("native pointer interface allocates: %g", n)
	}
}

func TestNativeSliceViewInterface(t *testing.T) {
	shape := &Shape{Type: NewType("[]row", Pair), Kind: SliceKind, Cells: 1, Elem: &Shape{Type: NewType("row", Pair), Kind: TypedPairKind, Cells: 1}}
	types := NewTypeTable([]DynamicType{{}, {Shape: shape}})
	backing := new(int)
	for _, size := range []uint32{0, 1, 100, ^uint32(0)} {
		view := &NativeSliceView{Data: unsafe.Pointer(backing), Length: size, Capacity: ^uint32(0)}
		v := NativeSliceValue(view)
		if v.NativeSlice() != view || v.SliceLen() != size || v.SliceCap() != ^uint32(0) {
			t.Fatal("native slice header lost metadata")
		}
		if _, inline := types.Inline(1, v); inline {
			t.Fatal("compact interface encoding discarded native view marker")
		}
		boxed := Value{R: 1, P: unsafe.Pointer(&v)}
		gotShape, got, err := types.Unpack(boxed)
		if err != nil || gotShape != shape || got != v || got.NativeSlice() != view {
			t.Fatalf("native slice round trip: %v %v", got, err)
		}
	}
	for _, size := range []uint32{0, 1, ^uint32(0)} {
		v := SliceValue(unsafe.Pointer(backing), size, size)
		if v.NativeSlice() != nil || v.SliceLen() != size || v.SliceCap() != size {
			t.Fatal("ordinary slice header changed")
		}
	}
}
