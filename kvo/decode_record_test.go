package kvo_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/andreyvit/evm/internal/statrow"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/kvo/codec/msgpack"
	"github.com/andreyvit/evm/kvo/codec/row2"
	"github.com/andreyvit/evm/nativebind"
)

type decodeRecordRow struct {
	Name   string    `kvo:"name" msgpack:"name"`
	Hidden []*string `kvo:"-" msgpack:"hidden"`
}
type decodeRecordAlternate struct {
	Label string `kvo:"name" msgpack:"label"`
}

func TestDecodeRecordRepresentations(t *testing.T) {
	schema := kvo.NewSchema()
	entity := kvo.NewEntityType(schema, "Row", reflect.TypeFor[decodeRecordRow](), reflect.TypeFor[decodeRecordAlternate]())
	name := kvo.NewStringProp[string](schema, 1, "name", kvo.TMachineString)
	model := kvo.NewModel(schema, entity, func(b *kvo.ModelBuilder) { b.Prop(name) })
	binding := kvo.Bind(reflect.TypeFor[decodeRecordRow](), model)
	projection := kvo.Project(reflect.TypeFor[decodeRecordRow](), model, nil)
	alternate := kvo.Bind(reflect.TypeFor[decodeRecordAlternate](), model)
	text := "unmodeled"
	source := decodeRecordRow{Name: "original", Hidden: []*string{nil, &text, nil}}
	native := binding.Record(reflect.ValueOf(&source))
	for _, layout := range []row2.Layout{row2.Fast2, row2.Compact2} {
		raw := binding.Encode(nil, reflect.ValueOf(source), layout)
		for _, record := range []kvo.Record{binding.Encoded(row2.Trusted(raw)), kvo.Encoded(row2.Trusted(raw)), kvo.Encoded(row2.Trusted(raw)).WithModel(model).Fastest()} {
			for _, decode := range []func(kvo.Record, reflect.Value) error{binding.TryDecodeRecord, projection.TryDecodeRecord} {
				result := decodeRecordRow{Name: "stale", Hidden: source.Hidden}
				if err := decode(record, reflect.ValueOf(&result).Elem()); err != nil {
					t.Fatal(err)
				}
				if result.Name != "original" || result.Hidden != nil {
					t.Fatalf("decoded %#v", result)
				}
			}
		}
	}
	for _, decode := range []func(kvo.Record, reflect.Value) error{binding.TryDecodeRecord, projection.TryDecodeRecord} {
		var result decodeRecordRow
		if err := decode(native, reflect.ValueOf(&result).Elem()); err != nil {
			t.Fatal(err)
		}
		if result.Hidden[1] != source.Hidden[1] || &result.Hidden[0] != &source.Hidden[0] {
			t.Fatal("native assignment lost aliases")
		}
		if native.Native() != &source {
			t.Fatal("native pointer identity changed")
		}
		raw := msgpack.Encode(nil, reflect.ValueOf(&source))
		if err := decode(projection.MessagePack(raw), reflect.ValueOf(&result).Elem()); err != nil {
			t.Fatal(err)
		}
		clear(raw)
		if result.Name != "original" || len(result.Hidden) != 3 || result.Hidden[0] != nil || result.Hidden[2] != nil || *result.Hidden[1] != "unmodeled" {
			t.Fatalf("MessagePack fields lost: %#v", result)
		}
		if result.Hidden[1] == source.Hidden[1] {
			t.Fatal("decoded pointer aliases source")
		}
	}
	for _, record := range []kvo.Record{native, projection.MessagePack(msgpack.Encode(nil, reflect.ValueOf(&source)))} {
		var result decodeRecordAlternate
		if err := alternate.TryDecodeRecord(record, reflect.ValueOf(&result).Elem()); err != nil {
			t.Fatal(err)
		}
		if result.Label != "original" {
			t.Fatal("alternate native field mapping", result)
		}
	}
	raw := bytes.Clone(binding.Encode(nil, reflect.ValueOf(source), row2.Compact2))
	var result decodeRecordRow
	if err := binding.TryDecodeRecord(kvo.Encoded(row2.Trusted(raw)), reflect.ValueOf(&result).Elem()); err != nil {
		t.Fatal(err)
	}
	clear(raw)
	if result.Name != "original" {
		t.Fatal("decoded string borrows wire data")
	}
	for _, dst := range []reflect.Value{reflect.Value{}, reflect.ValueOf(source), reflect.ValueOf(&text).Elem()} {
		if err := binding.TryDecodeRecord(native, dst); err == nil {
			t.Fatal("invalid destination accepted")
		}
	}
	badSchema := kvo.NewSchema()
	badName := kvo.NewStringProp[string](badSchema, 1, "name", kvo.TMachineString)
	other := kvo.NewModel(badSchema, kvo.NewEntityType(badSchema, "Other", reflect.TypeFor[decodeRecordRow]()), func(b *kvo.ModelBuilder) { b.Prop(badName) })
	wrong := kvo.Bind(reflect.TypeFor[decodeRecordRow](), other).Record(reflect.ValueOf(&source))
	if err := binding.TryDecodeRecord(wrong, reflect.ValueOf(&result).Elem()); err == nil {
		t.Fatal("wrong model accepted")
	}
	corrupt := projection.MessagePack(msgpack.Encode(nil, reflect.ValueOf(map[string]any{"name": 123})))
	if err := binding.TryDecodeRecord(corrupt, reflect.ValueOf(&result).Elem()); err == nil {
		t.Fatal("invalid MessagePack field accepted")
	}
	if err := binding.TryDecodeRecord(kvo.Record{}, reflect.ValueOf(&result).Elem()); err == nil {
		t.Fatal("missing record accepted")
	}
}

func TestDecodeRecordGenerated(t *testing.T) {
	_, model := statrow.Model()
	base := kvo.Bind(reflect.TypeFor[statrow.State](), model)
	generated := base.WithGenerated(&statrow.Static, nativebind.StaticOnly)
	projection := kvo.Project(reflect.TypeFor[statrow.State](), model, nil)
	for _, source := range []*statrow.State{statrow.Sample(7), {}} {
		source.Key = 0 // Not stored by either codec.
		for _, tc := range recordContractCases(base, model) {
			t.Run(tc.name, func(t *testing.T) {
				for _, decode := range []func(kvo.Record, reflect.Value) error{generated.TryDecodeRecord, projection.TryDecodeRecord} {
					got := *statrow.Sample(9)
					if err := decode(tc.open(source), reflect.ValueOf(&got).Elem()); err != nil {
						t.Fatal(err)
					}
					got.Created = got.Created.UTC()
					got.LastActivity = got.LastActivity.UTC()
					if !reflect.DeepEqual(got, *source) {
						t.Fatalf("got %#v, want %#v", got, *source)
					}
				}
			})
		}
	}
}
