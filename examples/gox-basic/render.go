// Package goxbasic shows GoX templates using an ordinary Go application
// directly: its types, methods, package functions and constants. The host
// passes its own values as arguments.
//
// app.go is the application. views/ holds the templates, one package in
// several files. This file exposes the application to templates and renders.
// The test renders the page, changes a template's source, and renders again
// in the same process.
package goxbasic

import (
	"context"
	"embed"
	"fmt"
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

// Views holds the template sources by filename. Render compiles their
// current text, so assigning new source to a file changes the next render.
var Views = loadViews()

func loadViews() map[string]string {
	views := map[string]string{}
	names, err := fs.Glob(viewFiles, "views/*.gox")
	if err != nil {
		panic(err)
	}
	for _, name := range names {
		source, err := viewFiles.ReadFile(name)
		if err != nil {
			panic(err)
		}
		views[name] = string(source)
	}
	return views
}

// Env is what templates compile and link against. Build it once per process.
type Env struct {
	packages []evm.Package
	options  gox.Options
}

// NewEnv exposes the application to templates as package "app": every
// exported type with its methods, every exported function and every exported
// constant in app.go. Go cannot look functions or types up by name, so the
// options supply the functions, plus the types that no function, method or
// field leads to. Variables are not exposed.
//
// *app.Store is an implicit type: a component that declares a parameter of
// that type receives the caller's value of the same name without being passed
// it, so the page hands its store down to every component that asks.
func NewEnv() (*Env, error) {
	schema, err := evmbind.New("app", appSource, evmbind.Options{
		Functions: map[string]any{
			"Pluralize": Pluralize,
			"Discount":  Discount,
		},
		Types: []reflect.Type{
			reflect.TypeFor[Store](),
			reflect.TypeFor[User](),
			reflect.TypeFor[Cart](),
		},
	})
	if err != nil {
		return nil, err
	}
	return &Env{
		packages: []evm.Package{schema.Bind(nil)},
		options:  gox.Options{ImplicitTypes: []string{"*app.Store"}},
	}, nil
}

// Render compiles Views, links them and runs the Page function with args as
// its parameters after the builder. Nothing is cached between calls. Pointers
// to application structs are shared with the templates.
func (e *Env) Render(ctx context.Context, args ...any) (string, error) {
	tmpl, err := e.options.CompileFiles("Page", Views, e.packages...)
	if err != nil {
		return "", fmt.Errorf("compile: %w", err)
	}
	program, err := tmpl.Link(e.packages...)
	if err != nil {
		return "", fmt.Errorf("link: %w", err)
	}
	return program.RenderNative(ctx, args, gox.RenderOptions{Strict: true})
}

// Format returns one template as the GoX formatter writes it. Given the
// environment's packages, the formatter also adds missing imports of them.
func (e *Env) Format(filename string) (string, error) {
	return e.options.FormatSource(filename, Views[filename], e.packages...)
}
