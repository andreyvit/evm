// Package evmstd offers general-purpose Go standard packages to scripts,
// beyond the few the VM implements itself. Pass Packages to the compiler and
// the linker together with the host's own packages:
//
//	module, err := evmcompiler.Compile("plugin.go", source, evmstd.Packages()...)
//	program, err := evm.Link(module, evmstd.Packages()...)
//
// GoX includes them without being asked.
//
// Nothing here reads files, the network, the environment or the operating
// system. API.md lists what each package exposes and what it leaves out.
// Most native functions run outside the VM's instruction and allocation
// budgets, charging only the values they return. bytes.Replace, bytes.ReplaceAll,
// bytes.Join, and bytes.Repeat charge their output size before allocating
// (bytes.Repeat also caps output at 16 MiB). The six
// regexp.Regexp ReplaceAll methods (literal: ReplaceAllLiteral,
// ReplaceAllLiteralString; capture expansion: ReplaceAll, ReplaceAllString;
// callback: ReplaceAllFunc, ReplaceAllStringFunc) charge their output against
// the VM allocation budget. Source packages (cmp, iter, maps, slices, sort)
// are metered like script code.
package evmstd

import (
	"embed"
	"fmt"
	"io/fs"
	"reflect"
	"slices"
	"sync"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmstd/internal/cachecore"
)

//go:generate go run ../internal/genstd

//go:embed _decl/*/decl.go
var declarations embed.FS

//go:embed _src/*.go
var sources embed.FS

// nativePackage describes one standard package bound to its Go functions.
type nativePackage struct {
	Path    string
	Decl    string   // directory under _decl
	Imports []string // natively bound packages its declarations name
	Options evmbind.Options
	Host    any
}

type library struct {
	schemas  map[string]*evmbind.Schema
	packages []evm.Package
}

var load = sync.OnceValue(func() *library {
	lib := &library{schemas: map[string]*evmbind.Schema{}}
	for _, p := range nativePackages {
		files, err := fs.Sub(declarations, "_decl/"+p.Decl)
		if err != nil {
			panic(err)
		}
		opt := p.Options
		opt.Host = reflect.TypeOf(p.Host)
		opt.Imports = map[string]*evmbind.Schema{}
		for _, dep := range p.Imports {
			if lib.schemas[dep] == nil {
				panic(fmt.Sprintf("evmstd: %s is declared before %s", p.Path, dep))
			}
			opt.Imports[dep] = lib.schemas[dep]
		}
		schema, err := evmbind.New(p.Path, files, opt)
		if err != nil {
			panic(fmt.Sprintf("evmstd: %s: %v", p.Path, err))
		}
		lib.schemas[p.Path] = schema
		pkg := schema.Bind(p.Host)
		if p.Path == "regexp" {
			bindRegexpReplacements(&pkg)
		}
		lib.packages = append(lib.packages, pkg)
	}
	for _, path := range sourcePackages {
		text, err := sources.ReadFile("_src/" + flatten(path) + ".go")
		if err != nil {
			panic(err)
		}
		lib.packages = append(lib.packages, evm.Package{Path: path, Source: string(text)})
	}
	lib.packages = append(lib.packages, ConcurrencyPackages()...)
	return lib
})

var concurrencyPackages = sync.OnceValue(func() []evm.Package {
	src, err := sources.ReadFile("_src/concurrency.go")
	if err != nil {
		panic(err)
	}
	return []evm.Package{cachecore.Package(), {Path: "evmruntime/concurrency", Source: string(src)}}
})

// ConcurrencyPackages exposes the cache and its native bindings without the rest
// of evmstd. Packages already includes these declarations.
func ConcurrencyPackages() []evm.Package { return slices.Clone(concurrencyPackages()) }

// sourcePackages are written in script Go and compiled with the script.
var sourcePackages = []string{"cmp", "iter", "maps", "slices", "sort"}

func flatten(path string) string {
	out := []byte(path)
	for i, c := range out {
		if c == '/' {
			out[i] = '_'
		}
	}
	return string(out)
}

// Packages returns the standard packages for Compile and Link. The slice is
// the caller's to append to.
func Packages() []evm.Package { return slices.Clone(load().packages) }

// Paths lists the import paths Packages provides, sorted.
func Paths() []string {
	var paths []string
	for _, p := range load().packages {
		paths = append(paths, p.Path)
	}
	slices.Sort(paths)
	return paths
}

// Imports returns the schemas of the natively bound packages by import path,
// for evmbind.Options.Imports. A host API that uses time.Duration or
// *url.URL then shares those types with scripts.
func Imports() map[string]*evmbind.Schema {
	out := make(map[string]*evmbind.Schema, len(load().schemas))
	for path, schema := range load().schemas {
		out[path] = schema
	}
	return out
}
