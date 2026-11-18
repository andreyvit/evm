package evmcompiler_test

import (
	"strings"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
)

func TestPackageSourceUsesAny(t *testing.T) {
	pkg := evm.Package{Path: "example", Source: `package example
 type Anything interface{}
 type Context interface { Get(string) interface{}; Put(map[string]interface{}) }
 type Data struct { Values []interface{}; Nested struct { Value interface{} ` + "`note:\"interface{}\"`" + ` } }
 const Literal = "interface{}"
 func Transform(func(interface{}) []interface{}) map[string]interface{}
 `}
	source, err := evmcompiler.PackageSource("example", pkg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"type Anything any", "Get(string) any", "Put(map[string]any)", "Values []any", "Value any", "func Transform(func(any) []any) map[string]any", `const Literal = "interface{}"`, `note:\"interface{}\"`} {
		if !strings.Contains(source, want) {
			t.Fatalf("missing %q:\n%s", want, source)
		}
	}
	if strings.Contains(source, "interface {}") {
		t.Fatalf("old empty interface spelling:\n%s", source)
	}
}

func TestPackageSource_annotation_extension(t *testing.T) {
	pkg := evm.Package{Path: "example", Source: `package example
//app:arg value inherited
func Transform(value string) string
`}
	options := evmcompiler.DeclarationOptions{Annotations: func(path, name, doc string) []string {
		if path != "example" || name != "Transform" {
			t.Fatalf("%s.%s", path, name)
		}
		return []string{strings.TrimSpace(doc)}
	}}
	source, err := options.PackageSource("example", pkg)
	if err != nil || !strings.Contains(source, "//app:arg value inherited\nfunc Transform(value string) string") {
		t.Fatalf("%s: %v", source, err)
	}
	plain, err := evmcompiler.PackageSource("example", pkg)
	if err != nil || strings.Contains(plain, "//app:arg") {
		t.Fatalf("%s: %v", plain, err)
	}
}
