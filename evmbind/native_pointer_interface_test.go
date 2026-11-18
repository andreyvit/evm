package evmbind

import (
	"context"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

type InterfaceNode struct{ Value int }

type NativeReader interface{ Read() int }

func (n *InterfaceNode) EchoReader(v NativeReader) NativeReader { return v }

func (n *InterfaceNode) Read() int {
	if n == nil {
		return -1
	}
	return n.Value
}
func (n *InterfaceNode) Mark()          { n.Value++ }
func (n *InterfaceNode) Error() string  { return "node" }
func (n *InterfaceNode) Echo(v any) any { runtime.GC(); return v }

func TestNativePointerInterfaces(t *testing.T) {
	source := fstest.MapFS{"node.go": {Data: []byte(`package native
 type InterfaceNode struct {Value int}
 type NativeReader interface {Read()int}
 func(n *InterfaceNode) EchoReader(v NativeReader) NativeReader{return v}
 func(n *InterfaceNode) Read()int{return 0}
 func(n *InterfaceNode) Mark(){}
 func(n *InterfaceNode) Error()string{return ""}
 func(n *InterfaceNode) Echo(v any)any{return v}
 `)}}
	schema, err := New("native", source, Options{
		Only:    []string{"InterfaceNode"},
		Types:   []reflect.Type{reflect.TypeFor[InterfaceNode]()},
		Methods: map[string][]string{"InterfaceNode": {"Read", "Mark", "Error", "Echo", "EchoReader"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg := schema.Bind(nil)
	pkg.Functions = append(pkg.Functions, evm.ExternalFunction{Name: "New", Signature: evm.Signature{Results: []*values.Type{schema.Type("*InterfaceNode")}}, Call: func(c *evm.Call) error {
		v, err := schema.NativePointer(&InterfaceNode{Value: 42})
		if err != nil {
			return err
		}
		c.SetResult(0, v)
		return nil
	}})
	pkg.Functions = append(pkg.Functions, evm.ExternalFunction{Name: "ReadInterface", Signature: evm.Signature{Params: []*values.Type{values.AnyType}, Results: []*values.Type{values.IntType}}, Call: func(c *evm.Call) error {
		var result [1]values.Value
		if err := c.RunInterfaceMethod(c.Value(0), "Read", evm.Signature{Results: []*values.Type{values.IntType}}, nil, result[:]); err != nil {
			return err
		}
		c.SetResult(0, result[0])
		return nil
	}})
	module, err := evmcompiler.Compile("interface.go", `import("native";"fmt";"errors")
 type Reader interface{Read()int;Mark()}
 func caught(v any)(result any){defer func(){result=recover()}();panic(v)}
 func Check(p,q *native.InterfaceNode){
  var a any=p
  var reader Reader=p
  defer reader.Mark()
  if a.(*native.InterfaceNode)!=p || a.(Reader).Read()!=7 || reader.Read()!=7 {panic("assertion")}
  switch v:=a.(type){case *native.InterfaceNode:if v!=p{panic("switch value")};default:panic("switch type")}
  if p.EchoReader(reader)!=reader || p.EchoReader(reader).Read()!=7 {panic("native interface round trip")}
  if p.EchoReader(nil)!=nil {panic("nil interface")}
  if native.ReadInterface(a)!=7{panic("host interface dispatch")}
  if caught(a)!=a{panic("panic round trip")}
  var e error=p;if !errors.Is(e,p){panic("error equality")}
  if fmt.Sprintf("%T",a)!="*native.InterfaceNode"{panic(fmt.Sprintf("type: %T",a))}
  m:=map[any]int{a:9};if m[any(p)]!=9{panic("map identity")}
  var zero *native.InterfaceNode
  if p.EchoReader(zero)==nil || p.EchoReader(zero).Read()!=-1 {panic("native typed nil interface")}
  var guestNil any=zero
  var nativeNil any=q
  if nativeNil==nil || guestNil!=nativeNil || nativeNil.(Reader).Read()!=-1 {panic("typed nil")}
  m[nativeNil]=11;if m[guestNil]!=11{panic("nil map identity")}
  if p.Echo(a)!=a{panic("native any round trip")}
  held:=struct{X any}{native.New()};p.Echo(nil)
  if held.X.(*native.InterfaceNode).Read()!=42{panic("GC retention")}
  p.Echo(held);if held.X.(*native.InterfaceNode).Read()!=42{panic("owned interface field")}
 }
 func Entry(reader native.NativeReader)int{return reader.Read()}
 func Convert(p *native.InterfaceNode,n int)any{
  var result any
  for i:=0;i<n;i++{result=p;if result.(*native.InterfaceNode)!=p{panic("conversion")}}
  return result
 }
 `, pkg)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, pkg)
	if err != nil {
		t.Fatal(err)
	}
	node := &InterfaceNode{Value: 7}
	arg, err := schema.NativePointer(node)
	if err != nil {
		t.Fatal(err)
	}
	nilArg, err := schema.NativePointer((*InterfaceNode)(nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		node *InterfaceNode
		want int64
	}{{node, 7}, {nil, -1}} {
		input, err := schema.Interface(program, entry.node)
		if err != nil {
			t.Fatal(err)
		}
		var result [1]values.Value
		if err := evm.New(program).Run(context.Background(), "Entry", []values.Value{input}, result[:]); err != nil {
			t.Fatal(err)
		}
		if result[0].AsInt() != entry.want {
			t.Fatal(result[0].AsInt(), entry.want)
		}
	}
	if err := evm.New(program).Run(context.Background(), "Check", []values.Value{arg, nilArg}, nil); err != nil {
		t.Fatal(err)
	}
	if node.Value != 8 {
		t.Fatal("deferred native method did not run")
	}
	vm := evm.New(program)
	args := []values.Value{arg, values.Int(1)}
	var out [1]values.Value
	run := func() {
		if err := vm.Reset(); err != nil {
			t.Fatal(err)
		}
		// Allow fixed initialization costs, but not one allocation per conversion.
		if err := vm.RunWithBudget(context.Background(), "Convert", args, out[:], evm.Budget{Allocations: 16, AllocationBytes: 4096}); err != nil {
			t.Fatal(err)
		}
	}
	one := testing.AllocsPerRun(20, run)
	args[1] = values.Int(1000)
	many := testing.AllocsPerRun(20, run)
	if many != one {
		t.Fatalf("per-conversion allocations: one=%g many=%g", one, many)
	}
	_, value, err := program.UnpackInterface(out[0])
	if err != nil || value != arg {
		t.Fatal("conversion result", value, err)
	}
}

func TestConflictingNativePointerIdentities(t *testing.T) {
	declared, err := New("declared", fstest.MapFS{"api.go": {Data: []byte("package declared;type InterfaceNode struct{Value int}")}}, Options{Selective: true, Types: []reflect.Type{reflect.TypeFor[InterfaceNode]()}})
	if err != nil {
		t.Fatal(err)
	}
	opaque, err := New("opaque", fstest.MapFS{"api.go": {Data: []byte("package opaque;type NativeReader interface{Read()int}")}}, Options{Selective: true, Types: []reflect.Type{reflect.TypeFor[NativeReader]()}, Implementations: []Implementation{Pointer[InterfaceNode]()}})
	if err != nil {
		t.Fatal(err)
	}
	packages := []evm.Package{declared.Bind(nil), opaque.Bind(nil)}
	module, err := evmcompiler.Compile("conflict.go", `import("declared";"opaque");func Run(p *declared.InterfaceNode,q opaque.NativeReader)any{return p}`, packages...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evm.Link(module, packages...); err == nil || !strings.Contains(err.Error(), "conflicting opaque native pointer identities") {
		t.Fatal(err)
	}
}
