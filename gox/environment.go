package gox

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
)

// Environment is a set of host packages prepared for compiling many
// templates. It holds what every template can import: the supplied packages,
// the GoX runtime and the evmstd standard packages.
//
// Building one type-checks all of those packages once. Compiles that name it
// in Options.Environment check only their own sources. An Environment never
// changes and is safe for concurrent use.
type Environment struct {
	compiler *evmcompiler.Environment
	packages []evm.Package
}

// NewEnvironment prepares the packages. It fails if any package cannot be
// imported, including one that no template would import.
func NewEnvironment(packages ...evm.Package) (*Environment, error) {
	all := withBuiltinPackages(packages)
	compiler, err := evmcompiler.NewEnvironment(all...)
	if err != nil {
		return nil, err
	}
	return &Environment{compiler: compiler, packages: all}, nil
}

// Compiler returns the prepared packages for compiling ordinary Go with
// evmcompiler. Its sources can import everything a template can.
func (e *Environment) Compiler() *evmcompiler.Environment { return e.compiler }

// environment returns the packages to compile against and, for a prepared
// environment, its compiler.
func (options Options) environment(packages []evm.Package) ([]evm.Package, *evmcompiler.Environment, error) {
	if options.Environment == nil {
		return withBuiltinPackages(packages), nil, nil
	}
	if len(packages) != 0 {
		return nil, nil, fmt.Errorf("Options.Environment supplies the packages; pass none")
	}
	return options.Environment.packages, options.Environment.compiler, nil
}

func (c *compiler) newFileSet() *token.FileSet {
	if c.environment != nil {
		return c.environment.NewFileSet()
	}
	return token.NewFileSet()
}

func (c *compiler) analyze(files *token.FileSet, syntax []*ast.File) (*types.Package, *types.Info, error) {
	if c.environment != nil {
		return c.environment.AnalyzeFiles(files, syntax, loweredGo)
	}
	return loweredGo.AnalyzeFiles(files, syntax, c.packages...)
}

func (c *compiler) compileGenerated() (*evm.Module, error) {
	if c.environment != nil {
		return c.environment.CompileFiles(c.template.GeneratedSources, loweredGo)
	}
	return loweredGo.CompileFiles(c.template.GeneratedSources, c.packages...)
}
