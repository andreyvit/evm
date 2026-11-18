package nativebind

import (
	"math"
	"reflect"
	"testing"
	"unsafe"

	"github.com/andreyvit/evm/values"
)

type opaqueBefore struct {
	Hidden  any
	Visible int
}
type opaqueAfter struct {
	Visible int
	Hidden  any
}
type opaqueScalars struct {
	Visible int
	pointer *int
	number  float64
	text    string
}
type opaqueUncomparable struct {
	Visible int
	hidden  []int
}

func opaqueTestShape(t reflect.Type, exposed ...string) (*values.Shape, *Struct) {
	fields := BindSelectedFields(t, exposed, nil, Automatic)
	s := &values.Shape{Type: values.NewType(t.Name(), values.Pointer), Kind: values.StructKind, Cells: uint64(len(fields.Fields) + 1), Native: Layout(t, fields, false)}
	s.Native.OpaqueCell = s.Cells
	for i, f := range fields.Fields {
		shape := &values.Shape{Type: values.IntType, Kind: values.IntKind, Cells: 1, Bits: 64}
		s.Fields = append(s.Fields, values.Field{Name: f.Name, Shape: shape, Offset: uint64(i)})
	}
	return s, fields
}
func opaqueTestValue(s *values.Shape, fields *Struct, pointer unsafe.Pointer) values.Value {
	cells := make([]values.Value, s.Cells)
	for i, f := range fields.Fields {
		cells[i] = f.Get(pointer)
	}
	cells[s.Native.OpaqueCell-1] = values.Value{P: pointer}
	return values.Value{P: unsafe.Pointer(&cells[0])}
}

func TestOpaqueNativeComparison(t *testing.T) {
	for _, tc := range []struct {
		a, b      any
		wantError bool
	}{
		{&opaqueBefore{[]int{1}, 1}, &opaqueBefore{[]int{1}, 2}, true},
		{&opaqueAfter{1, []int{1}}, &opaqueAfter{2, []int{1}}, false},
		{&opaqueAfter{1, []int{1}}, &opaqueAfter{1, []int{1}}, true},
	} {
		typ := reflect.TypeOf(tc.a).Elem()
		s, fields := opaqueTestShape(typ, "Visible")
		a := opaqueTestValue(s, fields, reflect.ValueOf(tc.a).UnsafePointer())
		b := opaqueTestValue(s, fields, reflect.ValueOf(tc.b).UnsafePointer())
		equal, err := s.Equal(a, b, nil)
		if equal || (err != nil) != tc.wantError {
			t.Fatalf("%v comparison = %v, %v", typ, equal, err)
		}
		if _, err := s.Hash(a, nil); err == nil {
			t.Fatalf("%v hashed an uncomparable dynamic field", typ)
		}
	}
	s, fields := opaqueTestShape(reflect.TypeFor[opaqueScalars](), "Visible")
	pointer := new(int)
	aHost := &opaqueScalars{1, pointer, 0, "hidden"}
	bHost := &opaqueScalars{99, pointer, math.Copysign(0, -1), "hidden"}
	a := opaqueTestValue(s, fields, unsafe.Pointer(aHost))
	b := opaqueTestValue(s, fields, unsafe.Pointer(bHost))
	*(*values.Value)(b.P) = values.Int(1)
	if equal, err := s.Equal(a, b, nil); err != nil || !equal {
		t.Fatal("visible cells did not override snapshot", equal, err)
	}
	ah, err := s.Hash(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	bh, err := s.Hash(b, nil)
	if err != nil || ah != bh {
		t.Fatal("equal values hash differently", ah, bh, err)
	}
	bHost.text = "different"
	if equal, err := s.Equal(a, b, nil); err != nil || equal {
		t.Fatal("hidden field ignored", equal, err)
	}
	aHost.number = math.NaN()
	if equal, err := s.Equal(a, a, nil); err != nil || equal {
		t.Fatal("snapshot identity treated NaN as equal", equal, err)
	}
	aHost.number = 0
	zero := opaqueTestValue(s, fields, unsafe.Pointer(new(opaqueScalars)))
	if equal, err := s.Equal(values.Value{}, zero, nil); err != nil || !equal {
		t.Fatal("zero snapshot differs", equal, err)
	}
	zh, err := s.Hash(values.Value{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.Hash(zero, nil)
	if err != nil || h != zh {
		t.Fatal("zero hash differs", h, zh, err)
	}
	if allocs := testing.AllocsPerRun(100, func() { s.Equal(a, a, nil); s.Hash(a, nil) }); allocs != 0 {
		t.Fatal("comparison allocations", allocs)
	}
	u, _ := opaqueTestShape(reflect.TypeFor[opaqueUncomparable](), "Visible")
	if u.Comparable() {
		t.Fatal("hidden slice is comparable")
	}
	if _, err := u.Equal(values.Value{}, values.Value{}, nil); err == nil {
		t.Fatal("compared uncomparable native type")
	}
}

func TestOpaqueNativeComparisonLayout(t *testing.T) {
	type nested struct {
		Empty   struct{}
		Visible int
		private [2]complex128
		_       int
	}
	s, fields := opaqueTestShape(reflect.TypeFor[nested](), "Visible")
	aHost := &nested{Visible: 1, private: [2]complex128{1 + 2i, 3 + 4i}}
	bHost := *aHost
	a := opaqueTestValue(s, fields, unsafe.Pointer(aHost))
	b := opaqueTestValue(s, fields, unsafe.Pointer(&bHost))
	if equal, err := s.Equal(a, b, nil); err != nil || !equal {
		t.Fatal(equal, err)
	}
	ah, err := s.Hash(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	bh, err := s.Hash(b, nil)
	if err != nil || ah != bh {
		t.Fatal(ah, bh, err)
	}
	bHost.private[1] = 4 + 3i
	if equal, err := s.Equal(a, b, nil); err != nil || equal {
		t.Fatal(equal, err)
	}
	type blankSlice struct{ _ []int }
	u, _ := opaqueTestShape(reflect.TypeFor[blankSlice]())
	if u.Comparable() {
		t.Fatal("blank slice field made type comparable")
	}
}

func TestOpaqueNativeComparisonMatchesGo(t *testing.T) {
	type runs struct {
		First   any
		Visible int
		Last    any
	}
	type hiddenRun struct {
		first   int
		last    any
		Visible int
	}
	type nestedRun struct {
		hidden  [2]any
		Visible int
	}
	pairs := [][2]any{
		{&runs{1, 1, []int{2}}, &runs{1, 2, []int{2}}},
		{&runs{[]int{1}, 1, 2}, &runs{[]int{1}, 2, 2}},
		{&hiddenRun{1, []int{2}, 1}, &hiddenRun{2, []int{2}, 1}},
		{&nestedRun{[2]any{1, []int{2}}, 1}, &nestedRun{[2]any{2, []int{2}}, 1}},
		{&nestedRun{[2]any{1, []int{2}}, 1}, &nestedRun{[2]any{1, []int{2}}, 1}},
	}
	for _, pair := range pairs {
		typ := reflect.TypeOf(pair[0]).Elem()
		s, fields := opaqueTestShape(typ, "Visible")
		a := opaqueTestValue(s, fields, reflect.ValueOf(pair[0]).UnsafePointer())
		b := opaqueTestValue(s, fields, reflect.ValueOf(pair[1]).UnsafePointer())
		var want, panicked bool
		func() {
			defer func() { panicked = recover() != nil }()
			want = reflect.ValueOf(pair[0]).Elem().Interface() == reflect.ValueOf(pair[1]).Elem().Interface()
		}()
		got, err := s.Equal(a, b, nil)
		if got != want || (err != nil) != panicked {
			t.Fatalf("%v: VM %v/%v, Go %v/%v", typ, got, err, want, panicked)
		}
	}
}

func TestOpaqueNativeZeroSizedExposedField(t *testing.T) {
	type sharedOffset struct {
		Empty  struct{}
		hidden int
	}
	typ := reflect.TypeFor[sharedOffset]()
	fields := BindSelectedFields(typ, []string{"Empty"}, nil, Automatic)
	empty := &values.Shape{Type: values.NewType("struct{}", values.Pointer), Kind: values.StructKind}
	s := &values.Shape{Type: values.NewType("sharedOffset", values.Pointer), Kind: values.StructKind, Cells: 1, Fields: []values.Field{{Name: "Empty", Shape: empty}}, Native: Layout(typ, fields, false)}
	s.Native.OpaqueCell = 1
	aHost := &sharedOffset{hidden: 1}
	bHost := &sharedOffset{hidden: 2}
	ac := values.Value{P: unsafe.Pointer(aHost)}
	bc := values.Value{P: unsafe.Pointer(bHost)}
	a := values.Value{P: unsafe.Pointer(&ac)}
	b := values.Value{P: unsafe.Pointer(&bc)}
	if equal, err := s.Equal(a, b, nil); err != nil || equal {
		t.Fatal("hidden field sharing exposed offset", equal, err)
	}
}
