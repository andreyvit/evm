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
// Native functions run outside the VM's instruction and allocation budgets;
// only the values they return are charged.
package evmstd

import (
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"sync"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
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
		lib.packages = append(lib.packages, schema.Bind(nil))
	}
	for _, path := range sourcePackages {
		text, err := sources.ReadFile("_src/" + flatten(path) + ".go")
		if err != nil {
			panic(err)
		}
		lib.packages = append(lib.packages, evm.Package{Path: path, Source: string(text)})
	}
	return lib
})

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
