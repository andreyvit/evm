package evmbind

import (
	"context"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
)

func TestNativePointerCaptureMutation(t *testing.T) {
	source := fstest.MapFS{"node.go": {Data: []byte(`package native; type InterfaceNode struct { Value int }`)}}
	schema, err := NewTypes("native", source, []string{"InterfaceNode"}, Options{Types: []reflect.Type{reflect.TypeFor[InterfaceNode]()}})
	if err != nil {
		t.Fatal(err)
	}
	const code = `import "native"
 func Read(slot **native.InterfaceNode) *native.InterfaceNode { return *slot }
 func NilAddress() (caught bool) { defer func(){caught=recover()!=nil}(); Read(nil); return }
 func Run() {
  a,b:=&native.InterfaceNode{Value:1},&native.InterfaceNode{Value:2}
  current:=a
  read:=func()*native.InterfaceNode{return current}
  write:=func(p *native.InterfaceNode){current=p}
  if read()!=a || Read(&current)!=a {panic("initial identity")}
  write(b)
  if read()!=b || Read(&current)!=b {panic("captured assignment")}
  *(&current)=nil
  if read()!=nil || Read(&current)!=nil {panic("nil value")}
  write(a);read().Value=3
  if a.Value!=3 || b.Value!=2 {panic("pointee identity")}
  holder:=struct{P *native.InterfaceNode}{a}
  array:=[1]*native.InterfaceNode{b}; slice:=[]*native.InterfaceNode{a}
  if Read(&holder.P)!=a || Read(&array[0])!=b || Read(&slice[0])!=a {panic("aggregate pointer slot")}
  if !NilAddress(){panic("nil address did not panic")}
 }`
	for _, disable := range []bool{false, true} {
		t.Run(map[bool]string{false: "optimized", true: "unoptimized"}[disable], func(t *testing.T) {
			module, err := (evmcompiler.Options{NoOptimization: disable}).Compile("main.go", code, schema.Package())
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
		})
	}
}
