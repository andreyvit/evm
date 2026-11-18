package kvo_test

import (
	"reflect"
	"testing"
	"unsafe"

	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/kvo/codec/row2"
	"github.com/andreyvit/evm/values"
)

func TestDirectNativeAssignment(t *testing.T) {
	type Counts struct {
		Count int64 `kvo:"count"`
	}
	type Row struct {
		Name   string `kvo:"name"`
		Counts `kvo:",inline"`
		Hidden int `kvo:"-"`
	}
	s := kvo.NewSchema()
	name := kvo.NewStringProp[string](s, 1, "name", kvo.TMachineString)
	count := kvo.NewSignedProp[int64](s, 2, "count", kvo.TInt64)
	m := kvo.NewModel(s, kvo.NewEntityType(s, "Row", reflect.TypeFor[Row]()), func(b *kvo.ModelBuilder) { b.Prop(name); b.Prop(count) })
	b := kvo.Bind(reflect.TypeFor[Row](), m)
	row := &Row{Hidden: 42}
	r := b.Record(reflect.ValueOf(row))
	data := []byte("borrowed")
	borrowed := unsafe.String(unsafe.SliceData(data), len(data))
	if err := r.TrySetNativeValue(row2.Step{Key: 1}, values.String(borrowed)); err != nil {
		t.Fatal(err)
	}
	data[0] = 'X'
	if row.Name != "borrowed" || row.Hidden != 42 {
		t.Fatal("string ownership or hidden field changed")
	}
	typ, ptr, err := r.NativeField(2)
	if err != nil || typ != reflect.TypeFor[int64]() || ptr != unsafe.Pointer(&row.Count) {
		t.Fatal("inline field address", typ, err)
	}
	var writeErr error
	allocs := testing.AllocsPerRun(100, func() { writeErr = r.TrySetNativeValue(row2.Step{Key: 2}, values.Int(row.Count+1)) })
	if writeErr != nil || allocs != 0 {
		t.Fatalf("scalar assignment: allocations=%v error=%v", allocs, writeErr)
	}
	encoded := b.Encoded(row2.Trusted(b.Encode(nil, reflect.ValueOf(*row), row2.Fast2)))
	if err := encoded.TrySetNativeValue(row2.Step{Key: 2}, values.Int(0)); err == nil {
		t.Fatal("encoded row accepted as native")
	}
}
