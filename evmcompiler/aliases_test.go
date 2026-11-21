package evmcompiler_test

import (
	"strings"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

func TestExternalAliasValidation(t *testing.T) {
	for _, tc := range []struct {
		aliases []evm.TypeAlias
		want    string
	}{
		{[]evm.TypeAlias{{Name: "ID", Target: values.NewType("missing.ID", values.Raw)}}, "undeclared target"},
		{[]evm.TypeAlias{{Name: "ID"}}, "invalid alias"},
		{[]evm.TypeAlias{{Name: "_", Target: values.StringType}}, "invalid alias"},
		{[]evm.TypeAlias{{Name: "ID", Target: values.StringType}, {Name: "ID", Target: values.StringType}}, "duplicate alias"},
	} {
		_, err := evmcompiler.Compile("alias.go", `import "aliases";func Run(){var x aliases.ID;_=x}`, evm.Package{Path: "aliases", Aliases: tc.aliases})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("got %v, want %s", err, tc.want)
		}
	}
	pkg := evm.Package{Path: "aliases", Source: "package aliases;type ID string", Aliases: []evm.TypeAlias{{Name: "ID", Target: values.StringType}}}
	if _, err := evmcompiler.Compile("alias.go", `import "aliases";func Run(){var x aliases.ID;_=x}`, pkg); err == nil || !strings.Contains(err.Error(), "duplicate export") {
		t.Fatalf("name clash: %v", err)
	}
}

func TestAliasCannotAdoptFallbackType(t *testing.T) {
	missing := values.NewType("missing.ID", values.Raw)
	pkg := evm.Package{Path: "aliases", Aliases: []evm.TypeAlias{{Name: "PromoID", Target: missing}}, Functions: []evm.ExternalFunction{{Name: "Use", Signature: evm.Signature{Params: []*values.Type{missing}}, Call: func(*evm.VM) error { return nil }}}}
	_, err := evmcompiler.Compile("alias.go", `import "aliases";func Run(){var x aliases.PromoID;aliases.Use(x)}`, pkg)
	if err == nil || !strings.Contains(err.Error(), "undeclared target") {
		t.Fatalf("alias accepted implicit fallback type: %v", err)
	}
}
