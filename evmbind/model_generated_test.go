//go:build !evmreflect

package evmbind_test

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/modelbindings"
	"github.com/andreyvit/evm/evmbind/internal/modeldata"
	"github.com/andreyvit/evm/evmbind/internal/modelhost"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/kvo/codec/row2"
	"github.com/andreyvit/evm/values"
)

func TestGeneratedModelSignatures(t *testing.T) {
	models, dep, err := modeldata.Declarations()
	if err != nil {
		t.Fatal(err)
	}
	models.Records[0].NativeAllocation = reflect.TypeFor[BoundModel]()
	imports := map[string]*evmbind.Schema{"github.com/andreyvit/evm/evmbind/internal/modeldata": dep}
	generated, err := evmbind.GenerateWithImports("host", "github.com/andreyvit/evm/evmbind/internal/modelhost", "modelbindings", modelhost.Source, imports)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := os.ReadFile("internal/modelbindings/native_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, checked) {
		t.Fatal("regenerate model bindings")
	}
	for _, static := range []*evmbind.Static{nil, &modelbindings.Bindings} {
		opt := evmbind.Options{Imports: imports, Host: reflect.TypeFor[*modelHost](), Static: static}
		if static != nil {
			opt.Mode = evmbind.StaticOnly
		}
		host, err := evmbind.New("host", modelhost.Source, opt)
		if err != nil {
			t.Fatal(err)
		}
		instance := &modelHost{row: &BoundModel{Name: "original", Hidden: 42}}
		for _, ptr := range []*BoundModel{instance.row, nil} {
			value, err := dep.NativePointer(ptr)
			if err != nil {
				t.Fatal(err)
			}
			row, ok := kvo.TryFromValue(value)
			if !ok || row.NativePointer() != ptr {
				t.Fatal("native pointer entry lost model or identity", row, err)
			}
		}
		binding := kvo.Bind(reflect.TypeFor[BoundModel](), models.Records[0].Model)
		encoded := binding.Encoded(row2.Trusted(binding.Encode(nil, reflect.ValueOf(instance.row).Elem(), row2.Fast2)))
		records := evm.Package{Path: "records", Functions: []evm.ExternalFunction{{Name: "Get", Signature: evm.Signature{Results: []*values.Type{models.Records[0].Pointer}}, Call: func(c *evm.Call) error { c.SetResult(0, encoded.Value()); return nil }}}}
		records.Functions = append(records.Functions, evm.ExternalFunction{
			Name: "ToNative", Signature: evm.Signature{Params: []*values.Type{models.Records[0].Pointer}, Results: []*values.Type{models.Records[0].Pointer}},
			Call: func(c *evm.Call) error {
				row, err := c.Record(c.Argument(0), models.Records[0].Model)
				if err != nil {
					return err
				}
				x := new(BoundModel)
				if err := binding.TryDecodeRecord(row, reflect.ValueOf(x).Elem()); err != nil {
					return err
				}
				c.SetResult(0, binding.Record(reflect.ValueOf(x)).Value())
				return nil
			},
		})
		pkgs := []evm.Package{models, dep.Package(), host.Bind(instance), records}
		m, err := evmcompiler.Compile("model.go", `import("db";"host";"records")
   func genericCheck()bool {var r *db.GenericString=host.GenericGet();return host.GenericDuration().name=="duration" && host.GenericNamed().name=="named" && r.name=="generic" && host.GenericEcho(r)==r && host.GenericEcho(nil)==nil && host.GenericCopy(*r).name=="generic"}
   func valueCheck()bool{
 var zero db.Customer
 if host.Hidden(zero)!=0{return false}
 row:=host.Get()
 nativeValues:=host.EchoValues([]db.Customer{*row})
 nativeValues=append(nativeValues,*row)
 nativeValues=host.EchoAny(nativeValues).([]db.Customer)
 if len(host.EchoValues(nativeValues))!=2 || host.Hidden(nativeValues[1])!=42{return false}
 var captured db.Customer
 returned:=host.WithValue(*row,func(value db.Customer)db.Customer{captured=value;return value})
 return captured.name=="original" && returned.name=="after callback" && host.Hidden(captured)==42 && host.Hidden(returned)==42 && host.Hidden(host.Copy(*row))==42
 }
 func Run()bool {if !valueCheck(){return false};if !genericCheck(){return false};var row *db.Customer=host.Get();return row.name=="original" && host.Same(row) && host.Echo(row)==row && host.Echo(nil)==nil && host.Copy(*row).name=="copy" && row.name=="original"}
   func pairCheck()bool{r:=records.ToNative(records.Get());if !host.Pair(r,r){panic("pair aliases")};box:=struct{Row *db.Customer}{r};if !host.Pair(r,box.Row){panic("stored pair aliases")};return true}
   func Convert()bool {other:=host.Other();native:=records.ToNative(other);decoded:=records.ToNative(records.Get());return native.name=="other" && !host.Same(native) && decoded.name=="original" && !host.Same(decoded) && pairCheck() && host.Copy(*records.ToNative(records.Get())).name=="copy" && host.Copy(*records.ToNative(other)).name=="copy"}
   func Slices()bool {
 rows:=host.Rows()
 named:=host.NamedEcho(host.NamedRows())
 plain:=(*[2]*db.Customer)(named)[:]
 converted:=host.NamedRowsType(plain)
 producedNamed:=host.NamedProduce(func()host.NamedRowsType{return converted})
 if !host.NamedIs(any(named)) || !host.NamedIs(any(converted)) || !host.NamedIs(any(producedNamed)){panic("named generated slice type")}
 if host.NamedEcho(plain)[0]!=rows[0]{panic("named generated slice alias")}
 namedMatrix:=host.NamedMatrix()
 matrixPlain:=(*[1][]*db.Customer)(namedMatrix)[:]
 if host.NamedMatrixEcho(matrixPlain)[0][0]!=rows[0]{panic("named generated nested slice alias")}
 groups:=host.Groups()
 if len(groups["rows"])!=2 || groups["rows"][0]!=rows[0] {panic("map slice binding")}
 matrix:=host.EchoMatrix(host.Matrix())
 if len(matrix)!=1 || len(matrix[0])!=2 || matrix[0][0]!=rows[0] {panic("nested model slice binding")}
 if len(rows)!=2 || cap(rows)!=4 || rows[1]!=nil || !host.Same(rows[0]) {return false}
 rows[1]=rows[0]
 rows=append(rows,records.ToNative(records.Get()),records.ToNative(host.Other()))
 if rows[2].name!="original" || rows[3].name!="other" || !host.Same(host.EchoRows(rows)[1]) {return false}
 r:=records.ToNative(records.Get())
 fresh:=host.EchoRows([]*db.Customer{rows[0],nil,r,r})
 if fresh[2]!=r || fresh[2].name!=r.name || fresh[3].name!=r.name {return false}
 host.Mutate(r)
 if host.Echo(r).name!="mutated" {return false}
 fresh[2]=r
 if fresh[2].name!="mutated" {return false}
 slot:=&fresh[1]; *slot=rows[0]
 if !host.Same(fresh[1]) {return false}
 clear(fresh[1:2])
 arr:=[2]*db.Customer{rows[0],r}
 dst:=make([]*db.Customer,2)
 copy(dst,arr[:])
 if !host.Same(dst[0]) || dst[1].name!=r.name {return false}
 for _,v:=range dst {if v==nil{return false}}
 dst=append(dst,rows[0])
 if !host.Same(dst[2]) {return false}
 view:=(*[2]*db.Customer)(rows)
 if &view[0]!=&rows[0] || (*view)[0]!=rows[0] {panic("native array address")}
 view[1]=rows[0]
 viaArray:=view[:]
 if viaArray[1]!=rows[0] {panic("native array slice")}
 viaArray[1]=nil
 if rows[1]!=nil {panic("native array alias")}
 rows[1]=rows[0]
 other:=records.ToNative(host.Other())
 reordered:=[]*db.Customer{r,other}
 host.SwapRows(reordered)
 if reordered[0]!=other || reordered[1]!=r {panic("reordered rows lost identity")}
 tail:=host.TailRows(reordered)
 if tail==nil || len(tail)!=0 || cap(tail)!=0 {panic("empty tail")}
 if host.NilRows()!=nil || !host.IsRows(any(host.NilRows())) {panic("typed nil any")}
 if host.EchoAny(any(host.NilRows())).([]*db.Customer)!=nil {panic("nil any roundtrip")}
 callbackRows:=[]*db.Customer{r,r,nil}
 host.WithRows(callbackRows,func(arg []*db.Customer)[]*db.Customer{
  if arg[0]!=host.Get() || callbackRows[0]!=arg[0] {panic("callback sees stale rows")}
  callbackRows[1]=nil
  if arg[1]!=nil {panic("callback input lost alias")}
  echoed:=host.EchoRows(callbackRows);echoed[2]=host.Get()
  if arg[2]!=host.Get() {panic("nested host slice lost alias")}
  return callbackRows
 })
 if callbackRows[2]!=host.Get() || callbackRows[1]!=nil {panic("callback mutation")}
 var produced []*db.Customer
 result:=host.ProduceRows(func()[]*db.Customer {produced=[]*db.Customer{r,nil};return produced})
 if produced[1]!=host.Get() || result[1]!=produced[1] {panic("callback-created slice lost alias")}
 result[0]=nil
 if produced[0]!=nil {panic("callback-created return lost alias")}
 callbackRows[0]=r;callbackRows[1]=r
 host.VisitRows(callbackRows,func(){
  if callbackRows[0]!=host.Get() {panic("empty callback sees stale rows")}
  callbackRows[1]=nil
 })
 callbackRows[0]=r;callbackRows[1]=r
 host.DirectVisit(callbackRows,func(){
  if callbackRows[0]!=host.Get() {panic("direct callback sees stale rows")}
  callbackRows[1]=nil
 })
 mixed:=[]*db.Customer{r,r,nil}
 aliased:=host.AliasRows(mixed[1:2:2],mixed)
 if mixed[0]!=r || mixed[1]!=nil || !host.Same(mixed[2]) {panic("guest slice mutation")}
 aliased[0]=rows[0]
 if mixed[1]!=rows[0] {panic("returned slice alias")}
 dynamic:=host.AnyRows().([]*db.Customer)
 if dynamic[0]!=rows[0] || cap(dynamic)!=cap(rows) {panic("native any slice")}
 again:=host.EchoAny(any(dynamic)).([]*db.Customer)
 again[1]=rows[0]
 if !host.Same(rows[1]) {panic("native any alias")}
 var empty []*db.Customer
 return host.Same(fresh[0]) && fresh[1]==nil && fresh[2].name=="mutated" && host.EchoRows(empty)==nil && host.EchoRows(make([]*db.Customer,0))!=nil
 }
 func PanicSlice(){rows:=host.Rows();rows[0]=nil;host.PanicRows(rows)}
 func StoreEncoded(){rows:=host.Rows();rows[0]=records.Get()}
 func RejectEncodedPointer(){host.Echo(records.Get())}
 func RejectOtherPointer(){host.Echo(host.Other())}
 func RejectEncodedValue(){host.Copy(*records.Get())}

  `, pkgs...)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := evmcompiler.Compile("readonly.go", `import "host";func Run(){host.Get().name="changed"}`, pkgs...); err == nil {
			t.Fatal("native model row was editable without annotation")
		}
		p, err := evm.Link(m, pkgs...)
		if err != nil {
			t.Fatal(err)
		}
		var out [1]values.Value
		if err = evm.New(p).Run(context.Background(), "Run", nil, out[:]); err != nil || out[0].R != 1 {
			t.Fatal(out, err)
		}
		if err = evm.New(p).Run(context.Background(), "Convert", nil, out[:]); err != nil || out[0].R != 1 {
			t.Fatal(out, err)
		}
		if err = evm.New(p).Run(context.Background(), "Slices", nil, out[:]); err != nil || out[0].R != 1 {
			t.Fatal(out, err)
		}
		vm := evm.New(p)
		err = vm.Run(context.Background(), "PanicSlice", nil, nil)
		if err == nil || !strings.Contains(err.Error(), "host panic") {
			t.Fatalf("expected host failure: %v", err)
		}
		if instance.rows[1] != instance.row || instance.rows[:4][2].Name != "original" || instance.rows[:4][3].Name != "other" {
			t.Fatal("slice aliasing lost")
		}
		if err = evm.New(p).Run(context.Background(), "StoreEncoded", nil, nil); err == nil || !strings.Contains(err.Error(), "explicit conversion") {
			t.Fatalf("encoded row store accepted: %v", err)
		}
		if instance.rows[0] != instance.row {
			t.Fatal("failed store changed destination")
		}
		for _, name := range []string{"RejectEncodedPointer", "RejectOtherPointer", "RejectEncodedValue"} {
			if err := evm.New(p).Run(context.Background(), name, nil, nil); err == nil || !strings.Contains(err.Error(), "explicit conversion") {
				t.Fatalf("%s: expected explicit conversion error, got %v", name, err)
			}
		}
		if _, err = evmcompiler.Compile("nil.go", `import "host";func Run(){host.Copy(nil)}`, pkgs...); err == nil {
			t.Fatal("nil accepted for native value")
		}
	}
}

func TestModelBindingFingerprint(t *testing.T) {
	schema := kvo.NewSchema()
	name := kvo.NewStringProp[string](schema, 1, "name", kvo.TMachineString)
	kvo.NewModel(schema, kvo.NewEntityType(schema, "OtherCustomer", reflect.TypeFor[BoundModel](), reflect.TypeFor[modeldata.Alternate]()), func(b *kvo.ModelBuilder) { b.Prop(name) })
	kvo.NewModel(schema, kvo.NewEntityType(schema, "GenericString", reflect.TypeFor[modeldata.Generic[string]]()), func(b *kvo.ModelBuilder) { b.Prop(name) })
	kvo.NewModel(schema, kvo.NewEntityType(schema, "GenericCustomer", reflect.TypeFor[modeldata.Generic[modeldata.BoundModel]]()), func(b *kvo.ModelBuilder) { b.Prop(name) })
	kvo.NewModel(schema, kvo.NewEntityType(schema, "GenericDuration", reflect.TypeFor[modeldata.Generic[time.Duration]]()), func(b *kvo.ModelBuilder) { b.Prop(name) })
	models, err := evm.ModelPackage("db", schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range models.Records {
		if row.Mutable {
			continue
		}
		switch row.Name {
		case "OtherCustomer":
			row.NativeAllocation = reflect.TypeFor[BoundModel]()
		case "GenericString":
			row.NativeAllocation = reflect.TypeFor[modeldata.Generic[string]]()
		case "GenericCustomer":
			row.NativeAllocation = reflect.TypeFor[modeldata.Generic[BoundModel]]()
		case "GenericDuration":
			row.NativeAllocation = reflect.TypeFor[modeldata.Generic[time.Duration]]()
		}
	}
	duration, err := evmbind.NewTypes("time", fstest.MapFS{"time.go": {Data: []byte("package time; type Duration int64")}}, nil, evmbind.Options{Types: []reflect.Type{reflect.TypeFor[time.Duration]()}})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := evmbind.NewTypes("model", modeldata.Source, nil, evmbind.Options{Models: &models, Types: []reflect.Type{reflect.TypeFor[BoundModel]()}, Imports: map[string]*evmbind.Schema{"time": duration}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := evmbind.New("host", modelhost.Source, evmbind.Options{Host: reflect.TypeFor[*modelHost](), Imports: map[string]*evmbind.Schema{"github.com/andreyvit/evm/evmbind/internal/modeldata": dep}, Static: &modelbindings.Bindings})
	if err != nil {
		t.Fatal(err)
	}
	if host.UsesStatic() {
		t.Fatal("model wrappers accepted a different model identity")
	}
}

func BenchmarkNativeModelSlices(b *testing.B) {
	for _, static := range []bool{false, true} {
		name := "Reflected"
		if static {
			name = "Generated"
		}
		b.Run(name, func(b *testing.B) {
			models, dep, err := modeldata.Declarations()
			if err != nil {
				b.Fatal(err)
			}
			opt := evmbind.Options{Host: reflect.TypeFor[*modelHost](), Imports: map[string]*evmbind.Schema{"github.com/andreyvit/evm/evmbind/internal/modeldata": dep}}
			if static {
				opt.Static = &modelbindings.Bindings
				opt.Mode = evmbind.StaticOnly
			}
			schema, err := evmbind.New("host", modelhost.Source, opt)
			if err != nil {
				b.Fatal(err)
			}
			host := &modelHost{row: &BoundModel{Name: "sample"}, rows: make([]*BoundModel, 128)}
			for i := range host.rows {
				host.rows[i] = host.row
			}
			pkgs := []evm.Package{models, dep.Package(), schema.Bind(host)}
			module, err := evmcompiler.Compile("bench.go", `import "host"
func Roundtrip()int{return len(host.EchoRows(host.Rows()))}
func Read()int{rows:=host.Rows();n:=0;for _,row:=range rows{n+=len(row.name)};return n}
func Index()int{rows:=host.Rows();n:=0;for i:=0;i<len(rows);i++{n+=len(rows[i].name)};return n}
`, pkgs...)
			if err != nil {
				b.Fatal(err)
			}
			program, err := evm.Link(module, pkgs...)
			if err != nil {
				b.Fatal(err)
			}
			for _, fn := range []string{"Roundtrip", "Read", "Index"} {
				b.Run(fn, func(b *testing.B) {
					vm := evm.New(program)
					vm.Limits.Allocations = ^uint64(0)
					vm.Limits.AllocationBytes = ^uint64(0)
					var out [1]values.Value
					ctx := context.Background()
					want := uint64(128 * 6)
					if fn == "Roundtrip" {
						want = 128
					}
					if err := vm.Run(ctx, fn, nil, out[:]); err != nil || out[0].R != want {
						b.Fatal(out, err)
					}
					b.ReportAllocs()
					b.ResetTimer()
					for range b.N {
						if err := vm.Run(ctx, fn, nil, out[:]); err != nil || out[0].R != want {
							b.Fatal(out, err)
						}
					}
				})
			}
		})
	}
}
