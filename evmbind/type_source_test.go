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

type SelectedPermission uint16
type SelectedChild struct{ Permission SelectedPermission }
type SelectedPanel struct {
	Child     *SelectedChild
	Unrelated string
}

func TestSelectedNativeTypeSource(t *testing.T) {
	files := fstest.MapFS{"native.go": {Data: []byte(`package native
 import "not.available/to/the/vm"
 type SelectedPermission uint16
 type Other int
 const (
   hidden = iota
   Read SelectedPermission = 1 << iota
   Write
   OtherValue Other = 44
 )
 const WriteAlias = Write
 type SelectedChild struct { Permission SelectedPermission }
 type SelectedPanel struct { Child *SelectedChild; Unrelated string }
 type Unrelated chan int
 func (p SelectedPermission) String() string {return vm.Encode(p)}
 func Implementation() {vm.Run()}
 var Unavailable = vm.Init()
 `)}}
	schema, err := New("host/data", files, Options{Only: []string{"SelectedPanel"}, Types: []reflect.Type{reflect.TypeFor[SelectedPanel](), reflect.TypeFor[SelectedChild](), reflect.TypeFor[SelectedPermission]()}})
	if err != nil {
		t.Fatal(err)
	}
	if schema.Type("Other") != nil || schema.Type("Unrelated") != nil {
		t.Fatal("unrelated types exposed")
	}
	pkg := schema.Package()
	if len(pkg.Functions) != 0 || len(pkg.Methods) != 0 {
		t.Fatal("implementation methods exposed")
	}
	constants := map[string]uint64{}
	for _, c := range pkg.Constants {
		if c.Type != schema.Type("SelectedPermission") {
			t.Fatalf("unrelated constant exposed: %s", c.Name)
		}
		constants[c.Name] = c.Value.R
	}
	if !reflect.DeepEqual(constants, map[string]uint64{"Read": 2, "Write": 4, "WriteAlias": 4}) {
		t.Fatal(constants)
	}
	m, err := evmcompiler.Compile("check.go", `import "host/data"
 func Run(p *data.SelectedPanel) uint16 {p.Child.Permission=data.WriteAlias;return uint16(p.Child.Permission)+uint16(data.Read)}`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, pkg)
	if err != nil {
		t.Fatal(err)
	}
	native := &SelectedPanel{Child: &SelectedChild{}}
	arg, err := schema.NativePointer(native)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(p).Run(context.Background(), "Run", []values.Value{arg}, out[:]); err != nil {
		t.Fatal(err)
	}
	if out[0].R != 6 || native.Child.Permission != 4 {
		t.Fatalf("lost native enum identity or pointer: %v %+v", out, native.Child)
	}
	if _, err := New("host/data", files, Options{Only: []string{"Missing"}}); err == nil || !strings.Contains(err.Error(), "Missing") {
		t.Fatal(err)
	}
}

func TestSelectedSourceIncludesAllTypes(t *testing.T) {
	files := fstest.MapFS{"types.go": {Data: []byte(`package native
 type First int
 type Second string
 const One First = 1
 const Text Second = "two"
 func Implementation() { panic("unused") }
 `)}}
	s, err := parseTypes("data", files, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if s.nodes["First"] == nil || s.nodes["Second"] == nil || len(s.pkg.Constants) != 2 || len(s.ops) != 0 {
		t.Fatal("incomplete type selection")
	}
}
