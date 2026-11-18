package evmbind

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

type ARecursiveRoot struct{ Node *RecursiveNode }
type RecursiveNode struct {
	Value    int
	Children []*RecursiveNode
	ByName   map[string]*RecursiveNode
}

func TestRecursivePointerCollectionIdentity(t *testing.T) {
	schema, err := NewTypes("native", fstest.MapFS{"types.go": {Data: []byte(`package native
 type ARecursiveRoot struct{Node *RecursiveNode}
 type RecursiveNode struct{Value int;Children []*RecursiveNode;ByName map[string]*RecursiveNode}
 `)}}, []string{"ARecursiveRoot", "RecursiveNode"}, Options{Types: []reflect.Type{reflect.TypeFor[ARecursiveRoot](), reflect.TypeFor[RecursiveNode]()}})
	if err != nil {
		t.Fatal(err)
	}
	pointer := schema.src.nodes["*RecursiveNode"].shape.Type
	for _, name := range []string{"[]*RecursiveNode", "map[string]*RecursiveNode"} {
		if schema.src.nodes[name].shape.Elem.Type != pointer {
			t.Fatalf("%s changed pointer identity", name)
		}
	}
	declarations := 0
	for _, decl := range schema.Package().Types {
		if decl.Shape.Type == pointer {
			declarations++
		}
	}
	if declarations != 1 {
		t.Fatalf("pointer declarations: %d", declarations)
	}
	pkg := schema.Bind(nil)
	module, err := evmcompiler.Compile("main.go", `import "native"
 func Check(root *native.ARecursiveRoot){
  n:=root.Node
  if n.Children[0]!=n || n.ByName["self"]!=n {panic("native identity lost")}
  n.Children[0].Value++
  n.ByName["self"].Value++
 }`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, pkg)
	if err != nil {
		t.Fatal(err)
	}
	n := &RecursiveNode{Value: 10}
	n.Children = []*RecursiveNode{n}
	n.ByName = map[string]*RecursiveNode{"self": n}
	root := &ARecursiveRoot{Node: n}
	value, err := schema.NativePointer(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(program).Run(context.Background(), "Check", []values.Value{value}, nil); err != nil {
		t.Fatal(err)
	}
	if n.Value != 12 {
		t.Fatalf("native writes lost: %d", n.Value)
	}
}

func TestRecursivePointerToCollectionReportsSchemaError(t *testing.T) {
	for _, definition := range []string{"type X []*X", "type X map[string]*X"} {
		_, err := NewTypes("native", fstest.MapFS{"types.go": {Data: []byte("package native;type A struct{P *X};" + definition)}}, []string{"A", "X"}, Options{})
		if err == nil || !strings.Contains(err.Error(), "expected pointer to data struct or scalar") {
			t.Fatalf("%s: %v", definition, err)
		}
	}
}
