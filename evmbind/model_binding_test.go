package evmbind_test

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/modeldata"
	"github.com/andreyvit/evm/evmbind/internal/modelhost"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/values"
)

type BoundModel = modeldata.BoundModel

type modelHost struct {
	row  *BoundModel
	rows []*BoundModel
}

func (h *modelHost) Get(c *evm.Call) *BoundModel                 { return h.row }
func (h *modelHost) Echo(c *evm.Call, r *BoundModel) *BoundModel { return r }
func (h *modelHost) Other(*evm.Call) *modeldata.Alternate {
	return &modeldata.Alternate{Label: "other"}
}
func (h *modelHost) Copy(c *evm.Call, r BoundModel) BoundModel { r.Name = "copy"; return r }
func (h *modelHost) Same(c *evm.Call, r *BoundModel) bool      { return r == h.row }

func TestNativeModelSignatures(t *testing.T) {
	schema := kvo.NewSchema()
	prop := kvo.NewStringProp[string](schema, 1, "name", kvo.TMachineString)
	kvo.NewModel(schema, kvo.NewEntityType(schema, "Customer", reflect.TypeFor[BoundModel]()), func(b *kvo.ModelBuilder) { b.Prop(prop) })
	models, err := evm.ModelPackage("db", schema)
	if err != nil {
		t.Fatal(err)
	}
	models.Records[0].NativeAllocation = reflect.TypeFor[BoundModel]()
	nativePath := reflect.TypeFor[BoundModel]().PkgPath()
	dep, err := evmbind.NewTypes("model", fstest.MapFS{"model.go": {Data: []byte(`package model
 import "unavailable/dependency"
 type BoundModel dependency.Unknown
 `)}}, nil, evmbind.Options{Models: &models, NativePackage: nativePath})
	if err != nil {
		t.Fatal(err)
	}
	src := fstest.MapFS{"host.go": {Data: []byte(`package host
 import "native/model"
 func Get()*model.BoundModel
 func Echo(r *model.BoundModel)*model.BoundModel
 func Copy(r model.BoundModel)model.BoundModel
 func Same(r *model.BoundModel)bool
 `)}}
	host, err := evmbind.New("host", src, evmbind.Options{Imports: map[string]*evmbind.Schema{"native/model": dep}, Host: reflect.TypeFor[*modelHost]()})
	if err != nil {
		t.Fatal(err)
	}
	pkg := host.Package()
	instance := &modelHost{row: &BoundModel{Name: "original", Hidden: 42}}
	pkg = host.Bind(instance)
	pkgs := []evm.Package{models, dep.Package(), pkg}
	m, err := evmcompiler.Compile("model.go", `import("db";"host")
 func Run()bool {
  r:=host.Get();var typed *db.Customer=r
  if typed.name!="original" || !host.Same(r) || host.Echo(r)!=r {return false}
  if host.Echo(nil)!=nil {return false}
  copy:=host.Copy(*r)
  return copy.name=="copy" && r.name=="original"
 }`, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err = evm.New(p).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if !out[0].AsBool() {
		t.Fatal("model handoff failed")
	}
	if instance.row.Hidden != 42 {
		t.Fatal("hidden field changed")
	}
	generated, err := evmbind.GenerateWithOptions("host", "example/host", "bindings", src, evmbind.Options{Imports: map[string]*evmbind.Schema{"native/model": dep}})
	if err != nil {
		t.Fatal(err)
	}
	if len(generated) == 0 {
		t.Fatal("missing generated wrappers")
	}
}

func TestModelPackageInference(t *testing.T) {
	models, _, err := modeldata.Declarations()
	if err != nil {
		t.Fatal(err)
	}
	dep, err := evmbind.NewTypes("consolidated", modeldata.Source, nil, evmbind.Options{Models: &models, Types: []reflect.Type{reflect.TypeFor[BoundModel]()}})
	if err != nil {
		t.Fatal(err)
	}
	if dep.Type("*BoundModel") != models.Records[0].Pointer || dep.Type("*Alternate") != models.Records[0].Pointer {
		t.Fatal("native model declarations did not share their row type")
	}
	empty, err := evmbind.NewTypes("consolidated", modeldata.Source, []string{}, evmbind.Options{Models: &models, NativePackage: reflect.TypeFor[BoundModel]().PkgPath()})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Type("BoundModel") != nil {
		t.Fatal("empty selection exposed model types")
	}
	if len(dep.Package().Types) != 0 {
		t.Fatal("native model structs were emitted as duplicate script declarations")
	}
	if _, err = evmbind.NewTypes("consolidated", modeldata.Source, nil, evmbind.Options{Models: &models}); err == nil {
		t.Fatal("missing native package identity was guessed")
	}
}

func (h *modelHost) Pair(_ *evm.Call, a, b *BoundModel) bool { return a == b }

func TestModelSelectionAmbiguity(t *testing.T) {
	schema := kvo.NewSchema()
	name := kvo.NewStringProp[string](schema, 1, "name", kvo.TMachineString)
	for _, modelName := range []string{"One", "Two"} {
		kvo.NewModel(schema, kvo.NewEntityType(schema, modelName, reflect.TypeFor[BoundModel]()), func(b *kvo.ModelBuilder) { b.Prop(name) })
	}
	models, err := evm.ModelPackage("db", schema)
	if err != nil {
		t.Fatal(err)
	}
	opt := evmbind.Options{Models: &models, NativePackage: reflect.TypeFor[BoundModel]().PkgPath()}
	if _, err = evmbind.NewTypes("model", modeldata.Source, []string{}, opt); err != nil {
		t.Fatal("unused ambiguous model rejected", err)
	}
	if _, err = evmbind.NewTypes("model", modeldata.Source, []string{"BoundModel"}, opt); err == nil {
		t.Fatal("ambiguous model selected")
	}
}

func TestImportedStaticTypeDoesNotIdentifyLocalModel(t *testing.T) {
	models, _, err := modeldata.Declarations()
	if err != nil {
		t.Fatal(err)
	}
	_, err = evmbind.NewTypes("other", fstest.MapFS{"other.go": {Data: []byte(`package other;type BoundModel struct{Count int}`)}}, nil, evmbind.Options{Models: &models, Static: &evmbind.Static{Types: map[string]reflect.Type{"modeldata.BoundModel": reflect.TypeFor[BoundModel]()}}})
	if err == nil || !strings.Contains(err.Error(), "requires NativePackage") {
		t.Fatal("imported native type was used as local package identity", err)
	}
}

type GenericRegistryModel[T any] struct {
	Name   string `kvo:"name"`
	Marker T      `kvo:"-"`
}

func TestGenericNativeModelSourceRegistry(t *testing.T) {
	schema := kvo.NewSchema()
	prop := kvo.NewStringProp[string](schema, 1, "name", kvo.TMachineString)
	kvo.NewModel(schema, kvo.NewEntityType(schema, "StringModel", reflect.TypeFor[GenericRegistryModel[string]]()), func(b *kvo.ModelBuilder) { b.Prop(prop) })
	models, err := evm.ModelPackage("db", schema)
	if err != nil {
		t.Fatal(err)
	}
	source := fstest.MapFS{"model.go": {Data: []byte(`package model
 type Constraint interface { ~string | ~int64 }
 type GenericRegistryModel[T Constraint] struct { Unavailable missing.Type }
 `)}}
	_, err = evmbind.NewTypes("model", source, []string{reflect.TypeFor[GenericRegistryModel[string]]().Name()}, evmbind.Options{Models: &models, NativePackage: reflect.TypeFor[GenericRegistryModel[string]]().PkgPath()})
	if err != nil {
		t.Fatal(err)
	}
}

type genericRegistryHost struct{ row *GenericRegistryModel[string] }

func (h *genericRegistryHost) Echo(_ *evm.Call, r *GenericRegistryModel[string]) *GenericRegistryModel[string] {
	return r
}
func (h *genericRegistryHost) Get(_ *evm.Call) *GenericRegistryModel[string] { return h.row }

func TestGenericNativeModelSignatures(t *testing.T) {
	schema := kvo.NewSchema()
	prop := kvo.NewStringProp[string](schema, 1, "name", kvo.TMachineString)
	kvo.NewModel(schema, kvo.NewEntityType(schema, "StringModel", reflect.TypeFor[GenericRegistryModel[string]]()), func(b *kvo.ModelBuilder) { b.Prop(prop) })
	models, err := evm.ModelPackage("db", schema)
	if err != nil {
		t.Fatal(err)
	}
	dep, err := evmbind.NewTypes("model", fstest.MapFS{"model.go": {Data: []byte(`package model
 type GenericRegistryModel[T any] struct { Name string; Marker T }
 `)}}, nil, evmbind.Options{Models: &models, NativePackage: reflect.TypeFor[GenericRegistryModel[string]]().PkgPath()})
	if err != nil {
		t.Fatal(err)
	}
	src := fstest.MapFS{"host.go": {Data: []byte(`package host
 import "native/model"
 func Get()*model.GenericRegistryModel[string]
 func Echo(r *model.GenericRegistryModel[string])*model.GenericRegistryModel[string]
 `)}}
	if _, err := dep.NativePointer(&GenericRegistryModel[string]{Name: "native"}); err != nil {
		t.Fatal(err)
	}
	opt := evmbind.Options{Imports: map[string]*evmbind.Schema{"native/model": dep}, Host: reflect.TypeFor[*genericRegistryHost]()}
	host, err := evmbind.New("host", src, opt)
	if err != nil {
		t.Fatal(err)
	}
	pkgs := []evm.Package{models, dep.Package(), host.Bind(&genericRegistryHost{row: &GenericRegistryModel[string]{Name: "generic"}})}
	m, err := evmcompiler.Compile("test.go", `import("db";"host")
 func Run()bool { var r *db.StringModel=host.Get(); return r.name=="generic" && host.Echo(r)==r }`, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(p).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if !out[0].AsBool() {
		t.Fatal("generic model handoff failed")
	}
	generated, err := evmbind.GenerateWithOptions("host", "example/host", "bindings", src, opt)
	if err != nil {
		t.Fatal(err)
	}
	middle, err := evmbind.NewTypes("middle", fstest.MapFS{"middle.go": {Data: []byte(`package middle
 import "native/model"
 type Transform func(*model.GenericRegistryModel[string]) *model.GenericRegistryModel[string]
 `)}}, nil, evmbind.Options{Imports: map[string]*evmbind.Schema{"native/model": dep}, Types: []reflect.Type{reflect.TypeFor[Transform]()}})
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := evmbind.NewTypes("consumer", fstest.MapFS{"consumer.go": {Data: []byte(`package consumer
 import "native/middle"
 type GenericHolder struct { Transform middle.Transform }
 `)}}, nil, evmbind.Options{Imports: map[string]*evmbind.Schema{"native/middle": middle}, Types: []reflect.Type{reflect.TypeFor[GenericHolder]()}})
	if err != nil {
		t.Fatal(err)
	}
	transitivePkgs := []evm.Package{models, dep.Package(), middle.Package(), consumer.Package()}
	module, err := evmcompiler.Compile("transitive.go", `import("db";"consumer");func Run(h *consumer.GenericHolder,r *db.StringModel)bool{return h.Transform(r)==r}`, transitivePkgs...)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, transitivePkgs...)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := consumer.NativePointer(&GenericHolder{Transform: func(r *GenericRegistryModel[string]) *GenericRegistryModel[string] { return r }})
	if err != nil {
		t.Fatal(err)
	}
	row, err := dep.NativePointer(&GenericRegistryModel[string]{Name: "transitive"})
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(program).Run(context.Background(), "Run", []values.Value{holder, row}, out[:]); err != nil {
		t.Fatal(err)
	}
	if !out[0].AsBool() {
		t.Fatal("transitive generic callback lost pointer identity")
	}
	for i := range 4 {
		t.Run(fmt.Sprintf("consumer%d", i), func(t *testing.T) {
			t.Parallel()
			if _, err := evmbind.New("host", src, opt); err != nil {
				t.Fatal(err)
			}
			got, err := evmbind.GenerateWithOptions("host", "example/host", "bindings", src, opt)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, generated) {
				t.Fatal("concurrent generation differs")
			}
		})
	}
	if !strings.Contains(string(generated), "GenericRegistryModel[string]") {
		t.Fatal("missing generic native spelling")
	}
}

func (h *modelHost) GenericGet(*evm.Call) *modeldata.Generic[string] {
	return &modeldata.Generic[string]{Name: "generic", Hidden: "preserved"}
}
func (h *modelHost) GenericEcho(_ *evm.Call, r *modeldata.Generic[string]) *modeldata.Generic[string] {
	return r
}
func (h *modelHost) GenericCopy(_ *evm.Call, r modeldata.Generic[string]) modeldata.Generic[string] {
	if r.Hidden != "preserved" {
		panic("lost hidden field")
	}
	return r
}

type Transform func(*GenericRegistryModel[string]) *GenericRegistryModel[string]
type GenericHolder struct{ Transform Transform }

func (h *modelHost) GenericNamed(*evm.Call) *modeldata.Generic[modeldata.BoundModel] {
	return &modeldata.Generic[modeldata.BoundModel]{Name: "named"}
}

func (h *modelHost) GenericDuration(*evm.Call) *modeldata.Generic[time.Duration] {
	return &modeldata.Generic[time.Duration]{Name: "duration"}
}

func TestGenericModelArgumentShapes(t *testing.T) {
	cases := []struct {
		spelling string
		native   reflect.Type
	}{
		{"error", reflect.TypeFor[GenericRegistryModel[error]]()},
		{"any", reflect.TypeFor[GenericRegistryModel[any]]()},
		{"[]byte", reflect.TypeFor[GenericRegistryModel[[]byte]]()},
		{"map[string]*int", reflect.TypeFor[GenericRegistryModel[map[string]*int]]()},
		{"[2]rune", reflect.TypeFor[GenericRegistryModel[[2]rune]]()},
	}
	for _, tc := range cases {
		t.Run(tc.spelling, func(t *testing.T) {
			schema := kvo.NewSchema()
			prop := kvo.NewStringProp[string](schema, 1, "name", kvo.TMachineString)
			kvo.NewModel(schema, kvo.NewEntityType(schema, "Row", tc.native), func(b *kvo.ModelBuilder) { b.Prop(prop) })
			models, err := evm.ModelPackage("db", schema)
			if err != nil {
				t.Fatal(err)
			}
			dep, err := evmbind.NewTypes("model", fstest.MapFS{"model.go": {Data: []byte("package model; type GenericRegistryModel[T any] struct{}")}}, nil, evmbind.Options{Models: &models, NativePackage: tc.native.PkgPath()})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = dep.NativePointer(reflect.New(tc.native).Interface()); err != nil {
				t.Fatal(err)
			}
			_, err = evmbind.GenerateWithOptions("host", "example/host", "bindings", fstest.MapFS{"host.go": {Data: []byte("package host; import \"native/model\"; func Get()*model.GenericRegistryModel[" + tc.spelling + "]")}}, evmbind.Options{Imports: map[string]*evmbind.Schema{"native/model": dep}})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

type LocalArgument string
type localGenericHost struct{}

func (*localGenericHost) Get(*evm.Call) *GenericRegistryModel[LocalArgument] {
	return &GenericRegistryModel[LocalArgument]{Name: "local"}
}

func TestGenericConsumerLocalArgument(t *testing.T) {
	schema := kvo.NewSchema()
	prop := kvo.NewStringProp[string](schema, 1, "name", kvo.TMachineString)
	native := reflect.TypeFor[GenericRegistryModel[LocalArgument]]()
	kvo.NewModel(schema, kvo.NewEntityType(schema, "Row", native), func(b *kvo.ModelBuilder) { b.Prop(prop) })
	models, err := evm.ModelPackage("db", schema)
	if err != nil {
		t.Fatal(err)
	}
	dep, err := evmbind.NewTypes("model", fstest.MapFS{"model.go": {Data: []byte("package model; type GenericRegistryModel[T any] struct{}")}}, nil, evmbind.Options{Models: &models, NativePackage: native.PkgPath()})
	if err != nil {
		t.Fatal(err)
	}
	src := fstest.MapFS{"host.go": {Data: []byte(`package host
 import "native/model"
 type LocalArgument string
 func Get()*model.GenericRegistryModel[LocalArgument]
 `)}}
	opt := evmbind.Options{Imports: map[string]*evmbind.Schema{"native/model": dep}, Host: reflect.TypeFor[*localGenericHost](), Types: []reflect.Type{reflect.TypeFor[LocalArgument]()}}
	host, err := evmbind.New("host", src, opt)
	if err != nil {
		t.Fatal(err)
	}
	_, err = evmbind.NewTypes("local", fstest.MapFS{"local.go": {Data: []byte(`package local; import "native/model"; type LocalArgument string; type LocalTransform func(*model.GenericRegistryModel[LocalArgument])`)}}, nil, evmbind.Options{Imports: map[string]*evmbind.Schema{"native/model": dep}, Types: []reflect.Type{reflect.TypeFor[LocalArgument](), reflect.TypeFor[LocalTransform]()}})
	if err != nil {
		t.Fatal(err)
	}
	argument, err := evmbind.NewTypes("argument", fstest.MapFS{"arg.go": {Data: []byte("package argument; type LocalArgument string")}}, nil, evmbind.Options{Types: []reflect.Type{reflect.TypeFor[LocalArgument]()}})
	if err != nil {
		t.Fatal(err)
	}
	imported := fstest.MapFS{"host.go": {Data: []byte(`package host; import("native/model";"remapped/argument");func Get()*model.GenericRegistryModel[argument.LocalArgument]`)}}
	importedOpt := evmbind.Options{Imports: map[string]*evmbind.Schema{"native/model": dep, "remapped/argument": argument}, Host: reflect.TypeFor[*localGenericHost]()}
	if _, err := evmbind.New("host", imported, importedOpt); err != nil {
		t.Fatal(err)
	}
	if _, err := evmbind.GenerateWithOptions("host", native.PkgPath(), "bindings", imported, importedOpt); err != nil {
		t.Fatal(err)
	}
	if host.Type("*"+native.PkgPath()+"."+native.Name()) != models.Records[0].Pointer {
		t.Fatal("wrong concrete row type")
	}
	if _, err := evmbind.GenerateWithOptions("host", native.PkgPath(), "bindings", src, opt); err != nil {
		t.Fatal(err)
	}
}

type LocalTransform func(*GenericRegistryModel[LocalArgument])

func (h *modelHost) Rows(*evm.Call) []*BoundModel {
	if h.rows == nil {
		h.rows = make([]*BoundModel, 2, 4)
		h.rows[0] = h.row
	}
	return h.rows
}
func (h *modelHost) EchoRows(_ *evm.Call, rows []*BoundModel) []*BoundModel { return rows }

func (*modelHost) Mutate(_ *evm.Call, row *BoundModel) { row.Name = "mutated" }

func (h *modelHost) AliasRows(_ *evm.Call, part, all []*BoundModel) []*BoundModel {
	part[0] = nil
	if all[1] != nil {
		panic("overlapping slice arguments lost aliasing")
	}
	all[2] = h.row
	return all[1:3]
}
func (h *modelHost) AnyRows(c *evm.Call) any            { return h.Rows(c) }
func (h *modelHost) EchoAny(_ *evm.Call, value any) any { return value }

func (h *modelHost) SwapRows(_ *evm.Call, rows []*BoundModel) { rows[0], rows[1] = rows[1], rows[0] }
func (h *modelHost) NilRows(_ *evm.Call) []*BoundModel        { return nil }
func (h *modelHost) IsRows(_ *evm.Call, value any) bool       { _, ok := value.([]*BoundModel); return ok }
func (h *modelHost) TailRows(_ *evm.Call, rows []*BoundModel) []*BoundModel {
	return rows[cap(rows):cap(rows)]
}

func (h *modelHost) WithRows(_ *evm.Call, rows []*BoundModel, visit func([]*BoundModel) []*BoundModel) {
	rows[0] = h.row
	returned := visit(rows)
	if &returned[0] != &rows[0] {
		panic("callback return lost slice alias")
	}
	if rows[1] != nil {
		panic("callback mutation not visible to host")
	}
	returned[2] = h.row
}
func (h *modelHost) VisitRows(_ *evm.Call, rows []*BoundModel, visit func()) {
	rows[0] = h.row
	visit()
	if rows[1] != nil {
		panic("empty callback mutation not visible to host")
	}
}

func (h *modelHost) ProduceRows(_ *evm.Call, produce func() []*BoundModel) []*BoundModel {
	rows := produce()
	rows[1] = h.row
	return rows
}

func (h *modelHost) DirectVisit(c *evm.Call, rows []*BoundModel, visit *evm.FunctionHandle) {
	rows[0] = h.row
	fn, err := visit.Value(c.Program(), evm.Signature{})
	if err != nil {
		panic(err)
	}
	if err := c.RunCallbackArgs(fn, nil, nil); err != nil {
		panic(err)
	}
	if rows[1] != nil {
		panic("direct callback mutation not visible to host")
	}
}
func (h *modelHost) PanicRows(_ *evm.Call, rows []*BoundModel) { rows[0] = h.row; panic("host panic") }

func (h *modelHost) Matrix(c *evm.Call) [][]*BoundModel                           { return [][]*BoundModel{h.Rows(c)} }
func (h *modelHost) EchoMatrix(_ *evm.Call, rows [][]*BoundModel) [][]*BoundModel { return rows }
func (h *modelHost) Groups(c *evm.Call) map[string][]*BoundModel {
	return map[string][]*BoundModel{"rows": h.Rows(c)}
}

func (h *modelHost) NamedRows(c *evm.Call) modelhost.NamedRowsType {
	return modelhost.NamedRowsType(h.Rows(c))
}
func (h *modelHost) NamedEcho(_ *evm.Call, rows modelhost.NamedRowsType) modelhost.NamedRowsType {
	if &rows[0] != &h.rows[0] {
		panic("named generated argument copied backing")
	}
	return rows
}
func (h *modelHost) NamedIs(_ *evm.Call, value any) bool {
	_, ok := value.(modelhost.NamedRowsType)
	return ok
}
func (h *modelHost) NamedProduce(_ *evm.Call, produce func() modelhost.NamedRowsType) modelhost.NamedRowsType {
	rows := produce()
	if &rows[0] != &h.rows[0] {
		panic("named callback copied backing")
	}
	return rows
}
func (h *modelHost) NamedMatrix(c *evm.Call) modelhost.NamedMatrixType {
	return modelhost.NamedMatrixType{h.Rows(c)}
}
func (h *modelHost) NamedMatrixEcho(_ *evm.Call, rows modelhost.NamedMatrixType) modelhost.NamedMatrixType {
	return rows
}

func (h *modelHost) Hidden(_ *evm.Call, r BoundModel) int { return r.Hidden }
func (h *modelHost) WithValue(_ *evm.Call, r BoundModel, visit func(BoundModel) BoundModel) BoundModel {
	result := visit(r)
	result.Name = "after callback"
	return result
}

func TestModelValueDeclarationRequiresNativeLayout(t *testing.T) {
	for _, allocation := range []bool{false, true} {
		schema := kvo.NewSchema()
		prop := kvo.NewStringProp[string](schema, 1, "name", kvo.TMachineString)
		model := kvo.NewModel(schema, kvo.NewEntityType(schema, "Customer", reflect.TypeFor[BoundModel](), reflect.TypeFor[modeldata.Alternate]()), func(b *kvo.ModelBuilder) { b.Prop(prop) })
		models, err := evm.ModelPackage("db", schema)
		if err != nil {
			t.Fatal(err)
		}
		if allocation {
			models.Record(model, false).NativeAllocation = reflect.TypeFor[BoundModel]()
		}
		dep, err := evmbind.NewTypes("model", fstest.MapFS{"model.go": {Data: []byte(`package model;type BoundModel struct{};type Alternate struct{}`)}}, nil, evmbind.Options{Models: &models, NativePackage: reflect.TypeFor[BoundModel]().PkgPath()})
		if err != nil {
			t.Fatal(err)
		}
		for _, typ := range []string{"BoundModel", "Alternate"} {
			src := fstest.MapFS{"host.go": {Data: []byte(`package host;import "native/model";func Copy(r model.` + typ + `)model.` + typ)}}
			_, err := evmbind.GenerateWithImports("host", "example/host", "bindings", src, map[string]*evmbind.Schema{"native/model": dep})
			if allocation && typ == "BoundModel" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "NativeAllocation") {
				t.Fatalf("allocation=%v type=%s: %v", allocation, typ, err)
			}
			src["host.go"].Data = []byte(`package host;import "native/model";func Get()*model.` + typ)
			if _, err := evmbind.GenerateWithImports("host", "example/host", "bindings", src, map[string]*evmbind.Schema{"native/model": dep}); err != nil {
				t.Fatal("pointer should remain supported:", err)
			}
		}
	}
}

func (h *modelHost) EchoValues(_ *evm.Call, rows []BoundModel) []BoundModel { return rows }
