package evmcompiler_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/kvo/codec/msgpack"
	"github.com/andreyvit/evm/values"
)

type nullableListChild struct {
	Name   string `msgpack:"name" kvo:"name"`
	Hidden string `msgpack:"hidden" kvo:"-"`
}
type nullableListRoot struct {
	Children []*nullableListChild `msgpack:"children" kvo:"children"`
	Hidden   string               `msgpack:"hidden" kvo:"-"`
}

func TestRecordObjectListsPreserveNils(t *testing.T) {
	schema := kvo.NewSchema()
	childType := kvo.NewEntityType(schema, "Child", reflect.TypeFor[nullableListChild]())
	rootType := kvo.NewEntityType(schema, "Root", reflect.TypeFor[nullableListRoot]())
	name := kvo.NewStringProp[string](schema, 1, "name", kvo.TMachineString)
	children := kvo.NewRecordListProp(schema, 2, "children", kvo.ListOf(childType))
	child := kvo.NewModel(schema, childType, func(b *kvo.ModelBuilder) { b.Prop(name) })
	root := kvo.NewModel(schema, rootType, func(b *kvo.ModelBuilder) { b.Prop(children) })
	projection := kvo.Project(reflect.TypeFor[nullableListRoot](), root, nil)
	for _, native := range []bool{false, true} {
		label := "MessagePack"
		if native {
			label = "Native"
		}
		t.Run(label, func(t *testing.T) {
			source := &nullableListRoot{Children: []*nullableListChild{{Name: "first", Hidden: "child secret"}, nil, {Name: "last"}}, Hidden: "root secret"}
			pkg, err := evm.ModelPackage("db", schema)
			if err != nil {
				t.Fatal(err)
			}
			seen := 0
			pkg.Functions = append(pkg.Functions, evm.ExternalFunction{Name: "Observe", Signature: evm.Signature{Params: []*values.Type{pkg.Record(root, false).Pointer, pkg.Record(child, false).Pointer}}, Call: func(c *evm.VM) error {
				parent, err := c.Record(c.CallArgument(0), root)
				if err != nil {
					return err
				}
				item, err := c.Record(c.CallArgument(1), child)
				if err != nil {
					return err
				}
				if _, err := c.Record(c.CallArgument(0), child); err == nil {
					t.Fatal("wrong model accepted")
				}
				if zero, err := c.Record(values.Value{}, root); err != nil || !zero.Missing() {
					t.Fatal("nil record", err)
				}
				if parent.Model() != root || item.Model() != child {
					t.Fatal("model lost")
				}
				if native {
					if parent.Native() != source || item.Native() != source.Children[0] {
						t.Fatal("native identity lost")
					}
				} else {
					if string(msgpack.Trusted(parent.MessagePack()).Lookup("hidden").Bytes()) != "root secret" || string(msgpack.Trusted(item.MessagePack()).Lookup("hidden").Bytes()) != "child secret" {
						t.Fatal("unexposed MessagePack fields lost")
					}
				}
				seen++
				return nil
			}})
			module, err := evmcompiler.Compile("objects.go", `import "db"
 var saved *db.Root
 var items []*db.Child
 func Run(row *db.Root)func(){saved=row;items=row.children;Check();r,children:=saved,items;return func(){saved=r;items=children;Check()}}
 func Check(){if len(items)!=3 || items[0].name!="first" || items[1]!=nil || items[2].name!="last"{panic("object list changed")};n:=0;for _,item:=range saved.children{if item==nil{n++}};if n!=1{panic("nil iteration")};db.Observe(saved,items[0]);copy:=*saved;db.Observe(&copy,items[0])}
 func NativeLists(row *db.Root)bool {
  cells:=row.children
  native:=make([]*db.Child,len(cells))
  if copy(native,cells)!=3 || native[0].name!="first" || native[1]!=nil {return false}
  native[0]=nil
  if copy(cells,native)!=3 || cells[0]!=nil {return false}
  cells=append(cells,native...)
  return len(cells)==6 && cells[3]==nil && cells[5].name=="last"
 }
 func Nil(row *db.Root)bool{return row.children==nil}
 `, pkg)
			if err != nil {
				t.Fatal(err)
			}
			program, err := evm.Link(module, pkg)
			if err != nil {
				t.Fatal(err)
			}
			vm := evmtest.New(program)
			var raw []byte
			row := projection.Record(reflect.ValueOf(source))
			if !native {
				raw = msgpack.Encode(nil, reflect.ValueOf(source))
				row = projection.MessagePack(raw)
			}
			var saved [1]values.Value
			if err := vm.Run(context.Background(), "Run", []values.Value{row.Value()}, saved[:]); err != nil {
				t.Fatal(err)
			}
			clear(raw)
			check, err := program.Callable(saved[0])
			if err != nil {
				t.Fatal(err)
			}
			if err := vm.VM.Run(check, nil); err != nil {
				t.Fatal(err)
			}
			if seen != 4 {
				t.Fatal("missing observations", seen)
			}
			if native {
				var out [1]values.Value
				if err := vm.Run(context.Background(), "NativeLists", []values.Value{row.Value()}, out[:]); err != nil || out[0].R != 1 {
					t.Fatal("mixed cell/native list operations", out, err)
				}
			}
			for _, data := range [][]*nullableListChild{nil, {}} {
				value := &nullableListRoot{Children: data}
				row := projection.Record(reflect.ValueOf(value))
				if !native {
					row = projection.MessagePack(msgpack.Encode(nil, reflect.ValueOf(value)))
				}
				var out [1]values.Value
				if err := vm.Run(context.Background(), "Nil", []values.Value{row.Value()}, out[:]); err != nil || out[0].AsBool() != (data == nil) {
					t.Fatal("nil versus empty", out, err)
				}
			}
		})
	}
}
