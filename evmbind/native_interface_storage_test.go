package evmbind

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind/internal/interfacedep"
	"github.com/andreyvit/evm/evmcompiler"
)

type Problem interface{ Error() string }
type hiddenProblem struct{}

func (*hiddenProblem) Error() string { return "hidden" }

type ReaderTree struct {
	Problem  Problem
	Root     NativeReader
	Children []NativeReader
	Named    map[string]NativeReader
}

type opaqueReader struct{ value int }

func (r *opaqueReader) Read() int { return r.value }

func TestNativeInterfaceStorage(t *testing.T) {
	var tree *ReaderTree
	source := fstest.MapFS{"tree.go": {Data: []byte(`package tree
 type NativeReader interface{Read()int}
 type InterfaceNode struct{Value int}
 func(n *InterfaceNode)Read()int{return 0}
 type Problem interface{Error()string}
 type ReaderTree struct{Problem Problem;Root NativeReader;Children []NativeReader;Named map[string]NativeReader}
 func Keep(t *ReaderTree)
 func Mutate()
 func Opaque(t *ReaderTree)
 func Hidden()*ReaderTree
 `)}}
	s, err := New("tree", source, Options{
		Only:            []string{"ReaderTree", "InterfaceNode"},
		Types:           []reflect.Type{reflect.TypeFor[ReaderTree](), reflect.TypeFor[InterfaceNode]()},
		Methods:         map[string][]string{"InterfaceNode": {"Read"}},
		Implementations: []Implementation{Pointer[opaqueReader]()},
		Functions: map[string]any{
			"Hidden": func() *ReaderTree { return &ReaderTree{Problem: &hiddenProblem{}} },
			"Keep":   func(v *ReaderTree) { tree = v },
			"Opaque": func(v *ReaderTree) { v.Root = &opaqueReader{value: 31} },
			"Mutate": func() {
				if tree.Root != tree.Children[0] || tree.Root != tree.Named["first"] {
					panic("native interface identity changed")
				}
				tree.Root.(*InterfaceNode).Value = 23
				tree.Children[1] = tree.Root
				tree.Named["second"] = tree.Root
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg := s.Bind(nil)
	module, err := evmcompiler.Compile("tree.go", `import "tree"
 type GuestReader struct{}
 func(g *GuestReader)Read()int{return 42}
 type GuestError struct{}
 func(g *GuestError)Error()string{return "guest"}
 func RejectGuestError(){root:=&tree.ReaderTree{};root.Problem=&GuestError{}}
 func RejectUnregistered(){tree.Hidden().Problem.Error()}
 func RejectGuest(){root:=&tree.ReaderTree{};root.Root=&GuestReader{}}
 func Run(){
  node:=&tree.InterfaceNode{Value:7}
  var absent *tree.InterfaceNode
  children:=[]tree.NativeReader{node,absent}
  named:=map[string]tree.NativeReader{"first":node}
  root:=&tree.ReaderTree{Root:node,Children:children,Named:named}
  if root.Children[1]==nil || root.Children[1].Read()!=-1 {panic("typed nil lost")}
  tree.Keep(root);tree.Mutate()
  if node.Value!=23 || children[1]!=node || named["second"]!=node {panic("native writes copied")}
  if root.Root.Read()!=23 || root.Named["first"].Read()!=23 {panic("stored interface dispatch")}
  root.Root=nil
  if root.Root!=nil {panic("nil interface store")}
 tree.Opaque(root)
 if root.Root.Read()!=31 {panic("opaque dispatch")}
 root.Named["opaque"]=root.Root
 if root.Named["opaque"]!=root.Root {panic("opaque identity")}
  root.Children=append(root.Children,root.Root,nil)
  if root.Children[2].Read()!=31 || root.Children[3]!=nil {panic("interface append")}
  for key,value:=range root.Named {
   if key=="opaque" && value.Read()!=31 {panic("interface map range")}
  }
 root.Root=nil
 }
 `, pkg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(module, pkg)
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(p).Run(context.Background(), "Run", nil, nil); err != nil {
		t.Fatal(err)
	}
	if tree.Root != nil || tree.Children[1].Read() != 23 || tree.Named["second"] != tree.Children[0] {
		t.Fatal("native interface storage diverged")
	}
	for _, name := range []string{"RejectGuest", "RejectGuestError"} {
		err = evm.New(p).Run(context.Background(), name, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "does not implement") {
			t.Fatalf("%s: guest-only implementation must be rejected explicitly: %v", name, err)
		}
	}
	err = evm.New(p).Run(context.Background(), "RejectUnregistered", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "unregistered native type") {
		t.Fatalf("unregistered native interface value must be rejected: %v", err)
	}
}

type ImportedReaderTree struct {
	Root interfacedep.Named
}

func TestImportedNativeInterfaceStorage(t *testing.T) {
	dep, err := New("domain", interfacedep.Source, Options{
		Only:    []string{"Named", "NamedValue"},
		Types:   []reflect.Type{reflect.TypeFor[interfacedep.Named](), reflect.TypeFor[interfacedep.NamedValue]()},
		Methods: map[string][]string{"NamedValue": {"Label"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tree := &ImportedReaderTree{Root: &interfacedep.NamedValue{Text: "native"}}
	source := fstest.MapFS{"tree.go": {Data: []byte(`package tree
 import "github.com/andreyvit/evm/evmbind/internal/interfacedep"
 type ImportedReaderTree struct{Root interfacedep.Named}
 func Get()*ImportedReaderTree
 `)}}
	s, err := New("tree", source, Options{
		Only:      []string{"ImportedReaderTree"},
		Types:     []reflect.Type{reflect.TypeFor[ImportedReaderTree]()},
		Imports:   map[string]*Schema{"github.com/andreyvit/evm/evmbind/internal/interfacedep": dep},
		Functions: map[string]any{"Get": func() *ImportedReaderTree { return tree }},
	})
	if err != nil {
		t.Fatal(err)
	}
	packages := []evm.Package{dep.Bind(nil), s.Bind(nil)}
	module, err := evmcompiler.Compile("tree.go", `import("tree";"domain")
 func Run(){
 t:=tree.Get()
 if t.Root.Label()!="native"{panic("imported dispatch")}
 next:=&domain.NamedValue{Text:"guest"}
 t.Root=next
 if t.Root!=next{panic("imported identity")}
 }`, packages...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(module, packages...)
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(p).Run(context.Background(), "Run", nil, nil); err != nil {
		t.Fatal(err)
	}
	if tree.Root.Label() != "guest" {
		t.Fatal("imported native write lost")
	}
}
