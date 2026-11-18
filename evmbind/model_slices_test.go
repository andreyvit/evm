package evmbind_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/modeldata"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/values"
)

type modelSliceHost struct {
	matrix [][]*BoundModel
	rows   []*BoundModel
	others []*modeldata.Alternate
}

func (h *modelSliceHost) Rows(*evm.Call) []*BoundModel                       { return h.rows }
func (h *modelSliceHost) Same(_ *evm.Call, row *BoundModel) bool             { return row == h.rows[0] }
func (h *modelSliceHost) Echo(_ *evm.Call, rows []*BoundModel) []*BoundModel { return rows }

func TestNativeModelSliceSignatures(t *testing.T) {
	models, dep, err := modeldata.Declarations()
	if err != nil {
		t.Fatal(err)
	}
	models.Records[0].NativeAllocation = reflect.TypeFor[BoundModel]()
	source := fstest.MapFS{"api.go": {Data: []byte(`package host
 import model "native/model"
 func Rows() []*model.BoundModel
 func Same(row *model.BoundModel) bool
 func Echo(rows []*model.BoundModel) []*model.BoundModel
 `)}}
	schema, err := evmbind.New("host", source, evmbind.Options{Host: reflect.TypeFor[*modelSliceHost](), Imports: map[string]*evmbind.Schema{"native/model": dep}})
	if err != nil {
		t.Fatal(err)
	}
	first := &BoundModel{Name: "first", Hidden: 42}
	host := &modelSliceHost{rows: make([]*BoundModel, 2, 4)}
	host.rows[0] = first
	pkgs := []evm.Package{models, dep.Package(), schema.Bind(host)}
	if _, err := evmcompiler.Compile("hostonly.go", `import "host";func Run()string{return host.Rows()[0].name}`, pkgs...); err != nil {
		t.Fatal(err)
	}
	module, err := evmcompiler.Compile("slices.go", `import("db";"host")
 func Run() bool {
  rows:=host.Rows()
  if len(rows)!=2 || cap(rows)!=4 || rows[0].name!="first" || rows[1]!=nil || !host.Same(rows[0]) {return false}
  rows[1]=rows[0]
  echoed:=host.Echo(rows)
  if !host.Same(echoed[1]) {return false}
  rows=append(rows,rows[0])
  if len(rows)!=3 || !host.Same(rows[2]) {return false}
  fresh:=[]*db.Customer{rows[0],nil}
  fresh=host.Echo(fresh)
  if !host.Same(fresh[0]) || fresh[1]!=nil {return false}
  var empty []*db.Customer
  return host.Echo(empty)==nil && host.Echo(make([]*db.Customer,0))!=nil
 }
 `, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(module, pkgs...)
	if err != nil {
		t.Fatalf("%v\n%s", err, module.Disassemble())
	}
	for _, decl := range pkgs[2].Types {
		if decl.NativeSlice != nil && decl.NativeSlice.Element.Codec != nil {
			codec := decl.NativeSlice.Element.Codec
			decode := codec.Decode
			codec.Decode = nil
			defer func() { codec.Decode = decode }()
			break
		}
	}
	var out [1]values.Value
	if err = evm.New(p).Run(context.Background(), "Run", nil, out[:]); err != nil || !out[0].AsBool() {
		t.Fatalf("%v %v\n%s", out, err, module.Disassemble())
	}
	if host.rows[1] != first || host.rows[:3][2] != first || first.Hidden != 42 {
		t.Fatal("lost native slice storage or row identity")
	}
	if _, err := evmcompiler.Compile("readonly.go", `import "host";func Run(){host.Rows()[0].name="changed"}`, pkgs...); err == nil {
		t.Fatal("model row became mutable")
	}
}

func (h *modelSliceHost) First(*evm.Call) []*BoundModel           { return h.rows }
func (h *modelSliceHost) Second(*evm.Call) []*modeldata.Alternate { return h.others }

func TestNativeModelSliceMultipleLayouts(t *testing.T) {
	models, dep, err := modeldata.Declarations()
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{"api.go": {Data: []byte(`package host
 import model "native/model"
 func First() []*model.BoundModel
 func Second() []*model.Alternate
 `)}}
	schema, err := evmbind.New("host", files, evmbind.Options{Host: reflect.TypeFor[*modelSliceHost](), Imports: map[string]*evmbind.Schema{"native/model": dep}})
	if err != nil {
		t.Fatal(err)
	}
	host := &modelSliceHost{rows: []*BoundModel{{Name: "first"}}, others: []*modeldata.Alternate{{Label: "second"}}}
	pkgs := []evm.Package{models, dep.Package(), schema.Bind(host)}
	module, err := evmcompiler.Compile("layouts.go", `import("host";"db")
func Run()bool{
 a,b:=host.First(),host.Second()
 if a[0].name!="first" || b[0].name!="second" {return false}
 var selected []*db.Customer=a
 selected=b
 slot:=&selected[0]
 return b[0].name=="second" && a[0].name=="first" && &b[0]==slot
}
func Reject(){
 a,b:=host.First(),host.Second()
 b[0]=a[0]
}`, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil || !out[0].AsBool() {
		t.Fatal(out, err)
	}
	if err := evm.New(program).Run(context.Background(), "Reject", nil, nil); err == nil || !strings.Contains(err.Error(), "explicit conversion") {
		t.Fatalf("expected explicit conversion error, got %v", err)
	}
	if host.others[0].Label != "second" || host.rows[0].Name != "first" {
		t.Fatal("rejected assignment changed native rows")
	}
}

func TestSharedNativeModelSliceSchemas(t *testing.T) {
	models, dep, err := modeldata.Declarations()
	if err != nil {
		t.Fatal(err)
	}
	source := fstest.MapFS{"api.go": {Data: []byte(`package host
 import model "native/model"
 func Rows() []*model.BoundModel
 `)}}
	for i := 0; i < 8; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			schema, err := evmbind.New("host", source, evmbind.Options{Host: reflect.TypeFor[*modelSliceHost](), Imports: map[string]*evmbind.Schema{"native/model": dep}})
			if err != nil {
				t.Fatal(err)
			}
			host := &modelSliceHost{rows: []*BoundModel{{Name: "shared"}}}
			pkgs := []evm.Package{models, dep.Package(), schema.Bind(host)}
			module, err := evmcompiler.Compile("shared.go", `import "host";func Run()string{return host.Rows()[0].name}`, pkgs...)
			if err != nil {
				t.Fatal(err)
			}
			program, err := evm.Link(module, pkgs...)
			if err != nil {
				t.Fatal(err)
			}
			var out [1]values.Value
			if err := evm.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil || out[0].AsString() != "shared" {
				t.Fatal(out, err)
			}
		})
	}
}

type modelSliceExtra struct {
	Name  string `kvo:"name"`
	Extra string `kvo:"-"`
}
type modelSliceRoot struct {
	Children []*modelSliceExtra `kvo:"children"`
}

func TestNativeModelSliceDoesNotChangeRecordLists(t *testing.T) {
	schema := kvo.NewSchema()
	childType := kvo.NewEntityType(schema, "Customer", reflect.TypeFor[BoundModel](), reflect.TypeFor[modelSliceExtra]())
	rootType := kvo.NewEntityType(schema, "Root", reflect.TypeFor[modelSliceRoot]())
	name := kvo.NewStringProp[string](schema, 1, "name", kvo.TMachineString)
	children := kvo.NewRecordListProp(schema, 3, "children", kvo.ListOf(childType))
	child := kvo.NewModel(schema, childType, func(b *kvo.ModelBuilder) { b.Prop(name) })
	root := kvo.NewModel(schema, rootType, func(b *kvo.ModelBuilder) { b.Prop(children) })
	models, err := evm.ModelPackage("db", schema)
	if err != nil {
		t.Fatal(err)
	}
	dep, err := evmbind.NewTypes("model", modeldata.Source, []string{"BoundModel"}, evmbind.Options{Models: &models, NativePackage: reflect.TypeFor[BoundModel]().PkgPath()})
	if err != nil {
		t.Fatal(err)
	}
	src := fstest.MapFS{"api.go": {Data: []byte(`package host
 import model "native/model"
 func Rows() []*model.BoundModel
 `)}}
	host, err := evmbind.New("host", src, evmbind.Options{Host: reflect.TypeFor[*modelSliceHost](), Imports: map[string]*evmbind.Schema{"native/model": dep}})
	if err != nil {
		t.Fatal(err)
	}
	original := &modelSliceRoot{Children: []*modelSliceExtra{{Name: "first", Extra: "must survive"}}}
	binding := kvo.Bind(reflect.TypeFor[modelSliceRoot](), root)
	probe := evm.Package{Path: "probe", Functions: []evm.ExternalFunction{{Name: "Same", Signature: evm.Signature{Params: []*values.Type{models.Record(child, false).Pointer}, Results: []*values.Type{values.Boolean}}, Call: func(c *evm.Call) error {
		row, err := c.Record(c.Argument(0), child)
		if err != nil {
			return err
		}
		c.SetResult(0, values.Bool(row.NativePointer() == original.Children[0]))
		return nil
	}}}}
	for _, linked := range []bool{false, true} {
		t.Run(fmt.Sprint(linked), func(t *testing.T) {
			pkgs := []evm.Package{models, dep.Package(), probe}
			if linked {
				pkgs = append(pkgs, host.Bind(&modelSliceHost{}))
			}
			module, err := evmcompiler.Compile("preserve.go", `import("db";"probe");func Run(row *db.Root)bool{return probe.Same(row.children[0])}`, pkgs...)
			if err != nil {
				t.Fatal(err)
			}
			program, err := evm.Link(module, pkgs...)
			if err != nil {
				t.Fatal(err)
			}
			var out [1]values.Value
			if err := evm.New(program).Run(context.Background(), "Run", []values.Value{binding.Record(reflect.ValueOf(original)).Value()}, out[:]); err != nil {
				t.Fatal(err)
			}
			if !out[0].AsBool() {
				t.Fatal("linking a native slice declaration changed the existing record-list element")
			}
		})
	}
}

// A host can retain an ordinary slice argument and mutate it in a later call.
// The guest's original slice must keep sharing those slots.
func (h *modelSliceHost) Retain(_ *evm.Call, rows []*BoundModel) { h.rows = rows }
func (h *modelSliceHost) Replace(_ *evm.Call)                    { h.rows[0] = nil }

func TestNativeModelSliceRetainedBacking(t *testing.T) {
	models, dep, err := modeldata.Declarations()
	if err != nil {
		t.Fatal(err)
	}
	models.Records[0].NativeAllocation = reflect.TypeFor[BoundModel]()
	source := fstest.MapFS{"api.go": {Data: []byte(`package host
 import model "native/model"
 func First() []*model.BoundModel
 func Retain(rows []*model.BoundModel)
 func Replace()
 `)}}
	schema, err := evmbind.New("host", source, evmbind.Options{Host: reflect.TypeFor[*modelSliceHost](), Imports: map[string]*evmbind.Schema{"native/model": dep}})
	if err != nil {
		t.Fatal(err)
	}
	host := &modelSliceHost{rows: []*BoundModel{{Name: "first"}}}
	pkgs := []evm.Package{models, dep.Package(), schema.Bind(host)}
	module, err := evmcompiler.Compile("retained.go", `import("host";"db")
 func Run()bool {
  first:=host.First()[0]
  rows:=[]*db.Customer{first}
  slot:=&rows[0]
  host.Retain(rows)
  host.Replace()
  if rows[0]!=nil || *slot!=nil {return false}
  size:=len(rows)
  rows=make([]*db.Customer,size,size+2)
  rows[0]=first
  host.Retain(rows)
  host.Replace()
  if rows[0]!=nil {return false}
  rows=nil
  rows=append(rows,first)
  host.Retain(rows)
  host.Replace()
  return rows[0]==nil
 }`, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(module, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(p).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatalf("%v\n%s", err, module.Disassemble())
	}
	if !out[0].AsBool() {
		t.Fatal("retained native slice no longer aliases guest backing")
	}
}

type ModelSliceBox struct{ Rows []*BoundModel }
type AlternateModelSliceBox struct{ Rows []*modeldata.Alternate }

func (h *modelSliceHost) OtherBox(*evm.Call) *AlternateModelSliceBox {
	return &AlternateModelSliceBox{Rows: h.others}
}

func (h *modelSliceHost) Box(*evm.Call) *ModelSliceBox { return &ModelSliceBox{Rows: h.rows} }

func TestNativeModelSliceField(t *testing.T) {
	models, dep, err := modeldata.Declarations()
	if err != nil {
		t.Fatal(err)
	}
	source := fstest.MapFS{"api.go": {Data: []byte(`package host
 import model "native/model"
 type ModelSliceBox struct { Rows []*model.BoundModel }
 type AlternateModelSliceBox struct { Rows []*model.Alternate }
 func Box() *ModelSliceBox
 func OtherBox() *AlternateModelSliceBox
 `)}}
	schema, err := evmbind.New("host", source, evmbind.Options{Host: reflect.TypeFor[*modelSliceHost](), Imports: map[string]*evmbind.Schema{"native/model": dep}})
	if err != nil {
		t.Fatal(err)
	}
	host := &modelSliceHost{rows: []*BoundModel{{Name: "first"}}, others: []*modeldata.Alternate{{Label: "second"}}}
	pkgs := []evm.Package{models, dep.Package(), schema.Bind(host)}
	module, err := evmcompiler.Compile("field.go", `import "host"
 func Run()bool {
 box:=host.Box();rows:=box.Rows
 if len(rows)!=1 || rows[0].name!="first" {return false}
 other:=host.OtherBox();otherRows:=other.Rows
 if len(otherRows)!=1 || otherRows[0].name!="second" {return false}
 otherSlot:=&other.Rows
 (*otherSlot)[0]=nil
 if otherRows[0]!=nil {return false}
 other.Rows=otherRows[:1]
 box.Rows=rows[:1]
 slot:=&box.Rows
 if len(*slot)!=1 || (*slot)[0]!=rows[0] {return false}
 (*slot)[0]=nil
 if rows[0]!=nil {return false}
 *slot=nil
 return box.Rows==nil
 }`, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(module, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(p).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if host.others[0] != nil {
		t.Fatal("wrong native field row layout")
	}
	if !out[0].AsBool() {
		t.Fatal("native model slice field lost its storage layout")
	}
}

func (h *modelSliceHost) Matrix(*evm.Call) [][]*BoundModel                             { return h.matrix }
func (h *modelSliceHost) EchoMatrix(_ *evm.Call, rows [][]*BoundModel) [][]*BoundModel { return rows }

func TestNativeNestedModelSlices(t *testing.T) {
	models, dep, err := modeldata.Declarations()
	if err != nil {
		t.Fatal(err)
	}
	source := fstest.MapFS{"api.go": {Data: []byte(`package host
 import model "native/model"
 func Matrix() [][]*model.BoundModel
 func EchoMatrix(rows [][]*model.BoundModel) [][]*model.BoundModel
 `)}}
	schema, err := evmbind.New("host", source, evmbind.Options{Host: reflect.TypeFor[*modelSliceHost](), Imports: map[string]*evmbind.Schema{"native/model": dep}})
	if err != nil {
		t.Fatal(err)
	}
	row := &BoundModel{Name: "first"}
	host := &modelSliceHost{matrix: [][]*BoundModel{{row}, nil}}
	pkgs := []evm.Package{models, dep.Package(), schema.Bind(host)}
	module, err := evmcompiler.Compile("nested.go", `import("host";"db")
 func Run()bool{
  rows:=host.Matrix()
  if len(rows)!=2 || cap(rows)!=2 || len(rows[0])!=1 || rows[0][0].name!="first" || rows[1]!=nil {return false}
  echoed:=host.EchoMatrix(rows)
  echoed[1]=rows[0]
  if len(rows[1])!=1 {return false}
  copy(rows[0:1],rows[1:2])
  rows=append(rows,rows[0])
  if len(rows)!=3 || len(rows[2])!=1 {return false}
  guest:=make([][]*db.Customer,3)
  copy(guest,rows)
  guest[2][0]=nil
  return rows[0][0]==nil && echoed[1][0]==nil
 }`, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(module, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(p).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if !out[0].AsBool() || host.matrix[0][0] != nil || host.matrix[1][0] != nil {
		t.Fatal("nested native slice lost backing or layout")
	}
}

func (h *modelSliceHost) Groups(*evm.Call) map[string][]*BoundModel {
	return map[string][]*BoundModel{"first": h.rows}
}

func TestNativeModelSliceMap(t *testing.T) {
	models, dep, err := modeldata.Declarations()
	if err != nil {
		t.Fatal(err)
	}
	source := fstest.MapFS{"api.go": {Data: []byte(`package host
 import model "native/model"
 func Groups() map[string][]*model.BoundModel
 `)}}
	schema, err := evmbind.New("host", source, evmbind.Options{Host: reflect.TypeFor[*modelSliceHost](), Imports: map[string]*evmbind.Schema{"native/model": dep}})
	if err != nil {
		t.Fatal(err)
	}
	host := &modelSliceHost{rows: []*BoundModel{{Name: "first"}}}
	pkgs := []evm.Package{models, dep.Package(), schema.Bind(host)}
	module, err := evmcompiler.Compile("groups.go", `import "host"
 func Run()bool{
  groups:=host.Groups()
  rows,ok:=groups["first"]
  if !ok || len(rows)!=1 || rows[0].name!="first" {return false}
  groups["second"]=rows
  count:=0
  for key,rows:=range groups {if key=="" || len(rows)!=1 {return false};rows[0]=nil;count++}
  return count==2 && groups["first"][0]==nil
 }
 func JSON()any{return host.Groups()}
`, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(module, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(p).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if !out[0].AsBool() || host.rows[0] != nil {
		t.Fatal("native map lost model slice backing")
	}
	host.rows = host.rows[:0]
	if err := evm.New(p).Run(context.Background(), "JSON", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	encoded, err := p.EncodeJSON(context.Background(), out[0], 1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"first":[]}` {
		t.Fatalf("map JSON: %s", encoded)
	}

}

type NamedModelRows []*BoundModel

func (h *modelSliceHost) NamedRows(*evm.Call) NamedModelRows { return NamedModelRows(h.rows) }
func (h *modelSliceHost) NamedEcho(_ *evm.Call, rows NamedModelRows) NamedModelRows {
	if &rows[0] != &h.rows[0] {
		panic("named argument copied backing")
	}
	return rows
}
func (h *modelSliceHost) NamedIs(_ *evm.Call, x any) bool { _, ok := x.(NamedModelRows); return ok }

func TestNativeNamedModelSlice(t *testing.T) {
	models, dep, err := modeldata.Declarations()
	if err != nil {
		t.Fatal(err)
	}
	source := fstest.MapFS{"api.go": {Data: []byte(`package host
 import model "native/model"
 type NamedModelRows []*model.BoundModel
 func NamedRows() NamedModelRows
 func NamedEcho(rows NamedModelRows) NamedModelRows
 func NamedIs(value any)bool
 `)}}
	schema, err := evmbind.New("host", source, evmbind.Options{Host: reflect.TypeFor[*modelSliceHost](), Imports: map[string]*evmbind.Schema{"native/model": dep}})
	if err != nil {
		t.Fatal(err)
	}
	host := &modelSliceHost{rows: []*BoundModel{{Name: "first"}}}
	pkgs := []evm.Package{models, dep.Package(), schema.Bind(host)}
	module, err := evmcompiler.Compile("named.go", `import("host";"db")
 func Run()bool{
  rows:=host.NamedRows()
  if !host.NamedIs(any(rows)){return false}
  array:=(*[1]*db.Customer)(rows)
  plain:=array[:]
  named:=host.NamedModelRows(plain)
  echoed:=host.NamedEcho(named)
  if !host.NamedIs(any(named)) || !host.NamedIs(any(echoed)){return false}
  echoed[0]=nil
  return rows[0]==nil
 }`, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(module, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(p).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if !out[0].AsBool() || host.rows[0] != nil {
		t.Fatal("named model slice lost type or backing")
	}
}

func (h *modelSliceHost) NilAlternateRows(*evm.Call) []*modeldata.Alternate { return nil }
func (h *modelSliceHost) OtherMatrix(*evm.Call) [][]*modeldata.Alternate {
	return [][]*modeldata.Alternate{nil}
}

func TestNativeModelSliceCrossLayoutNil(t *testing.T) {
	models, dep, err := modeldata.Declarations()
	if err != nil {
		t.Fatal(err)
	}
	source := fstest.MapFS{"api.go": {Data: []byte(`package host
 import model "native/model"
 func NilAlternateRows() []*model.Alternate
 func Echo(rows []*model.BoundModel) []*model.BoundModel
 func Matrix() [][]*model.BoundModel
 func OtherMatrix() [][]*model.Alternate
 func Groups() map[string][]*model.BoundModel
 `)}}
	schema, err := evmbind.New("host", source, evmbind.Options{Host: reflect.TypeFor[*modelSliceHost](), Imports: map[string]*evmbind.Schema{"native/model": dep}})
	if err != nil {
		t.Fatal(err)
	}
	row := &BoundModel{Name: "first"}
	host := &modelSliceHost{rows: []*BoundModel{row}, matrix: [][]*BoundModel{{row}, {row}}}
	pkgs := []evm.Package{models, dep.Package(), schema.Bind(host)}
	module, err := evmcompiler.Compile("nil_layouts.go", `import "host"
 func Run()bool{
  empty:=host.NilAlternateRows()
  if host.Echo(empty)!=nil {return false}
  matrix,other:=host.Matrix(),host.OtherMatrix()
  matrix[0]=empty
  copy(matrix[1:2],other)
  groups:=host.Groups();groups["nil"]=empty
  return matrix[0]==nil && matrix[1]==nil && groups["nil"]==nil
 }`, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(module, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(p).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if !out[0].AsBool() || host.matrix[0] != nil || host.matrix[1] != nil {
		t.Fatal("cross-layout nil became non-nil")
	}
}

func TestModelAllocationWithoutSliceBindings(t *testing.T) {
	models, _, err := modeldata.Declarations()
	if err != nil {
		t.Fatal(err)
	}
	models.Records[0].NativeAllocation = reflect.TypeFor[BoundModel]()
	var retained []*BoundModel
	host := evm.Package{Path: "host", Functions: []evm.ExternalFunction{
		{Name: "Retain", Signature: evm.Signature{Params: []*values.Type{values.AnyType}}, Call: func(c *evm.Call) error {
			value, err := c.NativeAny(c.Argument(0))
			if err != nil {
				return err
			}
			var ok bool
			retained, ok = value.([]*BoundModel)
			if !ok {
				return fmt.Errorf("unexpected native type %T", value)
			}
			return nil
		}},
		{Name: "Replace", Signature: evm.Signature{}, Call: func(c *evm.Call) error { retained[0] = &BoundModel{Name: "changed"}; return nil }},
	}}
	module, err := evmcompiler.Compile("allocation.go", `import("db";"host")
 func Run()bool {
  rows:=[]*db.Customer{nil}
  host.Retain(rows);host.Replace()
  if rows[0].name!="changed" {return false}
  size:=len(rows)
  rows=make([]*db.Customer,size,size+1)
  host.Retain(rows);host.Replace()
  matrix:=make([][2]*db.Customer,1)
  matrix[0][1]=rows[0]
  alias:=matrix[0][:]
  if alias[1]!=rows[0] {return false}
  var nested [2][2]*db.Customer
  nested[1][0]=rows[0]
  copied:=nested[1]
  return copied[0].name=="changed" && matrix[0][1]==rows[0]
 }`, models, host)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, models, host)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if !out[0].AsBool() {
		t.Fatal("guest allocation lost native backing")
	}
}

type modelValueSliceHost struct{ rows []BoundModel }

func (h *modelValueSliceHost) Values(*evm.Call) []BoundModel                          { return h.rows }
func (h *modelValueSliceHost) EchoValues(_ *evm.Call, rows []BoundModel) []BoundModel { return rows }
func (h *modelValueSliceHost) ChangeValue(*evm.Call)                                  { h.rows[0].Name = "changed" }

func TestNativeModelValueSlices(t *testing.T) {
	models, dep, err := modeldata.Declarations()
	if err != nil {
		t.Fatal(err)
	}
	source := fstest.MapFS{"api.go": {Data: []byte(`package host
 import model "native/model"
 func Values() []model.BoundModel
 func EchoValues(rows []model.BoundModel) []model.BoundModel
 func ChangeValue()
 `)}}
	schema, err := evmbind.New("host", source, evmbind.Options{Host: reflect.TypeFor[*modelValueSliceHost](), Imports: map[string]*evmbind.Schema{"native/model": dep}})
	if err != nil {
		t.Fatal(err)
	}
	host := &modelValueSliceHost{rows: make([]BoundModel, 2, 4)}
	host.rows[0] = BoundModel{Name: "first", Hidden: 42}
	pkgs := []evm.Package{models, dep.Package(), schema.Bind(host)}
	module, err := evmcompiler.Compile("values.go", `import("db";"host")
 func Run() {
 rows:=host.Values()
 saved:=rows[0]
 address:=&rows[0]
 host.ChangeValue()
 if saved.name!="first" || address.name!="changed"{panic("value copy or address")}
 rows[1]=saved
 rows=append(rows,saved)
 if len(rows)!=3 {panic(len(rows))};if cap(rows)!=4{panic(cap(rows))}
 echoed:=host.EchoValues(rows)
 echoed[0]=saved
 fresh:=make([]db.Customer,1,2)
 fresh[0]=saved
 fresh=host.EchoValues(fresh)
 if fresh[0].name!="first"{panic("guest backing")}
 var empty []db.Customer
 if host.EchoValues(empty)!=nil || host.EchoValues(make([]db.Customer,0))==nil{panic("nil/empty")}
 }`, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(program).Run(context.Background(), "Run", nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, row := range host.rows[:3] {
		if row.Name != "first" || row.Hidden != 42 {
			t.Fatal("lost shared backing or hidden fields", host.rows[:3])
		}
	}
}
