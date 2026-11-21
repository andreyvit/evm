package evm_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/kvo/codec/row2"
	"github.com/andreyvit/evm/nativebind"
	"github.com/andreyvit/evm/values"
)

type mutableNativeRow struct {
	Name    string              `kvo:"name"`
	Count   int64               `kvo:"count"`
	Child   *mutableNativeRow   `kvo:"child"`
	Kids    []*mutableNativeRow `kvo:"kids"`
	Tags    []string            `kvo:"tags"`
	Bytes   []byte              `kvo:"bytes"`
	Scores  map[string]int64    `kvo:"scores"`
	Nested  [][]string          `kvo:"nested"`
	Numbers []int64             `kvo:"numbers"`
	Hidden  int                 `kvo:"-"`
}

func TestMutableNativeRowAliases(t *testing.T) {
	schema := kvo.NewSchema()
	entity := kvo.NewEntityType(schema, "Row", reflect.TypeFor[mutableNativeRow]())
	for i, field := range []struct {
		name string
		t    kvo.AnyType
	}{
		{"name", kvo.TMachineString}, {"count", kvo.TInt64}, {"child", entity}, {"kids", kvo.ListOf(entity)},
		{"tags", kvo.ListOf(kvo.TMachineString)}, {"bytes", kvo.TBytes}, {"scores", kvo.MapOf(kvo.TMachineString, kvo.TInt64)},
		{"nested", kvo.ListOf(kvo.ListOf(kvo.TMachineString))}, {"numbers", kvo.ListOf(kvo.TInt64)},
	} {
		kvo.NewProp(schema, uint64(i+1), field.name, field.t, nil)
	}
	model := kvo.NewModel(schema, entity, func(b *kvo.ModelBuilder) {
		for code := uint64(1); code <= 9; code++ {
			b.Prop(schema.MustPropByCode(code))
		}
	})
	db, err := evm.ModelPackage("db", schema)
	if err != nil {
		t.Fatal(err)
	}
	old := &mutableNativeRow{Name: "old", Hidden: 73}
	row := &mutableNativeRow{Name: "before", Child: old, Kids: []*mutableNativeRow{old, nil}, Tags: make([]string, 1, 3), Bytes: []byte{1, 2}, Scores: map[string]int64{"a": 1}, Nested: [][]string{{"before"}}, Numbers: []int64{1, 2}, Hidden: 42}
	row.Tags[0] = "before"
	replacement := &mutableNativeRow{Name: "new"}
	binding := kvo.Bind(reflect.TypeFor[mutableNativeRow](), model)
	encoded := binding.Encode(nil, reflect.ValueOf(mutableNativeRow{Name: "borrowed from encoded row"}), row2.Fast2)
	checks := 0
	host := evm.Package{Path: "host", Functions: []evm.ExternalFunction{
		{Name: "Get", Signature: evm.Signature{Results: []*values.Type{db.Record(model, true).Pointer}}, Call: func(c *evm.Call) error {
			v, err := evm.NativeMutableRecordValue(model, binding.Record(reflect.ValueOf(row)))
			c.SetResult(0, v)
			return err
		}},
		{Name: "Encoded", Signature: evm.Signature{Results: []*values.Type{db.Record(model, false).Pointer}}, Call: func(c *evm.Call) error { return c.SetReadRecordResult(model, row2.Trusted(encoded)) }},
		{Name: "Invalidate", Call: func(c *evm.Call) error { clear(encoded); return nil }},
		{Name: "Swap", Call: func(c *evm.Call) error { row.Child = replacement; return nil }},
		{Name: "Check", Call: func(c *evm.Call) error {
			checks++
			if row.Count != 42 || row.Name != "after" || old.Name != "retained" || row.Kids[1] != old || len(row.Tags) != 2 || row.Tags[0] != "shared" || row.Tags[1] != "added" || row.Bytes[0] != 9 || row.Bytes[1] != 8 || row.Nested[0][0] != "nested" || row.Numbers[1] != 12 || row.Hidden != 42 || old.Hidden != 73 {
				t.Error("native changes not immediate")
			}
			return nil
		}},
	}}
	plain := []int64{5, 6}
	integer := &values.Shape{Type: values.Int64, Kind: values.IntKind, Bits: 64, Cells: 1}
	numbers := &values.Shape{Type: values.NewType("[]int64", values.Pair), Kind: values.SliceKind, Elem: integer, Cells: 1, Native: nativebind.Layout(reflect.TypeOf(plain), nil, false)}
	host.Types = append(host.Types, evm.TypeDecl{Shape: numbers})
	host.Functions = append(host.Functions, evm.ExternalFunction{Name: "Plain", Signature: evm.Signature{Results: []*values.Type{numbers.Type}}, Call: func(c *evm.Call) error {
		c.SetResult(0, nativebind.Load(reflect.TypeOf(plain), unsafe.Pointer(&plain)))
		return nil
	}})
	mod, err := evmcompiler.Compile("aliases.go", `import("db";"host")
 func Run(){
  p:=host.Get();p.name="after";p.count=42
  child:=p.child;host.Swap();if p.child==child||p.child.name!="new"{panic("child cached")}
  child.name="retained"
  kids:=p.kids;if kids[1]!=nil{panic("nil child")};kids[1]=child
  if kids[0]!=child||kids[1]!=child{panic("child list identity")}
  var box any=kids[0];if box.(*db.RowEdit)!=child{panic("list element boxing")}
  tags:=p.tags
  if len(tags)!=1||cap(tags)!=3||tags==nil{panic("slice header")}
  q:=&tags[0];*q="address"
  (*[1]string)(tags)[0]="array"
  for i:=range tags {tags[i]+="!"}
  if tags[0]!="array!"{panic("slice addresses")}
  tags[0]="shared";p.tags=append(tags,"added")
  if len(p.tags)!=2{panic("changed header")}
  data:=p.bytes
  if copy(data,"ab")!=2 || string(data)!="ab" || string(append(data,"cd"...))!="abcd"{panic("native byte copy")}
  data[0]=9;data[1]=8
  if len(data)!=2||data[0]!=9||data[1]!=8{panic("byte storage")}
  guest:=[]byte{3,4};if guest[1]!=4{panic("guest slice")}
  choice:=guest;if p.count==42{choice=data};choice[1]=8
  p.nested[0][0]="nested";p.numbers[1]+=10
  if len(p.nested)!=1||len(p.nested[0])!=1||p.numbers[1]!=12{panic("nested lists")}
  plain:=host.Plain()
  both:=append(plain,p.numbers...)
  if len(both)!=4||both[0]!=5||both[1]!=6||both[2]!=1||both[3]!=12{panic("mixed append")}
  if copy(plain,p.numbers)!=2 || plain[0]!=1 || plain[1]!=12 {panic("mixed copy")}
  host.Check()
 }
 func OwnedStrings(){
 p:=host.Get();s:=host.Encoded().name
 p.tags[0]=s;p.tags=append(p.tags,s)
 host.Invalidate()
 if p.tags[0]!="borrowed from encoded row"||p.tags[2]!="borrowed from encoded row"{panic("borrowed string escaped")}
 }
 func NilChild(){host.Get().child.name="bad"}
 func Map(){host.Get().scores["a"]=2}
 func RejectSlice(){host.Get().tags=[]string{"copy"}}
 `, db, host)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(mod, db, host)
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(p).Run(context.Background(), "Run", nil, nil); err != nil {
		t.Fatalf("%v\n%s", err, mod.Disassemble())
	}
	if checks != 1 {
		t.Fatal("native check not called")
	}
	row.Child = nil
	for _, tc := range []struct{ name, contains string }{{"NilChild", "invalid"}, {"Map", "native map layout"}, {"RejectSlice", "explicit conversion"}} {
		if err := evm.New(p).Run(context.Background(), tc.name, nil, nil); err == nil || !strings.Contains(err.Error(), tc.contains) {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
	if row.Child != nil || row.Scores["a"] != 1 || row.Tags[0] != "shared" {
		t.Fatal("failed operations changed native data")
	}
	if err := evm.New(p).Run(context.Background(), "OwnedStrings", nil, nil); err != nil {
		t.Fatal(err)
	}
	if row.Tags[0] != "borrowed from encoded row" || row.Tags[2] != "borrowed from encoded row" {
		t.Fatal("native strings retain borrowed memory")
	}
}
