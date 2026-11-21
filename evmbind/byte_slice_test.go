package evmbind

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/kvo/codec/row2"
	"github.com/andreyvit/evm/values"
)

type ByteSliceBox struct{ Data []byte }
type byteSliceHost struct{}

func (*byteSliceHost) Keep(_ *evm.VM, fn *evm.FunctionOf[func([]byte) int]) {}

func TestByteSliceNativeIdentity(t *testing.T) {
	var packages []evm.Package
	for _, spelling := range []string{"byte", "uint8"} {
		schema, err := New("host/"+spelling, fstest.MapFS{"types.go": {Data: []byte("package " + spelling + "; type ByteSliceBox struct { Data []" + spelling + " }")}}, Options{Only: []string{"ByteSliceBox"}, Types: []reflect.Type{reflect.TypeFor[ByteSliceBox]()}})
		if err != nil {
			t.Fatal(err)
		}
		packages = append(packages, schema.Package())
	}
	elem := &values.Shape{Type: values.Uint8, Kind: values.UintKind, Cells: 1, Bits: 8}
	shape := &values.Shape{Type: evm.ByteSliceType, Kind: values.SliceKind, Elem: elem, Cells: 1}
	raw := evm.Package{Path: "raw", Types: []evm.TypeDecl{{Shape: shape}}, Functions: []evm.ExternalFunction{{Name: "Sum", Signature: evm.Signature{Params: []*values.Type{evm.ByteSliceType}, Results: []*values.Type{values.IntType}}, Call: func(c *evm.VM) error {
		sum := int64(0)
		for i := uint64(0); i < uint64(c.CallArgument(0).SliceLen()); i++ {
			v, err := c.SliceElement(c.Program().Shape(evm.ByteSliceType), c.CallArgument(0), i)
			if err != nil {
				return err
			}
			sum += int64(v.R)
		}
		c.SetCallResult(0, values.Int(sum))
		return nil
	}}}}
	handles, err := New("handles", fstest.MapFS{"types.go": {Data: []byte(`package handles
 import "github.com/andreyvit/evm"
 func Keep(fn *evm.FunctionOf[func([]byte) int]) {panic("host")}
 `)}}, Options{Host: reflect.TypeFor[*byteSliceHost]()})
	if err != nil {
		t.Fatal(err)
	}
	packages = append(packages, handles.Bind(&byteSliceHost{}))
	packages = append(packages, raw)
	schema := kvo.NewSchema()
	entity := kvo.NewEntityType(schema, "Blob")
	field := kvo.NewProp(schema, 1, "data", kvo.TBytes, nil)
	model := kvo.NewModel(schema, entity, func(b *kvo.ModelBuilder) { b.Prop(schema.MustPropByCode(field)) })
	records, err := evm.ModelPackage("records", schema)
	if err != nil {
		t.Fatal(err)
	}
	edit := row2.Edit(row2.Empty(row2.Compact2))
	edit.Set([]row2.Step{{Key: field}}, values.Bytes([]byte{7, 8}), row2.Data)
	record := kvo.Encoded(edit.Snapshot()).WithModel(model)
	records.Functions = append(records.Functions, evm.ExternalFunction{Name: "Load", Signature: evm.Signature{Results: []*values.Type{records.Record(model, false).Pointer}}, Call: func(c *evm.VM) error { return c.SetRecordResult(model, record) }})
	packages = append(packages, records)
	for _, reverse := range []bool{false, true} {
		if reverse {
			slices.Reverse(packages)
		}
		imports := `import (a "host/byte"; b "host/uint8"; "raw"; "fmt"; "handles"; "records")`
		if reverse {
			imports = `import ("raw"; b "host/uint8"; a "host/byte"; "fmt"; "handles"; "records")`
		}
		module, err := evmcompiler.Compile("bytes.go", imports+`
 func Run(){
  handles.Keep(func(v []byte)int{return raw.Sum(v)})
  if raw.Sum(records.Load().data)!=15 {panic("record bytes")}
  x:=a.ByteSliceBox{Data:[]byte{1,2,255}}
  y:=b.ByteSliceBox{Data:x.Data}
  y.Data[0]=3
  if raw.Sum(x.Data)!=260 || raw.Sum(y.Data)!=260 {panic("native alias")}
  if raw.Sum([]byte{4})!=4 || raw.Sum([]byte("ab"))!=195 {panic("local bytes")}
  packed:=append([]byte(nil),"xy"...)
  if raw.Sum(packed)!=241 {panic("append bytes")}
  if raw.Sum(nil)!=0 || fmt.Sprintf("%T",x.Data)!="[]uint8" {panic("type or nil")}
 }
 `, packages...)
		if err != nil {
			t.Fatal(err)
		}
		program, err := evm.Link(module, packages...)
		if err != nil {
			t.Fatal(err)
		}
		if err := evmtest.New(program).Run(context.Background(), "Run", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
}
