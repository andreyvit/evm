package kvo_test

import (
	"reflect"
	"testing"

	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/kvo/codec/row2"
)

func TestRecordNativePointer(t *testing.T) {
	s := kvo.NewSchema()
	name := kvo.NewStringProp[string](s, 1, "name", kvo.TMachineString)
	m := kvo.NewModel(s, kvo.NewEntityType(s, "Row", reflect.TypeFor[decodeRecordRow]()), func(b *kvo.ModelBuilder) { b.Prop(name) })
	b := kvo.Bind(reflect.TypeFor[decodeRecordRow](), m)
	p := kvo.Project(reflect.TypeFor[decodeRecordRow](), m, nil)
	for _, open := range []func(reflect.Value) kvo.Record{b.Record, p.Record} {
		for _, ptr := range []*decodeRecordRow{nil, {Name: "live"}} {
			r := open(reflect.ValueOf(ptr))
			if r.NativePointer() != ptr || r.Model() != m || r.Missing() != (ptr == nil) {
				t.Fatal("native pointer metadata lost")
			}
			if ptr == nil && r.Native() != nil {
				t.Fatal("Native changed missing-record behavior")
			}
			if ptr != nil {
				ptr.Name = "changed"
				if r.GetValue(1).AsString() != "changed" {
					t.Fatal("native alias lost")
				}
			}
		}
	}
	raw := b.Encode(nil, reflect.ValueOf(decodeRecordRow{Name: "encoded"}), row2.Fast2)
	for _, r := range []kvo.Record{{}, b.Encoded(row2.Trusted(raw)), kvo.Encoded(row2.Trusted(raw))} {
		if r.NativePointer() != nil {
			t.Fatal("encoded record unexpectedly materialized")
		}
	}
}
