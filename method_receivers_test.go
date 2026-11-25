package evm

import (
	"testing"

	"github.com/andreyvit/evm/values"
)

// The linker asks its index about every method. The index must agree with
// OwnsMethodReceiver, which checks one type by scanning the declarations.
func TestMethodReceiversMatchOwnsMethodReceiver(t *testing.T) {
	typ := func(name string, repr values.Repr) *values.Type { return values.NewType(name, repr) }
	structure := typ("Struct", values.Pointer)
	structPointer := typ("*Struct", values.Pointer)
	iface := typ("Interface", values.Pair)
	ifacePointer := typ("*Interface", values.Pointer)
	namedPointer := typ("NamedPointer", values.Pointer)
	source := typ("Source", values.Pointer)
	sourcePointer := typ("*Source", values.Pointer)
	shared := typ("Shared", values.Raw)
	record := typ("*Record", values.Pair)
	recordValue := typ("Record", values.Pointer)
	foreign := typ("Foreign", values.Pointer)
	foreignPointer := typ("*Foreign", values.Pointer)
	redeclared := typ("Redeclared", values.Pointer)
	twice := typ("*Twice", values.Pointer)
	unnamedSlice := typ("[]Struct", values.Pair)
	unknown := typ("Unknown", values.Raw)

	shape := func(t *values.Type, kind values.Kind, elem *values.Shape) *values.Shape {
		return &values.Shape{Type: t, Kind: kind, Elem: elem, Cells: 1}
	}
	p := Package{
		Path:        "host",
		SharedTypes: []SharedType{{Name: "Shared", Type: shared}},
		// A record owns its pointer and its value type; a record without a
		// value type must not own nil.
		Records: []*RecordType{nil, {Name: "Record", Pointer: record, Value: recordValue}, {Name: "Bare", Pointer: typ("*Bare", values.Pair)}},
		Types: []TypeDecl{
			{Shape: shape(structPointer, values.PointerKind, shape(structure, values.StructKind, nil))},
			{Name: "Struct", Shape: shape(structure, values.StructKind, nil)},
			{Name: "Interface", Shape: shape(iface, values.InterfaceKind, nil)},
			{Shape: shape(ifacePointer, values.PointerKind, shape(iface, values.InterfaceKind, nil))},
			{Name: "NamedPointer", Shape: shape(namedPointer, values.PointerKind, shape(structure, values.StructKind, nil))},
			{SourceName: "Source", Shape: shape(source, values.StructKind, nil)},
			{Shape: shape(sourcePointer, values.PointerKind, shape(source, values.StructKind, nil))},
			{Shape: shape(foreignPointer, values.PointerKind, shape(foreign, values.StructKind, nil))},
			{Shape: shape(unnamedSlice, values.SliceKind, shape(structure, values.StructKind, nil))},
			// The first named declaration of a type decides.
			{Name: "Redeclared", Shape: shape(redeclared, values.InterfaceKind, nil)},
			{Name: "Redeclared", Shape: shape(redeclared, values.StructKind, nil)},
			// The last unnamed pointer declaration of a type decides.
			{Shape: shape(twice, values.PointerKind, shape(structure, values.StructKind, nil))},
			{Shape: shape(twice, values.PointerKind, shape(iface, values.InterfaceKind, nil))},
			{Shape: shape(twice, values.PointerKind, nil)},
			{Name: "Broken"},
		},
	}
	want := map[*values.Type]bool{structure: true, structPointer: true, source: true, sourcePointer: true, shared: true, record: true, recordValue: true}
	receivers := p.methodReceivers()
	for _, typ := range []*values.Type{structure, structPointer, iface, ifacePointer, namedPointer, source, sourcePointer, shared, record, recordValue, foreign, foreignPointer, redeclared, twice, unnamedSlice, unknown, nil} {
		name := "nil"
		if typ != nil {
			name = typ.Name()
		}
		if got := p.OwnsMethodReceiver(typ); got != want[typ] {
			t.Errorf("OwnsMethodReceiver(%s) = %v, want %v", name, got, want[typ])
		}
		if got := receivers.owns(typ); got != want[typ] {
			t.Errorf("methodReceivers.owns(%s) = %v, want %v", name, got, want[typ])
		}
	}
	for _, pkg := range []Package{TimePackage(), {}} {
		receivers := pkg.methodReceivers()
		for _, d := range pkg.Types {
			if got, want := receivers.owns(d.Shape.Type), pkg.OwnsMethodReceiver(d.Shape.Type); got != want {
				t.Errorf("%s: methodReceivers.owns(%s) = %v, want %v", pkg.Path, d.Shape.Type.Name(), got, want)
			}
		}
	}
}
