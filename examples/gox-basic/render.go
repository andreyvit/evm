package main

import (
	"embed"
	"io/fs"
	"reflect"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/gox"
)

// The application's own source tells templates what the package declares.
//
//go:embed app.go
var appSource embed.FS

//go:embed views/*.gox
var viewFiles embed.FS

// Views holds the template sources by filename. A host that reloads
// templates replaces a file's text here and compiles again.
var Views = loadViews()

func loadViews() map[string]string {
	views := map[string]string{}
	for _, name := range must(fs.Glob(viewFiles, "views/*.gox")) {
		views[name] = string(must(viewFiles.ReadFile(name)))
	}
	return views
}

// Env is what templates compile and link against. Build it once per process.
type Env struct {
	Packages []evm.Package
	Options  gox.Options
}

// NewEnv exposes the application to templates as package "app": every
// exported type with its methods, every exported function and every exported
// constant in app.go. Variables are not exposed.
func NewEnv() *Env {
	// Go cannot look functions or types up by name, so the options supply
	// the functions, plus the types that no function, method or field
	// leads to.
	app := must(evmbind.New("app", appSource, evmbind.Options{
		Functions: map[string]any{
			"Pluralize":   Pluralize,
			"Discount":    Discount,
			"FormatMoney": FormatMoney,
			"FormatDate":  FormatDate,
		},
		Types: []reflect.Type{
			reflect.TypeFor[Store](),
			reflect.TypeFor[User](),
			reflect.TypeFor[Cart](),
		},
	}))
	return &Env{
		Packages: []evm.Package{app.Bind(nil)},
		Options: gox.Options{
			// A component that declares a *app.Store parameter receives
			// its caller's store without being passed it.
			// FormatMoney takes the store the same way, so a template
			// writes price | app.FormatMoney.
			ImplicitTypes: []string{"*app.Store"},
		},
	}
}
