package evmbind

import (
	"context"
	"reflect"
	"runtime"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
)

type AggregateCell struct {
	Value      int
	remembered *AggregateCell
}

func (s *AggregateCell) Echo(v any) any {
	runtime.GC()
	switch v.(type) {
	case nil, *evm.GuestValue, *AggregateCell:
	default:
		panic("guest storage leaked as native type")
	}
	return v
}
func (s *AggregateCell) Remember()    { s.remembered = s }
func (s *AggregateCell) IsSame() bool { return s.remembered == s }
func (s *AggregateCell) Add(n int)    { s.Value += n }

func TestNativeFieldsInGuestAggregates(t *testing.T) {
	source := fstest.MapFS{"native.go": {Data: []byte(`package native
 type AggregateCell struct {Value int; remembered *AggregateCell}
 func(s *AggregateCell) Remember(){}
 func(s *AggregateCell) IsSame()bool{return false}
 func(s *AggregateCell) Add(n int){}
 func(s *AggregateCell) Echo(v any)any{return v}
 `)}}
	schema, err := NewTypes("native", source, []string{"AggregateCell"}, Options{
		Types:      []reflect.Type{reflect.TypeFor[AggregateCell]()},
		OmitFields: map[string][]string{"AggregateCell": {"remembered"}},
		Methods:    map[string][]string{"AggregateCell": {"Remember", "IsSame", "Add", "Echo"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	module, err := evmcompiler.Compile("main.go", `import("native";"time")
 type Box struct { C native.AggregateCell; Tag string; Other struct{ N int }; Next *Box; Children []Box; Ptr *native.AggregateCell; Zero struct{}; Empty [0]native.AggregateCell; At time.Time }
 type A struct{C native.AggregateCell; B *B}
 type B struct{A A}
 type RecArray struct{C native.AggregateCell; P *[2]RecArray}
 var global Box
 func Check(b *Box) {
   b.C.Remember()
   b.C.Add(2)
   if !b.C.IsSame(){panic("native field changed address")}
   b.C.Value++
   b.C.Add(3)
   if b.C.Value != 6 {panic("native/guest writes diverged")}
   copy:=*b
   copy.C.Add(10)
   if b.C.Value != 6 || copy.C.Value != 16 || copy.C.IsSame(){panic("value copy aliased")}
   z:=&b.Zero;*z=struct{}{};if *z!=(struct{}{}){panic("zero field")}
   b.Other.N=7; b.Tag="kept"; b.Next=b
   if b.Other.N!=7 || b.Tag!="kept" || b.Next!=b {panic("guest fields")}
   if b.C.Echo(b).(*Box)!=b {panic("guest pointer any roundtrip")}
   round:=b.C.Echo(*b).(Box);round.C.Add(20)
   if b.C.Value!=6 || round.C.Value!=26 {panic("guest value any roundtrip")}
   m:=map[string]Box{"saved":*b};stored:=m["saved"];stored.C.Add(30)
   if b.C.Value!=6 || m["saved"].C.Value!=6 || stored.C.Value!=36 {panic("map value copies")}
   b.Ptr=&b.C
   if b.Ptr!=&b.C || !b.Ptr.IsSame(){panic("native pointer field")}
   alias:=&b.C
   if b.C.Echo(alias).(*native.AggregateCell)!=alias {panic("native pointer any roundtrip")}
   if !alias.IsSame(){panic("retained alias") }
 }
 func Inner()*struct{N int}{ var b Box; b.Other.N=42; return &b.Other }
 func Run(){
   var aa A;aa.B=new(B);aa.B.A.C.Remember();aa.B.A.C.Add(1);if !aa.B.A.C.IsSame(){panic("mutual type identity")}
   aa.B.A.B=aa.B;if aa.B.A.B!=aa.B{panic("mutual pointer field")};bb:=*aa.B;if bb.A.B!=aa.B{panic("mutual snapshot")}
   var ar RecArray;ar.P=new([2]RecArray);ar.P[1].C.Remember();ar.P[1].C.Add(1);if !ar.P[1].C.IsSame(){panic("recursive array identity")}
   copied:=*ar.P;if copied[1].C.Value!=1{panic("recursive array snapshot")}
   var gc native.AggregateCell
   inner:=Inner();gc.Echo(nil);gc.Echo(nil)
   if inner.N!=42 {panic("interior pointer GC")}
   nodes:=make([]Box,2);nodes[0].Next=&nodes[1];Check(nodes[0].Next)
   node:=new(Box);node.Next=new(Box);Check(node.Next)
   var local Box;Check(&local)
   nested:=struct{B Box}{};Check(&nested.B)
   var a [2]Box;Check(&a[1]);if gc.Echo(a).([2]Box)[1].C.Value!=6{panic("array any identity")}
   s:=make([]Box,1,1);Check(&s[0]);old:=&s[0].C
   s=append(s,Box{});Check(&s[1]);if gc.Echo(s).([]Box)[0].C.Value!=6{panic("slice any identity")}
   if old==&s[0].C || old.Value!=6 || s[0].C.IsSame(){panic("append addresses")}
   dst:=make([]Box,2);copy(dst,s);clear(s)
   if dst[0].C.Value!=6 || s[0].C.Value!=0 || old.Value!=6 {panic("copy/clear")}
   Check(&global)
   var captured Box;f:=func(){Check(&captured)};f()
   local.Children=make([]Box,1);Check(&local.Children[0])
 }
 `, schema.Package())
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, schema.Bind(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(program).Run(context.Background(), "Run", nil, nil); err != nil {
		t.Fatal(err)
	}
}
