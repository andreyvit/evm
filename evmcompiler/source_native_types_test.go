package evmcompiler_test

import (
	"context"
	"fmt"
	"go/token"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/nativebind"
	"github.com/andreyvit/evm/values"
)

type sourceNativeIndex[K any] struct{ Key K }
type sourceNativeTable struct{ value int }

func TestSourceNativeGenericTypes(t *testing.T) {
	scalar := &values.Shape{Type: values.StringType, Kind: values.StringKind, Cells: 1, Native: nativebind.Layout(reflect.TypeFor[string](), nil, true)}
	nativeType := reflect.TypeFor[sourceNativeIndex[string]]()
	fields := nativebind.BindFields(nativeType, nil, nativebind.Automatic)
	index := &values.Shape{Type: values.NewType("host.Index[string]", values.Pointer), Kind: values.StructKind, Cells: 1, Fields: []values.Field{{Name: "Key", Shape: scalar}}, Native: nativebind.Layout(nativeType, fields, false)}
	ptr := values.NewPointerShape(index)
	ptr.Type = values.NewType("*host.Index[string]", values.Pair)
	ptr.Native = nativebind.Layout(reflect.PointerTo(nativeType), nil, true)
	table := &values.Shape{Type: values.NewType("host.Table", values.Pointer), Kind: values.StructKind, Native: nativebind.Layout(reflect.TypeFor[sourceNativeTable](), nil, false)}
	tablePtr := values.NewPointerShape(table)
	tablePtr.Type = values.NewType("*host.Table", values.Pair)
	tablePtr.Native = nativebind.Layout(reflect.TypeFor[*sourceNativeTable](), nil, true)
	actualIndex := &sourceNativeIndex[string]{Key: "original"}
	actualTable := &sourceNativeTable{value: 42}
	pkg := evm.Package{Path: "host", Source: `//go:build go1.27
package host
type Index[K any] struct { Key K }
type Table struct{}
func (t *Table) Lookup[K comparable](index *Index[K], key K) int
`, Types: []evm.TypeDecl{{SourceName: "Table", Shape: table}, {Shape: tablePtr}, {SourceName: "Index", TypeArgs: []*values.Type{values.StringType}, Shape: index}, {Shape: ptr}}, Values: []evm.ExternalValue{
		{Name: "ByName", Type: ptr.Type, Read: func(c *evm.VM) error {
			c.SetCallResult(0, values.Value{R: values.NativeAddress, P: unsafe.Pointer(actualIndex)})
			return nil
		}},
		{Name: "Rows", Type: tablePtr.Type, Read: func(c *evm.VM) error {
			c.SetCallResult(0, values.Value{R: values.NativeAddress, P: unsafe.Pointer(actualTable)})
			return nil
		}},
	}, Methods: []evm.ExternalMethod{{Name: "Lookup", Receiver: tablePtr.Type, Instantiate: func(args []*values.Type) (evm.ExternalFunction, error) {
		if len(args) != 1 || args[0] != values.StringType {
			return evm.ExternalFunction{}, fmt.Errorf("unsupported index key")
		}
		return evm.ExternalFunction{Signature: evm.Signature{Params: []*values.Type{ptr.Type, values.StringType}, Results: []*values.Type{values.IntType}}, Call: func(c *evm.VM) error {
			table := (*sourceNativeTable)(c.CallArgument(0).P)
			idx := (*sourceNativeIndex[string])(c.CallArgument(1).P)
			result := -1
			if idx.Key == c.CallArgument(2).AsString() {
				result = table.value
			}
			c.SetCallResult(0, values.Int(int64(result)))
			return nil
		}}, nil
	}}}}
	for _, call := range []string{`host.Rows.Lookup(&host.Index[string]{Key:"changed"},"changed")`, `func()int{x:=host.Index[string]{Key:"changed"};return host.Rows.Lookup(&x,"changed")}()`, `func()int{x:=*host.ByName;x.Key="copy";return host.Rows.Lookup(&x,"copy")}()`, `func()int{s:="old";p:=&s;*p="changed";return host.Rows.Lookup(host.ByName,*p)}()`, `host.Rows.Lookup(host.ByName,"changed")`, `host.Rows.Lookup[string](host.ByName,"changed")`, `func()int{f:=host.Rows.Lookup[string];return f(host.ByName,"changed")}()`} {
		t.Run(call, func(t *testing.T) {
			actualIndex.Key = "original"
			m, err := evmcompiler.Compile("guest.go", `import "host";func Run()int{var idx *host.Index[string]=host.ByName;idx.Key="changed";return `+call+`}`, pkg)
			if err != nil {
				t.Fatal(err)
			}
			p, err := evm.Link(m, pkg)
			if err != nil {
				t.Fatal(err)
			}
			out := make([]values.Value, 1)
			if err := evmtest.New(p).Run(context.Background(), "Run", nil, out); err != nil {
				t.Fatal(err)
			}
			if out[0].AsInt() != 42 || actualIndex.Key != "changed" {
				t.Fatalf("result=%v", out)
			}
		})
	}
	t.Run("instance alias", func(t *testing.T) {
		alias := pkg
		alias.Source += "\ntype EmailIndex = Index[string]"
		alias.Types = append([]evm.TypeDecl(nil), pkg.Types...)
		alias.Types[2].SourceName = "EmailIndex"
		alias.Types[2].TypeArgs = nil
		printed, err := evmcompiler.PackageSource("host", alias)
		if err != nil || !strings.Contains(printed, "type EmailIndex = Index[string]") {
			t.Fatalf("source alias: %s, %v", printed, err)
		}
		if _, err := evmcompiler.Compile("alias.go", `import "host";func Run(){var idx *host.EmailIndex=host.ByName;host.Rows.Lookup(idx,"x")}`, alias); err != nil {
			t.Fatal(err)
		}
	})
	source, err := evmcompiler.PackageSource("host", pkg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"type Index[K any]", "var ByName *Index[string]", "Lookup[K comparable]"} {
		if !strings.Contains(source, want) {
			t.Fatalf("missing %q in %s", want, source)
		}
	}
	for _, call := range []string{`host.Rows.Lookup(host.ByName,42)`, `host.Rows.Lookup[int](host.ByName,42)`} {
		if _, err := evmcompiler.Compile("bad.go", `import "host";func Run(){_=`+call+`}`, pkg); err == nil || !strings.Contains(err.Error(), "cannot use") {
			t.Fatal(err)
		}
	}
	t.Run("source dependency", func(t *testing.T) {
		wrapped := pkg
		shape := &values.Shape{Type: values.NewType("host.Wrapper", values.Pointer), Kind: values.StructKind, Cells: 1, Fields: []values.Field{{Name: "Index", Shape: ptr}}}
		wrapped.Types = append(append([]evm.TypeDecl(nil), pkg.Types...), evm.TypeDecl{Name: "Wrapper", Shape: shape})
		m, err := evmcompiler.Compile("wrapper.go", `import "host";func Run(){w:=host.Wrapper{Index:host.ByName};if w.Index!=host.ByName{panic("index identity")}}`, wrapped)
		if err != nil {
			t.Fatal(err)
		}
		p, err := evm.Link(m, wrapped)
		if err != nil {
			t.Fatal(err)
		}
		if err := evmtest.New(p).Run(context.Background(), "Run", nil, nil); err != nil {
			t.Fatal(err)
		}
	})
	for name, modify := range map[string]func(*evm.Package){
		"empty receiver": func(p *evm.Package) { p.Source += "\nfunc () Broken()" },
		"generic receiver": func(p *evm.Package) {
			p.Methods = append(append([]evm.ExternalMethod(nil), p.Methods...), evm.ExternalMethod{Name: "Extra", Receiver: ptr.Type, Call: func(*evm.VM) error { return nil }})
		},

		"unknown":           func(p *evm.Package) { p.Types[2].SourceName = "Missing" },
		"both names":        func(p *evm.Package) { p.Types[2].Name = "Other" },
		"missing arguments": func(p *evm.Package) { p.Types[2].TypeArgs = nil },
		"extra arguments":   func(p *evm.Package) { p.Types[2].TypeArgs = []*values.Type{values.StringType, values.IntType} },
		"layout mismatch":   func(p *evm.Package) { p.Source = strings.Replace(p.Source, "Key K", "Other K", 1) },
		"mixed body":        func(p *evm.Package) { p.Source += "\nfunc Body() int { return 1 }" },
		"native receiver": func(p *evm.Package) {
			p.Source = strings.Replace(p.Source, "type Table struct{}", "", 1)
			p.Types[0].SourceName = ""
			p.Types[0].Name = "Table"
		},
		"duplicate instance": func(p *evm.Package) {
			duplicate := *p.Types[2].Shape
			duplicate.Type = values.NewType("duplicate", values.Pointer)
			p.Types = append(p.Types, evm.TypeDecl{SourceName: "Index", TypeArgs: []*values.Type{values.StringType}, Shape: &duplicate})
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := pkg
			bad.Types = append([]evm.TypeDecl(nil), pkg.Types...)
			modify(&bad)
			if _, err := evmcompiler.Compile("bad.go", `import "host";func Run(){_ = host.Rows}`, bad); err == nil {
				t.Fatal("invalid native source mapping accepted")
			}
			dependent := evm.Package{Path: "dependent", Functions: []evm.ExternalFunction{{Name: "Rows", Signature: evm.Signature{Results: []*values.Type{tablePtr.Type}}, Call: func(*evm.VM) error { return nil }}}}
			importer := evmcompiler.SourceImporter(token.NewFileSet(), bad, dependent)
			for range 2 {
				if _, err := importer.Import("host"); err == nil {
					t.Fatal("invalid import accepted")
				}
			}
			if _, err := importer.Import("dependent"); err == nil {
				t.Fatal("dependent import accepted failed source type")
			}
		})
	}
}

type sourceNativeKey string

func TestSourceNativeGenericNamedKey(t *testing.T) {
	keyType := values.NewType("keys.ID", values.Pair)
	keyNative := reflect.TypeFor[sourceNativeKey]()
	key := &values.Shape{Type: keyType, Kind: values.StringKind, Cells: 1, Native: nativebind.Layout(keyNative, nil, true)}
	keys := evm.Package{Path: "keys", Source: "package keys; type ID string", SharedTypes: []evm.SharedType{{Name: "ID", Type: keyType, Native: keyNative}}}
	nativeType := reflect.TypeFor[sourceNativeIndex[sourceNativeKey]]()
	index := &values.Shape{Type: values.NewType("host.Index[keys.ID]", values.Pointer), Kind: values.StructKind, Cells: 1, Fields: []values.Field{{Name: "Key", Shape: key}}, Native: nativebind.Layout(nativeType, nativebind.BindFields(nativeType, nil, nativebind.Automatic), false)}
	ptr := values.NewPointerShape(index)
	ptr.Type = values.NewType("*host.Index[keys.ID]", values.Pair)
	ptr.Native = nativebind.Layout(reflect.PointerTo(nativeType), nil, true)
	host := evm.Package{Path: "host", Source: "package host; type Index[K any] struct { Key K }", Types: []evm.TypeDecl{{SourceName: "Index", TypeArgs: []*values.Type{keyType}, Shape: index}, {Shape: ptr}}}
	m, err := evmcompiler.Compile("guest.go", `import("host";"keys");func Run()string{idx:=&host.Index[keys.ID]{Key:"named"};return string(idx.Key)}`, host, keys)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, host, keys)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]values.Value, 1)
	if err := evmtest.New(p).Run(context.Background(), "Run", nil, out); err != nil {
		t.Fatal(err)
	}
	if out[0].AsString() != "named" {
		t.Fatal("named key did not round-trip")
	}
}

func TestSourcePackageName(t *testing.T) {
	for _, path := range []string{"host/v1", "host/version-1"} {
		pkg := evm.Package{Path: path, Source: "package actual; func Value()int{return 42}"}
		m, err := evmcompiler.Compile("guest.go", `import "`+path+`";func Run()int{return actual.Value()}`, pkg)
		if err != nil {
			t.Fatal(err)
		}
		p, err := evm.Link(m, pkg)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]values.Value, 1)
		if err := evmtest.New(p).Run(context.Background(), "Run", nil, out); err != nil {
			t.Fatal(err)
		}
		if out[0].AsInt() != 42 {
			t.Fatal(out)
		}
	}
}

func TestSourceSharedScalarPointer(t *testing.T) {
	keyType := values.NewType("keys.ID", values.Pair)
	native := reflect.TypeFor[sourceNativeKey]()
	key := &values.Shape{Type: keyType, Kind: values.StringKind, Cells: 1, Native: nativebind.Layout(native, nil, true)}
	ptr := values.NewPointerShape(key)
	ptr.Type = values.NewType("*keys.ID", values.Pair)
	ptr.Native = nativebind.Layout(reflect.PointerTo(native), nil, true)
	pkg := evm.Package{Path: "keys", Source: `package keys;type ID string;const Default="changed";func(id ID)Text()string{return string(id)};func(id *ID)Set(v ID){*id=v}`, SharedTypes: []evm.SharedType{{Name: "ID", Type: keyType, Native: native}}, Types: []evm.TypeDecl{{Shape: ptr}}}
	m, err := evmcompiler.Compile("use.go", `import "keys";func Run(p *keys.ID)*keys.ID{if p==nil{return nil};if p.Text()!="original"{panic("source method")};p.Set(keys.Default);return p}`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, pkg)
	if err != nil {
		t.Fatal(err)
	}
	original := sourceNativeKey("original")
	arg := values.Value{R: values.NativeAddress, P: unsafe.Pointer(&original)}
	out := make([]values.Value, 1)
	if err := evmtest.New(p).Run(context.Background(), "Run", []values.Value{arg}, out); err != nil {
		t.Fatal(err)
	}
	if original != "changed" || out[0] != arg {
		t.Fatal("shared scalar pointer was copied", original, out)
	}
	if err := evmtest.New(p).Run(context.Background(), "Run", []values.Value{{}}, out); err != nil {
		t.Fatal(err)
	}
	if out[0] != (values.Value{}) {
		t.Fatal("nil pointer changed", out)
	}
}

func TestSourceSharedScalarPointerRejectsOtherNativeDeclarations(t *testing.T) {
	native := reflect.TypeFor[sourceNativeKey]()
	keyType := values.NewType("keys.ID", values.Pair)
	key := &values.Shape{Type: keyType, Kind: values.StringKind, Cells: 1, Native: nativebind.Layout(native, nil, true)}
	ptr := values.NewPointerShape(key)
	ptr.Native = nativebind.Layout(reflect.PointerTo(native), nil, true)
	other := values.NewPointerShape(&values.Shape{Type: values.IntType, Kind: values.IntKind, Cells: 1, Bits: 64, Native: nativebind.Layout(reflect.TypeFor[int](), nil, true)})
	other.Native = nativebind.Layout(reflect.TypeFor[*int](), nil, true)
	for name, decl := range map[string]evm.TypeDecl{
		"named":          {Name: "Other", Shape: other},
		"scalar":         {Shape: key},
		"unshared":       {Shape: other},
		"type arguments": {Shape: ptr, TypeArgs: []*values.Type{values.IntType}},
	} {
		t.Run(name, func(t *testing.T) {
			pkg := evm.Package{Path: "keys", Source: `package keys;type ID string;func(id ID)Text()string{return string(id)}`, SharedTypes: []evm.SharedType{{Name: "ID", Type: keyType, Native: native}}, Types: []evm.TypeDecl{{Shape: ptr}, decl}}
			_, err := evmcompiler.Compile("use.go", `import "keys";func Run(){_ = keys.ID("")}`, pkg)
			if err == nil || !strings.Contains(err.Error(), "mixed native/source package") {
				t.Fatal("expected mixed package rejection", err)
			}
		})
	}
}

func TestSourceNativeDeferredConstraintValidation(t *testing.T) {
	for _, valid := range []bool{true, false} {
		b := &values.Shape{Type: values.Boolean, Kind: values.BoolKind, Cells: 1}
		box := &values.Shape{Type: values.NewType("host.Box[host.W]", values.Pointer), Kind: values.StructKind, Cells: 1, Fields: []values.Field{{Name: "Hidden", Shape: b}}}
		field := b
		if !valid {
			field = &values.Shape{Type: values.NewType("[]bool", values.Pair), Kind: values.SliceKind, Cells: 1, Elem: b}
		}
		w := &values.Shape{Type: values.NewType("host.W", values.Pointer), Kind: values.StructKind, Cells: 2, Fields: []values.Field{{Name: "Box", Shape: box, Embedded: true}, {Name: "V", Shape: field, Offset: 1}}}
		pkg := evm.Package{Path: "host", Source: "package host\ntype Box[T comparable] struct{Hidden bool}", Types: []evm.TypeDecl{{Name: "W", Shape: w}, {SourceName: "Box", TypeArgs: []*values.Type{w.Type}, Shape: box}}}
		_, err := evmcompiler.Compile("constraint.go", `import "host";func Run(){_=host.W{}}`, pkg)
		if valid && err != nil {
			t.Fatal(err)
		}
		if !valid && (err == nil || !strings.Contains(err.Error(), "does not satisfy comparable")) {
			t.Fatalf("invalid constraint: %v", err)
		}
		if valid {
			pkg.Source += "\nfunc Get(b *Box[W])"
			_, err := evmcompiler.Compile("source_constraint.go", `import "host";func Run(){_=host.W{}}`, pkg)
			if err == nil || !strings.Contains(err.Error(), "deferred native types W") {
				t.Fatalf("source constraint diagnostic: %v", err)
			}
		}
	}
}

func TestNativeStructWithSourceDefinedScalar(t *testing.T) {
	keyType := values.NewType("host.Key", values.Pair)
	field := &values.Shape{Type: keyType, Kind: values.StringKind, Cells: 1}
	shape := &values.Shape{Type: values.NewType("host.Wrapper", values.Pointer), Kind: values.StructKind, Cells: 2, Fields: []values.Field{{Name: "Key", Shape: field}, {Name: "Keys", Offset: 1, Shape: &values.Shape{Type: values.NewType("[]host.Key", values.Pair), Kind: values.SliceKind, Cells: 1, Elem: field}}}}
	pkg := evm.Package{Path: "host", Source: "package host;type Key string", SharedTypes: []evm.SharedType{{Name: "Key", Type: keyType, Native: reflect.TypeFor[string]()}}, Types: []evm.TypeDecl{{Name: "Wrapper", Shape: shape}}}
	m, err := evmcompiler.Compile("use.go", `import "host";func Run()string{w:=host.Wrapper{Key:"hello",Keys:[]host.Key{"world"}};return string(w.Key)+"-"+string(w.Keys[0])}`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, pkg)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]values.Value, 1)
	if err := evmtest.New(p).Run(context.Background(), "Run", nil, out); err != nil {
		t.Fatal(err)
	}
	if out[0].AsString() != "hello-world" {
		t.Fatal(out)
	}
}
