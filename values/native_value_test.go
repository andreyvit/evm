package values

import (
	"errors"
	"fmt"
	"hash/maphash"
	"math"
	"reflect"
	"testing"
)

func TestNativeValueEqualityAndHash(t *testing.T) {
	sentinel := errors.New("sentinel")
	var nilPointer *int
	values := []any{nil, nilPointer, new(int), new(int), sentinel, sentinel, errors.New("sentinel"), 42, 42, "text", float64(0), math.Copysign(0, -1), math.NaN(), nativeValueError(7), nativeValueError(7), nativeSliceError{1}, []int{1}, struct{ X any }{[]int{1}}}
	shape := &Shape{Type: NewType("native", Pointer), Kind: NativeValueKind, Cells: 1}
	for i, a := range values {
		for j, b := range values {
			t.Run(fmt.Sprintf("%d/%d", i, j), func(t *testing.T) {
				want, panicked := nativeComparison(a, b)
				x, y := NativeValue(a), NativeValue(b)
				got, err := shape.Equal(x, y, nil)
				var comparisonError *ComparisonError
				if panicked != errors.As(err, &comparisonError) || !panicked && (err != nil || got != want) {
					t.Fatalf("comparison: %v %v; want %v panic=%v", got, err, want, panicked)
				}
				if panicked || !want {
					return
				}
				hx, ex := shape.Hash(x, nil)
				hy, ey := shape.Hash(y, nil)
				if ex != nil || ey != nil || hx != hy {
					t.Fatalf("equal values have different hashes: %x %x %v %v", hx, hy, ex, ey)
				}
			})
		}
		x := NativeValue(a)
		wantComparable := a == nil || reflect.TypeOf(a).Comparable()
		if x.NativeComparable() != wantComparable {
			t.Fatalf("comparability differs for %T", a)
		}
		_, err := shape.Hash(x, nil)
		_, panicked := nativeComparison(a, a)
		if (err != nil) != panicked {
			t.Fatalf("hash comparability differs for %T: %v", a, err)
		}
	}
	table := NewTypeTable([]DynamicType{{}, {Shape: shape, Comparable: true}})
	iface := &Shape{Type: AnyType, Kind: InterfaceKind, Cells: 1}
	x, _ := table.Inline(1, NativeValue(sentinel))
	y, _ := table.Inline(1, NativeValue(sentinel))
	if equal, err := iface.Equal(x, y, table); err != nil || !equal {
		t.Fatalf("separate interface payloads changed native identity: %v %v", equal, err)
	}
	hx, ex := iface.Hash(x, table)
	hy, ey := iface.Hash(y, table)
	if ex != nil || ey != nil || hx != hy {
		t.Fatal("native interface key hashes differ")
	}
	if NativeValue(sentinel).AsNativeValue() != sentinel {
		t.Fatal("error identity lost")
	}
	if NativeValue(nilPointer).AsNativeValue() == nil {
		t.Fatal("typed nil lost")
	}
}

func nativeComparison(a, b any) (equal, panicked bool) {
	defer func() {
		if recover() != nil {
			panicked = true
		}
	}()
	return a == b, false
}

type nativeValueError int

func (e nativeValueError) Error() string { return fmt.Sprint(int(e)) }

type nativeSliceError []int

func (e nativeSliceError) Error() string { return fmt.Sprint([]int(e)) }

func TestNativeValueMapKeys(t *testing.T) {
	shape := &Shape{Type: NewType("native", Pointer), Kind: NativeValueKind, Cells: 1}
	m := NewMap(shape, nil)
	key := errors.New("key")
	for i := 0; i < 2; i++ {
		v := NativeValue(key)
		h, err := shape.Hash(v, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Put(v, Int(int64(i)), h); err != nil {
			t.Fatal(err)
		}
	}
	if m.Len() != 1 {
		t.Fatal("same native key stored twice")
	}
	for i := 0; i < 2; i++ {
		v := NativeValue(math.NaN())
		h, err := shape.Hash(v, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Put(v, Int(9), h); err != nil {
			t.Fatal(err)
		}
		if _, found, err := m.Find(v, h); err != nil || found {
			t.Fatal("NaN key matched", err)
		}
	}
	if m.Len() != 3 {
		t.Fatal("NaN keys were collapsed")
	}
	if NativeValue(nil) != (Value{}) {
		t.Fatal("nil value allocated a payload")
	}
}

func TestNativeComparisonPanicText(t *testing.T) {
	x := struct{ X any }{[]int{1}}
	v := NativeValue(x)
	_, err := nativeEqual(v, v)
	if err == nil || err.Error() != nativePanicText(func() { _ = x == x }) {
		t.Fatal("comparison panic changed", err)
	}
	_, err = nativeHash(v)
	if err == nil || err.Error() != nativePanicText(func() { _ = maphash.Comparable(nativeHashSeed, any(x)) }) {
		t.Fatal("hash panic changed", err)
	}
}

func nativePanicText(f func()) (text string) {
	defer func() {
		if p := recover(); p != nil {
			text = fmt.Sprint(p)
		}
	}()
	f()
	return
}
