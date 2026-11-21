package gox

import (
	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

// LinkEnvironment is a set of bound host packages prepared for linking many
// templates and modules. It adds the GoX runtime and the evmstd standard
// packages, as Template.Link does.
//
// Building one validates and binds the packages once. Its programs share
// what the packages alone determine. A LinkEnvironment never changes and is
// safe for concurrent use.
type LinkEnvironment struct {
	links *evm.LinkEnvironment
}

// LinkOptions configures a link environment.
type LinkOptions struct {
	DefaultLimits *evm.Limits
	// Compilers are the compile environments that produce the templates and
	// modules to link. Programs of what they compile also share the linked
	// form of the host storage shapes. Anything else links the same way,
	// with shapes of its own.
	Compilers []*evmcompiler.Environment
}

// NewLinkEnvironment prepares the packages for linking. It fails where every
// link against them would fail.
func NewLinkEnvironment(packages ...evm.Package) (*LinkEnvironment, error) {
	return LinkOptions{}.NewEnvironment(packages...)
}

// NewEnvironment prepares the packages for linking; see [NewLinkEnvironment].
func (o LinkOptions) NewEnvironment(packages ...evm.Package) (*LinkEnvironment, error) {
	var shapes []*values.Shape
	var methods []evm.MethodTables
	for _, compiler := range o.Compilers {
		shapes = append(shapes, compiler.Shapes()...)
		methods = append(methods, compiler.MethodTables())
	}
	links, err := evm.LinkOptions{DefaultLimits: o.DefaultLimits, Shapes: shapes, Methods: methods}.NewEnvironment(withBuiltinPackages(packages)...)
	if err != nil {
		return nil, err
	}
	return &LinkEnvironment{links: links}, nil
}

// NewLinkEnvironment prepares packages for linking what this environment
// compiles. The packages declare what the environment was built from and
// bind the implementations, which a compile does not need.
func (e *Environment) NewLinkEnvironment(packages ...evm.Package) (*LinkEnvironment, error) {
	return LinkOptions{Compilers: []*evmcompiler.Environment{e.compiler}}.NewEnvironment(packages...)
}

// Link binds a template to the prepared packages; see [Template.Link].
func (l *LinkEnvironment) Link(t *Template) (*Program, error) {
	linked := &Program{template: t}
	p, err := l.links.LinkWithHostData(t.Module, linked)
	if err != nil {
		return nil, err
	}
	linked.setProgram(p)
	return linked, nil
}

// LinkModule links an ordinary Go module with GoX builder support; see
// [LinkModule].
func (l *LinkEnvironment) LinkModule(module *evm.Module) (*Program, error) {
	return l.Link(&Template{Module: module})
}

// EVM returns the prepared packages for linking with package evm.
func (l *LinkEnvironment) EVM() *evm.LinkEnvironment { return l.links }
