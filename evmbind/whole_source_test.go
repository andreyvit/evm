package evmbind_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	app "github.com/andreyvit/evm/evmbind/internal/appfixture"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

func wholeAppOptions() evmbind.Options {
	return evmbind.Options{
		Functions: map[string]any{"CurrentStore": app.CurrentStore, "Discount": app.Discount},
		Types:     []reflect.Type{reflect.TypeFor[app.Standalone]()},
		Omit:      []string{"Wait", "Product.Lock"},
	}
}

// A package's own source exposes its exported types, methods, functions and
// constants without listing them.
func TestWholeSourceExposesEverythingExported(t *testing.T) {
	schema, err := evmbind.New("app", app.Source, wholeAppOptions())
	if err != nil {
		t.Fatal(err)
	}
	pkg := schema.Bind(nil)
	module, err := evmcompiler.Compile("use.go", `
import "app"

func Run() string {
	store := app.CurrentStore()
	p := store.Find("Enamel Mug")
	p.Viewed()
	note := app.Standalone{Note: app.Greeting}
	var shelf app.Shelf = app.Garden
	out := store.Name + " " + p.Slug() + " " + p.Price.String() + " " + app.Discount(p.Price, 10).String()
	out += " " + p.Shelf.Label() + " " + shelf.Label() + " " + note.Note
	if p.Viewed() == 2 && app.MaxStock == 99 {
		out += " ok"
	}
	return out
}`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, pkg)
	if err != nil {
		t.Fatal(err)
	}
	app.Default.Products = []*app.Product{{Name: "Enamel Mug", Price: 1800, Shelf: app.Kitchen}}
	t.Cleanup(func() { app.Default.Products = nil })
	var out [1]values.Value
	if err := evm.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if got, want := out[0].AsString(), "Default enamel-mug $18.00 $16.20 kitchen garden hello ok"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWholeSourceHidesUnexportedAndOmitted(t *testing.T) {
	schema, err := evmbind.New("app", app.Source, wholeAppOptions())
	if err != nil {
		t.Fatal(err)
	}
	pkg := schema.Bind(nil)
	for name, body := range map[string]string{
		"unexported field":      `_ = app.CurrentStore().Products[0].views`,
		"unexported embedding":  `_ = app.CurrentStore().registry`,
		"unexported function":   `_ = app.slug("x")`,
		"unexported constant":   `_ = app.internalLimit`,
		"variable":              `_ = app.Default`,
		"omitted function":      `app.Wait(nil)`,
		"omitted method":        `app.CurrentStore().Products[0].Lock(nil)`,
		"function body package": `_ = app.Source`,
	} {
		if _, err := evmcompiler.Compile("use.go", "import \"app\"\nfunc Run() {\n"+body+"\n}", pkg); err == nil {
			t.Errorf("%s is visible to scripts", name)
		}
	}
}

func TestWholeSourceRequiresEveryFunctionAndType(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(*evmbind.Options)
		want   string
	}{
		"unbound function":  {func(o *evmbind.Options) { delete(o.Functions, "Discount") }, "Discount"},
		"unbound type":      {func(o *evmbind.Options) { o.Types = nil }, "Standalone"},
		"unsupported type":  {func(o *evmbind.Options) { o.Omit = []string{"Product.Lock"} }, "Wait"},
		"unknown omission":  {func(o *evmbind.Options) { o.Omit = append(o.Omit, "Missing") }, "Missing"},
		"bound and omitted": {func(o *evmbind.Options) { o.Omit = append(o.Omit, "Discount") }, "Discount"},
	} {
		opt := wholeAppOptions()
		tc.change(&opt)
		_, err := evmbind.New("app", app.Source, opt)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want an error naming %s, got: %v", name, tc.want, err)
		} else {
			t.Logf("%s: %v", name, err)
		}
	}
}

// Selecting keeps the old narrow surface: listed types, listed methods, bound functions.
func TestSelectiveSourceExposesOnlyListed(t *testing.T) {
	schema, err := evmbind.New("app", app.Source, evmbind.Options{
		Only:      []string{"Product"},
		Methods:   map[string][]string{"Product": {"Slug"}},
		Functions: map[string]any{"Discount": app.Discount},
		Types:     []reflect.Type{reflect.TypeFor[app.Product]()},
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg := schema.Bind(nil)
	if _, err := evmcompiler.Compile("use.go", `import "app"
func Run(p *app.Product) string { return p.Slug() + app.Discount(p.Price, 5).String() + app.Kitchen.Label() }`, pkg); err == nil || !strings.Contains(err.Error(), "Label") {
		t.Fatalf("want only the unlisted method Label to be missing, got: %v", err)
	}
	for name, body := range map[string]string{
		"unlisted type":    `var _ app.Store`,
		"unlisted method":  `func Run(p *app.Product) { p.Viewed() }`,
		"unbound function": `var _ = app.CurrentStore()`,
		"untyped constant": `var _ = app.MaxStock`,
	} {
		if _, err := evmcompiler.Compile("use.go", "import \"app\"\n"+body, pkg); err == nil {
			t.Errorf("%s is visible to scripts", name)
		}
	}
}
