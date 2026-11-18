package kvo_test

import (
	"reflect"
	"testing"

	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/kvo/codec/msgpack"
)

type cursorArrayChild struct {
	Name string `msgpack:"name" kvo:"name"`
}
type cursorPointerArray struct {
	Children [3]*cursorArrayChild `msgpack:"children" kvo:"children"`
}
type cursorValueArray struct {
	Children [3]cursorArrayChild `msgpack:"children" kvo:"children"`
}

func TestRecordCursorArrayZeroFill(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[cursorPointerArray](), reflect.TypeFor[cursorValueArray]()} {
		t.Run(typ.Name(), func(t *testing.T) {
			schema := kvo.NewSchema()
			ct := kvo.NewEntityType(schema, "Child", reflect.TypeFor[cursorArrayChild]())
			rt := kvo.NewEntityType(schema, "Root", typ)
			name := kvo.NewStringProp[string](schema, 1, "name", kvo.TMachineString)
			kvo.NewModel(schema, ct, func(b *kvo.ModelBuilder) { b.Prop(name) })
			children := kvo.NewRecordListProp(schema, 2, "children", kvo.ListOf(ct))
			root := kvo.NewModel(schema, rt, func(b *kvo.ModelBuilder) { b.Prop(children) })
			projection := kvo.Project(typ, root, nil)
			t.Run("oversized", func(t *testing.T) {
				defer func() {
					if recover() == nil {
						t.Fatal("oversized array accepted")
					}
				}()
				raw := msgpack.Encode(nil, reflect.ValueOf(struct {
					Children []*cursorArrayChild `msgpack:"children"`
				}{make([]*cursorArrayChild, 4)}))
				kvo.BindListAccess(root, 2).Records(projection.MessagePack(raw)).Cursor()
			})
			for _, count := range []int{-2, -1, 0, 1, 3} {
				var items []*cursorArrayChild
				if count >= 0 {
					items = make([]*cursorArrayChild, count)
				}
				if count > 0 {
					items[0] = &cursorArrayChild{Name: "first"}
				}
				raw := msgpack.Encode(nil, reflect.ValueOf(struct {
					Children []*cursorArrayChild `msgpack:"children"`
				}{items}))
				if count == -2 {
					raw = []byte{0x80}
				}
				list := kvo.BindListAccess(root, 2).Records(projection.MessagePack(raw))
				if list.Missing() {
					t.Fatal("array cannot be nil")
				}
				cursor := list.Cursor()
				for i := 0; i < 3; i++ {
					row, ok := cursor.Next()
					if !ok {
						t.Fatalf("count %d: missing position %d", count, i)
					}
					if i == 0 && count > 0 {
						if row.Get(name) != "first" {
							t.Fatal("first value")
						}
					} else if typ == reflect.TypeFor[cursorPointerArray]() {
						if !row.Missing() {
							t.Fatal("pointer zero must be nil")
						}
					} else if row.Missing() || row.Get(name) != "" {
						t.Fatal("struct zero must be nonnil empty row")
					}
				}
				if _, ok := cursor.Next(); ok {
					t.Fatal("array cursor exceeded length")
				}
			}
		})
	}
}
