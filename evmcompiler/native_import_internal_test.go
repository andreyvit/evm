package evmcompiler

import (
	"go/token"
	"go/types"
	"reflect"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/nativebind"
	"github.com/andreyvit/evm/values"
)

func TestNativeCollectionDeclaredAfterInference(t *testing.T) {
	text := &values.Shape{Type: values.StringType, Kind: values.StringKind, Cells: 1}
	mapping := &values.Shape{Type: values.NewType("map[string]string", values.Pointer), Kind: values.MapKind, Cells: 1, Key: text, Elem: text, Native: nativebind.Layout(reflect.TypeFor[map[string]string](), nil, false)}
	sequence := &values.Shape{Type: values.NewType("[]string", values.Pair), Kind: values.SliceKind, Cells: 1, Elem: text, Native: nativebind.Layout(reflect.TypeFor[[]string](), nil, false)}
	for _, source := range []string{"", "package native"} {
		env, err := newTypeEnvironment([]evm.Package{{Path: "native", Source: source, Types: []evm.TypeDecl{{Shape: mapping}, {Shape: sequence}}}})
		if err != nil {
			t.Fatal(err)
		}
		env.files = token.NewFileSet()
		goMap := types.NewMap(types.Typ[types.String], types.Typ[types.String])
		pointer, err := env.shape(types.NewPointer(goMap))
		if err != nil {
			t.Fatal(err)
		}
		if env.imported["native"] != nil {
			t.Fatal("local collection imported its declaration owner")
		}
		if pointer.Type.Repr() != values.Pair {
			t.Fatal("pointer inferred before native pointee layout")
		}
		array, err := env.shape(types.NewArray(types.Typ[types.String], 2))
		if err != nil {
			t.Fatal(err)
		}
		if array.Native == nil {
			t.Fatal("array lost native slice layout")
		}
		// A dependent package can shape a field before the owner's anonymous
		// declaration has been bound, including inside another collection.
		outer, err := env.shape(types.NewMap(types.Typ[types.String], goMap))
		if err != nil {
			t.Fatal(err)
		}
		inferred := outer.Elem
		if _, err := env.Import("native"); err != nil {
			t.Fatal(err)
		}
		if inferred.Type != mapping.Type || inferred.Native != mapping.Native || outer.Elem != inferred {
			t.Fatal("dependent collection lost the native identity/layout")
		}
		if typ, err := env.valueType(goMap); err != nil || typ != mapping.Type {
			t.Fatal(typ, err)
		}
	}
}

func TestNativePointerImportsItsReceiverPackage(t *testing.T) {
	thing := &values.Shape{Type: values.NewType("native.Thing", values.Pointer), Kind: values.StructKind}
	ptr := values.NewPointerShape(thing)
	signature := &values.Shape{Type: values.NewType("func()*native.Thing", values.Pair), Kind: values.FunctionKind, Cells: 1, Results: []*values.Shape{ptr}}
	iface := &values.Shape{Type: values.NewType("native.Selfer", values.Pair), Kind: values.InterfaceKind, Cells: 1, Methods: []values.Method{{ID: "Self", Signature: signature}}}
	pkg := evm.Package{Path: "native", Types: []evm.TypeDecl{{Name: "Thing", Shape: thing}, {Name: "Selfer", Shape: iface}, {Shape: ptr}}, Methods: []evm.ExternalMethod{{Name: "Self", Receiver: ptr.Type, Signature: evm.Signature{Results: []*values.Type{ptr.Type}}, Call: func(c *evm.Call) error { c.SetResult(0, c.Argument(0)); return nil }}}}
	env, err := newTypeEnvironment([]evm.Package{pkg})
	if err != nil {
		t.Fatal(err)
	}
	typ, err := env.goType(ptr.Type, nil)
	if err != nil {
		t.Fatal(err)
	}
	if types.NewMethodSet(typ).Len() != 1 {
		t.Fatal("native receiver method missing")
	}
}
