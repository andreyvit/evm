package evmcompiler_test

import (
	"context"
	"strings"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
)

func TestPackageSourceFiles(t *testing.T) {
	pkg := evm.Package{Path: "host", SourceFiles: []evm.SourceFile{
		{Name: "type.go", Text: `package host;import x "strings";type Box struct{Value string};func New()Box{return Box{x.ToUpper("native")}}`},
		{Name: "methods.go", Text: `package host;import x "strconv";func(b Box)Label()string{return b.Value+x.Itoa(42)}`},
	}}
	m, err := evmcompiler.Compile("main.go", `import "host";func Run()string{return host.New().Label()}`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range pkg.SourceFiles {
		if m.Sources["evm/_stdlib/host/"+file.Name] != file.Text {
			t.Fatal("missing source", file.Name)
		}
	}
	p, err := evm.Link(m, pkg)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evmtest.New(p).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if out[0].AsString() != "NATIVE42" {
		t.Fatal(out[0].AsString())
	}
	doc, err := evmcompiler.PackageSource("host", pkg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, "type Box struct") || !strings.Contains(doc, "Label() string") {
		t.Fatal(doc)
	}
	for _, tc := range []struct {
		name  string
		files []evm.SourceFile
		want  string
	}{
		{"duplicate", []evm.SourceFile{{Name: "same.go", Text: "package host"}, {Name: "same.go", Text: "package host"}}, "duplicate source filename"},
		{"package mismatch", []evm.SourceFile{{Name: "a.go", Text: "package host"}, {Name: "b.go", Text: "package other"}}, "package other"},
		{"file import scope", []evm.SourceFile{{Name: "a.go", Text: `package host;import "strings"`}, {Name: "b.go", Text: `package host;func F()string{return strings.ToUpper("x")}`}}, "undefined: strings"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := evm.Package{Path: "host", SourceFiles: tc.files}
			_, err := evmcompiler.Compile("invalid.go", `import "host";func Run(){}`, p)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v want %s", err, tc.want)
			}
		})
	}
}

func TestNativeGenericSourceFiles(t *testing.T) {
	pkg := evm.Package{Path: "host", SourceFiles: []evm.SourceFile{
		{Name: "types.go", Text: `package host;type Count int`},
		{Name: "functions.go", Text: `package host;func Echo[T any](v T)T`},
	}, Functions: []evm.ExternalFunction{{Name: "Echo", Instantiate: func(args []*values.Type) (evm.ExternalFunction, error) {
		return evm.ExternalFunction{Signature: evm.Signature{Params: args, Results: args}, Call: func(c *evm.VM) error { c.SetCallResult(0, c.CallArgument(0)); return nil }}, nil
	}}}}
	m, err := evmcompiler.Compile("generic.go", `import "host";func Run()int{return int(host.Echo(host.Count(42)))}`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, pkg)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evmtest.New(p).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if out[0].AsInt() != 42 {
		t.Fatal(out)
	}
	pkg.Source = "package host"
	pkg.SourceFiles = append(pkg.SourceFiles, evm.SourceFile{Name: "host.go", Text: "package host"})
	if _, err := evmcompiler.Compile("invalid.go", `import "host";func Run(){}`, pkg); err == nil || !strings.Contains(err.Error(), "duplicate source filename") {
		t.Fatal(err)
	}
}

func TestMixedNativeSourceFilesRejectBodies(t *testing.T) {
	typ := values.NewType("host.Count", values.Raw)
	pkg := evm.Package{Path: "host", Types: []evm.TypeDecl{{Name: "Count", ScalarBase: values.IntType, Shape: &values.Shape{Type: typ, Kind: values.IntKind, Bits: 64, Cells: 1}}}, SourceFiles: []evm.SourceFile{
		{Name: "decl.go", Text: `package host;func Native()Count`},
		{Name: "body.go", Text: `package host;func Body(){}`},
	}}
	_, err := evmcompiler.Compile("mixed.go", `import "host";func Run(){}`, pkg)
	if err == nil || !strings.Contains(err.Error(), "mixed native/source") {
		t.Fatal(err)
	}
}

func TestPackageSourceFileNamesValidatedWithoutImport(t *testing.T) {
	for _, name := range []string{"", ".go", "a.txt", "b/b.go", "../b.go", `b\b.go`} {
		t.Run(name, func(t *testing.T) {
			_, err := evmcompiler.Compile("main.go", "func Run(){}", evm.Package{Path: "host", SourceFiles: []evm.SourceFile{{Name: name, Text: "package host"}}})
			if err == nil || !strings.Contains(err.Error(), "invalid source filename") {
				t.Fatalf("got %v", err)
			}
		})
	}
}
