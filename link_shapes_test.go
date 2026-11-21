package evm

import (
	"reflect"
	"testing"

	"github.com/andreyvit/evm/nativebind"
	"github.com/andreyvit/evm/values"
)

// The fingerprint that guards shared link data in other tests must notice a
// write to a snapshot, to its native layout and to a resolved binding.
func TestLinkEnvironmentFingerprintDetectsWrites(t *testing.T) {
	text := &values.Shape{Type: values.StringType, Kind: values.StringKind, Cells: 1, Native: nativebind.Layout(reflect.TypeFor[string](), nil, true)}
	row := values.NewStructShape("demo.Row", values.Field{Name: "Text", Shape: text})
	pkg := Package{Path: "demo", Types: []TypeDecl{{Name: "Row", Shape: row}}, Functions: []ExternalFunction{{Name: "F", Signature: Signature{Params: []*values.Type{values.StringType}}, Call: func(*VM) error { return nil }}}}
	env, err := LinkOptions{Shapes: []*values.Shape{row}}.NewEnvironment(pkg)
	if err != nil {
		t.Fatal(err)
	}
	if len(env.shapes.snapshots) != 2 || env.shapes.snapshots[0] == row || env.shapes.snapshots[1] == text {
		t.Fatalf("shared snapshots = %v", env.shapes.snapshots)
	}
	export := env.exports[exportKey{"demo", "F", nil}]
	if export.resolved == nil {
		t.Fatal("the binding is not resolved")
	}
	for name, write := range map[string]func() (undo func()){
		"shape field": func() func() {
			env.shapes.snapshots[0].Fields[0].Offset++
			return func() { env.shapes.snapshots[0].Fields[0].Offset-- }
		},
		"shape scalar": func() func() {
			env.shapes.snapshots[1].Cells++
			return func() { env.shapes.snapshots[1].Cells-- }
		},
		"native layout": func() func() {
			env.shapes.snapshots[1].Native.Size++
			return func() { env.shapes.snapshots[1].Native.Size-- }
		},
		"binding layout": func() func() {
			export.resolved.params.Slots[0]++
			return func() { export.resolved.params.Slots[0]-- }
		},
		"binding signature": func() func() {
			export.resolved.Signature.Params[0] = values.IntType
			return func() { export.resolved.Signature.Params[0] = values.StringType }
		},
	} {
		before := env.Fingerprint()
		undo := write()
		if env.Fingerprint() == before {
			t.Errorf("%s: the fingerprint missed the write", name)
		}
		undo()
		if env.Fingerprint() != before {
			t.Errorf("%s: the fingerprint is not repeatable", name)
		}
	}
}

// A program reaches only the shared snapshots its module names, and what
// those refer to.
func TestSharedShapesReach(t *testing.T) {
	text := &values.Shape{Type: values.StringType, Kind: values.StringKind, Cells: 1}
	number := &values.Shape{Type: values.Int64, Kind: values.IntKind, Bits: 64, Cells: 1}
	other := &values.Shape{Type: values.Int64, Kind: values.IntKind, Bits: 64, Cells: 1, DisplayName: "other"}
	row := values.NewStructShape("demo.Row", values.Field{Name: "Text", Shape: text})
	pointer := values.NewPointerShape(row)
	shared := newSharedShapes([]*values.Shape{pointer, number, other, nil, {}})
	if len(shared.snapshots) != 5 {
		t.Fatalf("%d shared snapshots, want 5", len(shared.snapshots))
	}
	shapes := newShapeSnapshots(shared)
	c, err := shapes.snapshot(other)
	if err != nil || c == other || c.DisplayName != "other" {
		t.Fatalf("snapshot = %+v, %v", c, err)
	}
	if got := shapes.ofType(values.Int64); got != c {
		t.Errorf("a type with two shared shapes resolves to %+v, want the one the module names", got)
	}
	if shapes.ofType(values.StringType) != nil || shapes.ofType(row.Type) != nil {
		t.Error("the module reaches shapes it does not name")
	}
	if _, err := shapes.snapshot(pointer); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []*values.Type{pointer.Type, row.Type, values.StringType} {
		if s := shapes.ofType(typ); s == nil || s.Type != typ {
			t.Errorf("%s is not reached through the pointer", typ.Name())
		}
	}
	own := &values.Shape{Type: values.NewType("[]demo.Row", values.Pair), Kind: values.SliceKind, Cells: 1, Elem: row}
	c, err = shapes.snapshot(own)
	if err != nil || c.Elem != shared.snapshots[shared.index[row]] || len(shapes.own) != 1 {
		t.Fatalf("a shape of the module's own must refer to the shared snapshot: %+v, %v", c, err)
	}
}
