package evmbind

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/kvo/codec/row2"
	"github.com/andreyvit/evm/values"
)

type CollectionItem struct{ Title string }
type CollectionPanel struct {
	Items  []*CollectionItem
	Named  map[string]*CollectionItem
	Styles map[string]map[string]string
}

// Source-selected native structures must retain Go collection semantics. The
// same item is reachable through a slice and a map; writes must reach the host.
func TestSelectedNativeCollectionFields(t *testing.T) {
	files := fstest.MapFS{"types.go": {Data: []byte(`package native
 type CollectionItem struct { Title string }
 type CollectionPanel struct {
  Items []*CollectionItem
  Named map[string]*CollectionItem
  Styles map[string]map[string]string
 }`)}}
	schema, err := New("host/collections", files, Options{Only: []string{"CollectionPanel"}, Types: []reflect.Type{reflect.TypeFor[CollectionPanel](), reflect.TypeFor[CollectionItem]()}})
	if err != nil {
		t.Fatal(err)
	}
	pkg := schema.Package()
	module, err := evmcompiler.Compile("check.go", `import ("host/collections";"fmt")
 func Run(p *collections.CollectionPanel) string {
  p.Items[0].Title = "changed"
  if p.Named["first"] != p.Items[0] { panic("lost pointer identity") }
  p.Named["second"] = p.Items[0]
  p.Styles["card"]["color"] = "blue"
  fresh:=map[string]*collections.CollectionItem{"third":p.Items[0]}
  for key,value:=range fresh { p.Named[key]=value }
  delete(p.Named,"first")
  clear(fresh)
  if len(fresh)!=0 || len(p.Named)!=2 { panic("invalid map size") }
  var empty map[string]*collections.CollectionItem
  if _,ok:=empty["missing"];ok { panic("nil map lookup succeeded") }
  delete(empty,"missing")
  for range empty { panic("nil map yielded an entry") }
  return p.Named["second"].Title
 }
 func Format(p *collections.CollectionPanel) string {return fmt.Sprint(p.Styles)}
 func JSON(p *collections.CollectionPanel) any {return p.Styles}
 `, pkg)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, pkg)
	if err != nil {
		t.Fatal(err)
	}
	item := &CollectionItem{Title: "original"}
	panel := &CollectionPanel{Items: []*CollectionItem{item}, Named: map[string]*CollectionItem{"first": item}, Styles: map[string]map[string]string{"card": {"color": "red"}}}
	arg, err := schema.NativePointer(panel)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evmtest.New(program).Run(context.Background(), "Run", []values.Value{arg}, out[:]); err != nil {
		t.Fatal(err)
	}
	if out[0].AsString() != "changed" || panel.Named["second"] != item || panel.Named["third"] != item || panel.Named["first"] != nil || item.Title != "changed" || panel.Styles["card"]["color"] != "blue" {
		t.Fatalf("native collection writes or aliasing lost: result=%q panel=%+v", out[0].AsString(), panel)
	}
	if err := evmtest.New(program).Run(context.Background(), "Format", []values.Value{arg}, out[:]); err != nil {
		t.Fatal(err)
	}
	if out[0].AsString() != "map[card:map[color:blue]]" {
		t.Fatal(out[0].AsString())
	}
	if err := evmtest.New(program).Run(context.Background(), "JSON", []values.Value{arg}, out[:]); err != nil {
		t.Fatal(err)
	}
	encoded, err := program.EncodeJSON(context.Background(), out[0], 1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"card":{"color":"blue"}}` {
		t.Fatal(string(encoded))
	}
	if _, err := program.EncodeJSON(context.Background(), out[0], 4); err == nil {
		t.Fatal("JSON size limit ignored")
	}

}

type SlicePanel struct{ Items []*CollectionItem }

func TestSelectedNativeSliceFields(t *testing.T) {
	files := fstest.MapFS{"types.go": {Data: []byte(`package native
 type CollectionItem struct { Title string }
 type SlicePanel struct { Items []*CollectionItem }`)}}
	schema, err := New("host/slices", files, Options{Only: []string{"SlicePanel"}, Types: []reflect.Type{reflect.TypeFor[SlicePanel](), reflect.TypeFor[CollectionItem]()}})
	if err != nil {
		t.Fatal(err)
	}
	pkg := schema.Package()
	module, err := evmcompiler.Compile("check.go", `import "host/slices"
 func Run(p *slices.SlicePanel) string {
  p.Items[0].Title="changed"
  original:=p.Items
  copy(original[1:],original[:1])
  grown:=append(original,original...)
  clear(grown[2:])
  p.Items=grown
  fresh:=make([]*slices.CollectionItem,1,2)
  fresh[0]=original[0]
  literal:=[]*slices.CollectionItem{fresh[0]}
  literal=append(literal,fresh[0])
  arr:=(*[2]*slices.CollectionItem)(literal)
  arr[1]=nil
  if literal[1]!=nil { panic("array conversion lost aliasing") }
  return literal[0].Title
 }`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, pkg)
	if err != nil {
		t.Fatal(err)
	}
	item := &CollectionItem{Title: "original"}
	panel := &SlicePanel{Items: []*CollectionItem{item, nil}}
	arg, err := schema.NativePointer(panel)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evmtest.New(program).Run(context.Background(), "Run", []values.Value{arg}, out[:]); err != nil {
		t.Fatal(err)
	}
	if out[0].AsString() != "changed" || len(panel.Items) != 4 || panel.Items[1] != item || panel.Items[2] != nil || panel.Items[3] != nil {
		t.Fatal("lost native slice aliasing")
	}
}

type ScalarSlices struct {
	Bytes []byte
	Runes []rune
	Small []int16
}

func TestNativeScalarSliceWidthsAndConversions(t *testing.T) {
	files := fstest.MapFS{"types.go": {Data: []byte(`package native
 type ScalarSlices struct { Bytes []byte; Runes []rune; Small []int16 }`)}}
	schema, err := New("host/scalars", files, Options{Selective: true, Types: []reflect.Type{reflect.TypeFor[ScalarSlices]()}})
	if err != nil {
		t.Fatal(err)
	}
	pkg := schema.Package()
	module, err := evmcompiler.Compile("check.go", `import ("host/scalars";"fmt")
 func Run(p *scalars.ScalarSlices) string {
  p.Bytes=append([]byte{'a'},"bc"...)
  copy(p.Bytes,"!")
  p.Runes=[]rune("hé")
  p.Runes[0]='H'
  p.Small=make([]int16,2)
  p.Small[0]=-7;p.Small[1]=300
  return fmt.Sprintf("%s/%s/%d",p.Bytes,string(p.Runes),p.Small[0]+p.Small[1])
 }`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, pkg)
	if err != nil {
		t.Fatal(err)
	}
	panel := &ScalarSlices{}
	arg, err := schema.NativePointer(panel)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evmtest.New(program).Run(context.Background(), "Run", []values.Value{arg}, out[:]); err != nil {
		t.Fatal(err)
	}
	if out[0].AsString() != "!bc/Hé/293" || string(panel.Bytes) != "!bc" || string(panel.Runes) != "Hé" || !reflect.DeepEqual(panel.Small, []int16{-7, 300}) {
		t.Fatal(out[0].AsString(), panel, module.Disassemble())
	}
}

// Registering native collections also changes the representation of matching
// collection types read from KVO. Exercise edits and a second decode after save.
func TestNativeCollectionsInRecordFields(t *testing.T) {
	files := fstest.MapFS{"types.go": {Data: []byte(`package native
 type RecordCollectionFields struct { Extra any; Data []byte; Counts []int64; Labels map[string]string }`)}}
	schema, err := New("host/fields", files, Options{Only: []string{"RecordCollectionFields"}, Types: []reflect.Type{reflect.TypeFor[RecordCollectionFields]()}})
	if err != nil {
		t.Fatal(err)
	}
	ks := kvo.NewSchema()
	entity := kvo.NewEntityType(ks, "row")
	kvo.NewProp(ks, 1, "data", kvo.TBytes, nil)
	kvo.NewProp(ks, 2, "counts", kvo.ListOf(kvo.TInt64), nil)
	kvo.NewProp(ks, 3, "labels", kvo.MapOf(kvo.TMachineString, kvo.TMachineString), nil)
	kvo.NewProp(ks, 4, "extra", kvo.TJSON, nil)
	model := kvo.NewModel(ks, entity, func(b *kvo.ModelBuilder) {
		for i := uint64(1); i <= 4; i++ {
			b.Prop(ks.MustPropByCode(i))
		}
	})
	edit := kvo.EditEncoded(row2.Edit(row2.Empty(row2.Compact2))).WithModel(model)
	db := evm.Package{Path: "db", Records: []*evm.RecordType{evm.NewRecordType("Row", model, true)}}
	db.Functions = []evm.ExternalFunction{
		{Name: "Save", Signature: evm.Signature{}, Call: func(c *evm.VM) error { return c.FlushRecord(edit) }},
		{Name: "Flush", Signature: evm.Signature{Params: []*values.Type{db.Record(model, true).Pointer}}, Call: func(c *evm.VM) error { return c.FlushMutableRecord(c.CallArgument(0), model) }},
	}
	pkg := schema.Package()
	mod, err := evmcompiler.Compile("record.go", `import("db";"host/fields")
 func Write(r *db.Row){
 p:=fields.RecordCollectionFields{Data:[]byte{1,2,255},Counts:[]int64{4,-7},Labels:map[string]string{"a":"first"}}
 r.data=p.Data;r.counts=p.Counts;r.labels=p.Labels
 r.extra=map[string]any{"items":[]any{int64(-7),map[string]any{"text":"saved"}}}
 r.data[1]=9;r.counts[0]=12;r.labels["b"]="second"
 db.Save()
 }
 func Repeat(a,b *db.Row){a.labels["shared"]="yes";if b.labels["shared"]!="yes"{panic("editor alias changed")}}
 func Shared(r *db.Row){if r.labels["other"]!="shared"{panic("cross-program map alias lost")};r.labels["a"]="first"}
 func Change(r *db.Row){r.labels["c"]="third";r.counts[0]++;r.extra.(map[string]any)["later"]=int64(23)}
 func Read(r *db.Row)bool{return r.extra.(map[string]any)["items"].([]any)[0].(int64)==-7 && r.extra.(map[string]any)["items"].([]any)[1].(map[string]any)["text"].(string)=="saved" && len(r.data)==3 && r.data[1]==9 && r.data[2]==255 && r.counts[0]==13 && r.counts[1]==-7 && r.labels["a"]=="first" && r.labels["b"]=="second" && r.labels["c"]=="third" && r.extra.(map[string]any)["later"].(int64)==23}
 `, pkg, db)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(mod, pkg, db)
	if err != nil {
		t.Fatal(err)
	}
	vm := evmtest.New(program)
	ctx, scope := evm.WithRecords(context.Background())
	held := scope.EditRecord(model, edit)
	if err := vm.Run(ctx, "Write", []values.Value{held}, nil); err != nil {
		t.Fatal(err)
	}
	if err := vm.Run(ctx, "Repeat", []values.Value{held, scope.EditRecord(model, edit)}, nil); err != nil {
		t.Fatal(err)
	}
	scope.Close()
	if err := vm.Run(context.Background(), "Change", []values.Value{held}, nil); err != nil {
		t.Fatal(err)
	}
	otherModule, err := evmcompiler.Compile("other.go", `import("db";"host/fields");var anchor fields.RecordCollectionFields
func Update(r *db.Row){if r.extra.(map[string]any)["items"].([]any)[1].(map[string]any)["text"].(string)!="saved"{panic("foreign interface value")};r.labels["other"]="shared";r.counts[0]=13;db.Flush(r)}`, pkg, db)
	if err != nil {
		t.Fatal(err)
	}
	otherProgram, err := evm.Link(otherModule, pkg, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := evmtest.New(otherProgram).Run(context.Background(), "Update", []values.Value{held}, nil); err != nil {
		t.Fatal(err)
	}
	if err := vm.Run(context.Background(), "Shared", []values.Value{held}, nil); err != nil {
		t.Fatal(err)
	}
	flushModule, err := evmcompiler.Compile("flush.go", `import "db";func Flush(r *db.Row){db.Flush(r)}`, db)
	if err != nil {
		t.Fatal(err)
	}
	flushProgram, err := evm.Link(flushModule, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := evmtest.New(flushProgram).Run(context.Background(), "Flush", []values.Value{held}, nil); err != nil {
		t.Fatal(err)
	}
	ctx, scope = evm.WithRecords(context.Background())
	defer scope.Close()
	var out [1]values.Value
	if err := vm.Run(ctx, "Read", []values.Value{scope.EditRecord(model, edit)}, out[:]); err != nil {
		t.Fatal(err)
	}
	if out[0].R != 1 {
		t.Fatal("record collections did not round-trip")
	}
}

type RecordCollectionFields struct {
	Extra  any
	Data   []byte
	Counts []int64
	Labels map[string]string
}

func TestNativeCollectionsRejectFunctionElements(t *testing.T) {
	for _, typ := range []string{"[]func()", "map[string]func()"} {
		t.Run(typ, func(t *testing.T) {
			files := fstest.MapFS{"api.go": {Data: []byte("package host; func Use(v " + typ + ") {}")}}
			_, err := New("host", files, Options{})
			if err == nil || !strings.Contains(err.Error(), "unsupported native collection") {
				t.Fatalf("expected unsupported native collection, got %v", err)
			}
		})
	}
}

func TestNativeAnyPreservesTypesAndAliases(t *testing.T) {
	files := fstest.MapFS{"types.go": {Data: []byte(`package native; type CollectionItem struct { Title string }`)}}
	schema, err := New("native", files, Options{Selective: true, Types: []reflect.Type{reflect.TypeFor[CollectionItem]()}})
	if err != nil {
		t.Fatal(err)
	}
	var nativeSeen bool
	bridge := evm.Package{Path: "bridge", Functions: []evm.ExternalFunction{{Name: "Echo", Signature: evm.Signature{Params: []*values.Type{values.AnyType}, Results: []*values.Type{values.AnyType}}, Call: func(c *evm.VM) error {
		x, err := c.NativeAny(c.CallArgument(0))
		if err != nil {
			return err
		}
		if p, ok := x.(*CollectionItem); ok && p != nil {
			p.Title = "native write"
			nativeSeen = true
		}
		v, err := c.AnyValue(x)
		if err != nil {
			return err
		}
		c.SetCallResult(0, v)
		return nil
	}}}}
	pkg := schema.Package()
	module, err := evmcompiler.Compile("any.go", `import("native";"bridge")
 type Local struct{ N int }
 func Run()bool{
  p:=&native.CollectionItem{Title:"before"}
  if bridge.Echo(p).(*native.CollectionItem)!=p || p.Title!="native write" {return false}
  var nilp *native.CollectionItem
  if bridge.Echo(nilp).(*native.CollectionItem)!=nil {return false}
  q:=&Local{N:3};if bridge.Echo(q).(*Local)!=q {return false}
  if bridge.Echo(int16(-7)).(int16)!=-7 || bridge.Echo("text").(string)!="text" {return false}
  m:=map[string]int{"a":1};bridge.Echo(m).(map[string]int)["b"]=2
  return m["b"]==2 && bridge.Echo(nil)==nil
 }`, pkg, bridge)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, pkg, bridge)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evmtest.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if out[0].R != 1 || !nativeSeen {
		t.Fatal("any conversion lost type or alias identity")
	}
}

type NativeAnyA struct {
	Value any
	Names []string
}
type NativeAnyZ struct {
	Value any
	Names []string
}

func TestNativeAnyAcrossPackages(t *testing.T) {
	makeSchema := func(path, name string, typ reflect.Type) *Schema {
		t.Helper()
		s, err := New(path, fstest.MapFS{"api.go": {Data: []byte("package native; type " + name + " struct { Value any; Names []string }")}}, Options{Selective: true, Types: []reflect.Type{typ}})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	a := makeSchema("a", "NativeAnyA", reflect.TypeFor[NativeAnyA]())
	z := makeSchema("z", "NativeAnyZ", reflect.TypeFor[NativeAnyZ]())
	bridge := evm.Package{Path: "bridge", Functions: []evm.ExternalFunction{{Name: "Echo", Signature: evm.Signature{Params: []*values.Type{values.AnyType}, Results: []*values.Type{values.AnyType}}, Call: func(c *evm.VM) error {
		x, err := c.NativeAny(c.CallArgument(0))
		if err != nil {
			return err
		}
		v, err := c.AnyValue(x)
		if err != nil {
			return err
		}
		c.SetCallResult(0, v)
		return nil
	}}}}
	for _, pkgs := range [][]evm.Package{{a.Package(), z.Package(), bridge}, {z.Package(), a.Package(), bridge}} {
		module, err := evmcompiler.Compile("cross.go", `import("a";"z";"bridge")
 func Run()bool{
  names:=[]string{"first"}
  x:=a.NativeAnyA{Value:names,Names:names};y:=z.NativeAnyZ{Value:x.Value,Names:names}
  result:=bridge.Echo(y.Value).([]string);result[0]="changed"
  return x.Names[0]=="changed" && y.Names[0]=="changed"
 }`, pkgs...)
		if err != nil {
			t.Fatal(err)
		}
		p, err := evm.Link(module, pkgs...)
		if err != nil {
			t.Fatal(err)
		}
		var out [1]values.Value
		if err := evmtest.New(p).Run(context.Background(), "Run", nil, out[:]); err != nil {
			t.Fatal(err)
		}
		if out[0].R != 1 {
			t.Fatal("cross-package alias lost")
		}
	}
}

func TestNativeAnyRejectsAnotherProgram(t *testing.T) {
	var retained any
	pkg := evm.Package{Path: "bridge", Functions: []evm.ExternalFunction{
		{Name: "Save", Signature: evm.Signature{Params: []*values.Type{values.AnyType}}, Call: func(c *evm.VM) error { var err error; retained, err = c.NativeAny(c.CallArgument(0)); return err }},
		{Name: "Load", Signature: evm.Signature{Results: []*values.Type{values.AnyType}}, Call: func(c *evm.VM) error {
			v, err := c.AnyValue(retained)
			if err == nil {
				c.SetCallResult(0, v)
			}
			return err
		}},
	}}
	module, err := evmcompiler.Compile("owner.go", `import "bridge";type Local struct { N int };func Save(){bridge.Save(&Local{N:7})};func Load()any{return bridge.Load()}`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := evm.Link(module, pkg)
	if err != nil {
		t.Fatal(err)
	}
	other, err := evm.Link(module, pkg)
	if err != nil {
		t.Fatal(err)
	}
	if err := evmtest.New(owner).Run(context.Background(), "Save", nil, nil); err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evmtest.New(owner).Run(context.Background(), "Load", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if err := evmtest.New(other).Run(context.Background(), "Load", nil, out[:]); err == nil || !strings.Contains(err.Error(), "another program") {
		t.Fatalf("foreign handle: %v", err)
	}
}

func TestNativeAnyTimestamp(t *testing.T) {
	var want time.Time
	bridge := evm.Package{Path: "bridge", Functions: []evm.ExternalFunction{{Name: "Echo", Signature: evm.Signature{Params: []*values.Type{values.AnyType}, Results: []*values.Type{values.AnyType}}, Call: func(c *evm.VM) error {
		x, err := c.NativeAny(c.CallArgument(0))
		if err != nil {
			return err
		}
		got, ok := x.(time.Time)
		if !ok || got != want {
			t.Fatalf("native timestamp = %T %v, want %v", x, x, want)
		}
		v, err := c.AnyValue(got)
		if err != nil {
			return err
		}
		c.SetCallResult(0, v)
		return nil
	}}}}
	module, err := evmcompiler.Compile("timestamp.go", `import("time";"bridge");func Run(t time.Time)bool{return bridge.Echo(t).(time.Time)==t}`, bridge)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, bridge)
	if err != nil {
		t.Fatal(err)
	}
	for _, stamp := range []time.Time{{}, time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC), time.Unix(-1, 999999999), time.Date(2500, 1, 1, 0, 0, 0, 123, time.UTC), time.Now(), time.Date(2026, 11, 14, 18, 0, 0, 0, time.FixedZone("shop", 14400))} {
		want = stamp
		var out [1]values.Value
		if err := evmtest.New(program).Run(context.Background(), "Run", []values.Value{values.NativeTime(stamp)}, out[:]); err != nil {
			t.Fatal(err)
		}
		if out[0].R != 1 {
			t.Fatalf("timestamp round trip changed %v", stamp)
		}
	}
}

func TestValueCodecs(t *testing.T) {
	files := fstest.MapFS{"types.go": {Data: []byte(`package native
	type CollectionItem struct{Title string}
	type CollectionPanel struct {
		Items []*CollectionItem
		Named map[string]*CollectionItem
		Styles map[string]map[string]string
	}`)}}
	schema, err := New("native", files, Options{Only: []string{"CollectionPanel"}, Types: []reflect.Type{reflect.TypeFor[CollectionPanel](), reflect.TypeFor[CollectionItem]()}})
	if err != nil {
		t.Fatal(err)
	}
	panels, err := Codec[CollectionPanel](schema)
	if err != nil {
		t.Fatal(err)
	}
	items, err := Codec[*CollectionItem](schema)
	if err != nil {
		t.Fatal(err)
	}
	styles, err := Codec[map[string]map[string]string](schema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Codec[chan int](schema); err == nil {
		t.Fatal("unbound type accepted")
	}
	pkg := schema.Package()
	m, err := evmcompiler.Compile("echo.go", `import "native"; func Echo(x native.CollectionPanel) native.CollectionPanel{return x}`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, pkg)
	if err != nil {
		t.Fatal(err)
	}
	vm := p.AcquireVM(t.Context())
	defer vm.Release()
	fn, err := p.LookupFunction("Echo", &evm.Signature{Params: []*values.Type{panels.Type()}, Results: []*values.Type{panels.Type()}})
	if err != nil {
		t.Fatal(err)
	}
	item := &CollectionItem{Title: "VIP"}
	if n := testing.AllocsPerRun(100, func() {
		out, e := items.Unpack(items.Pack(item))
		if e != nil || out != item {
			t.Fatal(out, e)
		}
	}); n != 0 {
		t.Fatalf("pointer round trip: %v allocations", n)
	}
	if out, err := items.Unpack(items.Pack(nil)); err != nil || out != nil {
		t.Fatal(out, err)
	}
	input := CollectionPanel{Items: []*CollectionItem{item}, Named: map[string]*CollectionItem{"first": item}, Styles: map[string]map[string]string{"card": {"color": "blue"}}}
	var slots [2]values.Value
	slots[0], err = panels.PackIn(vm, input)
	if err != nil {
		t.Fatal(err)
	}
	if err = vm.Run(fn, slots[:]); err != nil {
		t.Fatal(err)
	}
	out, err := panels.UnpackIn(vm, slots[1])
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 1 || out.Items[0] != item || out.Named["first"] != item || out.Styles["card"]["color"] != "blue" {
		t.Fatalf("%+v", out)
	}
	v, err := styles.PackIn(vm, input.Styles)
	if err != nil {
		t.Fatal(err)
	}
	outStyles, err := styles.UnpackIn(vm, v)
	if err != nil {
		t.Fatal(err)
	}
	outStyles["card"]["color"] = "red"
	if input.Styles["card"]["color"] != "red" {
		t.Fatal("native map lost identity")
	}
	vm.SetBudget(evm.Limits{AllocationBytes: 1})
	if _, err := panels.PackIn(vm, input); err == nil {
		t.Fatal("struct packing ignored allocation limit")
	}
}
