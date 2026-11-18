// Package gox compiles GoX templates into typed EVM functions.
package gox

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"sync"
	"unsafe"

	"github.com/andreyvit/evm"
	ht "github.com/andreyvit/evm/gox/internal/htmltemplate"
	"github.com/andreyvit/evm/values"
)

// Trusted content types bypass selected escaping steps. Only construct them
// from trusted source, never by casting unvalidated input.
type HTML = template.HTML
type HTMLAttr = template.HTMLAttr
type CSS = template.CSS
type JS = template.JS
type JSStr = template.JSStr
type URL = template.URL
type Srcset = template.Srcset

// Format selects output analysis at compile time.
type Format uint8

const (
	HTMLFormat Format = iota
	TextFormat
	CSSFormat
)

// Options fixes the output language during compilation. The zero value is HTML.
type Options struct {
	// AllowMissingPackage accepts legacy files without a package clause.
	// They inherit the package name from the other files, or use plugin.
	AllowMissingPackage bool
	// ParseFrontmatter interprets file metadata during preprocessing, before type checking.
	// Nil rejects files containing frontmatter.
	ParseFrontmatter func([]byte) (any, error)
	Format           Format
	ImplicitTypes    []string
}

// Parameter describes an entry input in Render argument order.
// Parameter describes a Go input and its legacy argument adapter metadata.
// Optional is effective optionality, including structural optional types.
type Parameter struct {
	Name, Type string
	Optional   bool
	Implicit   bool
	LegacyName string
	// Ambient names a read-only inherited value; callers cannot supply this input.
	Ambient string
}

// Class identifies a statically known CSS class, including classes in helpers.
type Class struct {
	Name     string
	Position evm.SourcePosition
}
type Diagnostic struct {
	Position evm.SourcePosition
	Message  string
}

func (d Diagnostic) Error() string {
	return fmt.Sprintf("%s:%d:%d: %s", d.Position.File, d.Position.Line, d.Position.Column, d.Message)
}

// Template holds bytecode and its immutable rendering plan. Keep the plan with
// the module; the module alone does not contain the contextual escaping filters.
// Treat exported metadata as read-only after compilation.
type Template struct {
	files              map[string]File
	functionFiles      map[string]string
	Entry              string
	GeneratedSources   map[string]string
	DynamicClasses     bool
	format             Format
	Module             *evm.Module
	Parameters         []Parameter
	FunctionParameters map[string][]Parameter
	Classes            []Class
	GeneratedSource    string
	operations         []operation
	sources            map[string]string
}

// Program binds a template to host implementations and supports concurrent renders.
type Program struct {
	pool     sync.Pool
	template *Template
	program  *evm.Program
}
type RenderOptions struct {
	// Coverage collects execution for this linked program; nil disables it.
	// Rendering inside a native call inherits its collector.
	Coverage *evm.Coverage
	// Strict fails on nil field access in output instead of reporting and omitting it.
	Strict bool
	// Report receives recoverable output diagnostics. Nil logs through slog.
	Report func(Diagnostic)
	// Budget applies the same execution limits as an EVM script.
	Budget evm.Budget
	// MaxOutputBytes defaults to 16 MiB. A failed render returns no partial output.
	MaxOutputBytes int
}

// Compile compiles an HTML GoX file using EVM's standard and supplied packages.
func Compile(filename, source string, packages ...evm.Package) (*Template, error) {
	return Options{}.Compile(filename, source, packages...)
}
func (o Options) Compile(filename, source string, packages ...evm.Package) (*Template, error) {
	return o.CompileFiles(filename, map[string]string{filename: source}, packages...)
}

// LinkModule links an ordinary Go module with GoX builder support. Modules
// compiled from markup must use their Template.Link to retain the operations.
func LinkModule(module *evm.Module, packages ...evm.Package) (*Program, error) {
	return (&Template{Module: module}).Link(packages...)
}

// Link binds the module to host packages with matching signatures. It does not run it.
func (t *Template) Link(packages ...evm.Package) (*Program, error) {
	linked := &Program{template: t}
	p, err := evm.LinkWithHostData(t.Module, linked, append(packages, runtimePackage())...)
	if err != nil {
		return nil, err
	}
	linked.program = p
	return linked, nil
}

// Render executes with EVM values in Parameters order. Globals and temporary
// state reset between renders. Host package implementations retain their own state.
func (p *Program) Render(ctx context.Context, args []values.Value, options RenderOptions) (string, error) {
	return p.RenderFunc(ctx, p.template.Entry, args, options)
}

// EVM returns the linked program for ordinary Go entry points in this package.
func (p *Program) EVM() *evm.Program { return p.program }

// ProgramOf returns the GoX runtime that owns this linked program, or nil for
// programs linked without GoX. Retained functions keep this runtime alive.
func ProgramOf(p *evm.Program) *Program {
	program, _ := p.HostData().(*Program)
	return program
}

// RenderFunc executes a named renderer. args excludes its explicit builder parameter.
func (p *Program) RenderFunc(ctx context.Context, name string, args []values.Value, options RenderOptions) (string, error) {
	return p.render(ctx, nil, name, nil, args, nil, false, options)
}

// RenderNative executes the default renderer with ordinary Go values.
// See RenderFuncNative.
func (p *Program) RenderNative(ctx context.Context, args []any, options RenderOptions) (string, error) {
	return p.RenderFuncNative(ctx, p.template.Entry, args, options)
}

// RenderFuncNative executes a named renderer with ordinary Go values as its
// arguments, excluding the builder: strings and other scalars, time values,
// pointers to bound native structs, and registered interface implementations.
// A Go slice or map passes when a linked schema binds its type, as a field or
// in a signature. Each value must have the Go type its parameter is bound to;
// there are no implicit numeric conversions. Native pointers and collections
// are shared with the template, not copied. Conversion counts against
// options.Budget.
func (p *Program) RenderFuncNative(ctx context.Context, name string, args []any, options RenderOptions) (string, error) {
	return p.render(ctx, nil, name, nil, nil, args, true, options)
}

// RenderFunction executes a retained renderer, including a closure or bound
// method. Captures remain shared; the render VM and builder reset between calls.
// args excludes the renderer's explicit builder parameter.
func (p *Program) RenderFunction(ctx context.Context, fn *evm.FunctionHandle, args []values.Value, options RenderOptions) (string, error) {
	if fn == nil {
		return "", evm.ErrNilFunction
	}
	sig := fn.Signature()
	if len(sig.Params) == 0 || sig.Params[0] != BuilderType() || len(sig.Results) != 0 {
		return "", fmt.Errorf("invalid renderer signature")
	}
	return p.render(ctx, nil, "", fn, args, nil, false, options)
}

// RenderFunctionInCall renders within an active native call, sharing its VM
// globals, captures, context and budget. It supports nested renders with separate
// builders. options.Budget must be zero; the enclosing invocation owns limits.
func (p *Program) RenderFunctionInCall(c *evm.Call, fn *evm.FunctionHandle, args []values.Value, options RenderOptions) (string, error) {
	if fn == nil {
		return "", evm.ErrNilFunction
	}
	if c.Program() != p.program || fn.Program() != p.program {
		return "", evm.ErrFunction
	}
	sig := fn.Signature()
	if len(sig.Params) == 0 || sig.Params[0] != BuilderType() || len(sig.Results) != 0 {
		return "", fmt.Errorf("invalid renderer signature")
	}
	if options.Coverage != nil && options.Coverage != c.Coverage() {
		return "", fmt.Errorf("render within a call inherits its coverage collector")
	}
	if options.Budget != (evm.Budget{}) {
		return "", fmt.Errorf("render within a call uses the enclosing invocation budget")
	}
	return p.render(nil, c, "", fn, args, nil, false, options)
}

// render runs one renderer. With native set, nativeArgs are converted inside
// the invocation, where the VM can account for what conversion allocates.
func (p *Program) render(ctx context.Context, call *evm.Call, name string, fn *evm.FunctionHandle, args []values.Value, nativeArgs []any, native bool, options RenderOptions) (string, error) {
	session, _ := p.pool.Get().(*renderSession)
	if session == nil {
		session = &renderSession{}
	}
	if call == nil && session.vm == nil {
		session.vm = evm.New(p.program)
	}
	b := &session.builder
	b.program = p.program
	b.operations = p.template.operations
	b.options = options
	if b.options.Report == nil {
		b.options.Report = defaultReport
	}
	b.format = p.template.format
	b.content = ht.Context{}
	if b.format == CSSFormat {
		_, _ = b.content.Text("<style>")
	}
	defer func() {
		if call == nil {
			_ = session.vm.Reset()
			_ = session.vm.SetCoverage(nil)
		}
		clear(session.args)
		session.args = session.args[:0]
		clear(b.tags[:cap(b.tags)])
		b.tags = b.tags[:0]
		b.attr = ""
		b.attrbuf = b.attrbuf[:0]
		clear(b.attributeScratch)
		b.attributeScratch = b.attributeScratch[:0]
		if cap(b.attributeScratch) > 1024 {
			b.attributeScratch = nil
		}
		clear(b.classParts)
		b.classParts = b.classParts[:0]
		if cap(b.classParts) > 4096 {
			b.classParts = nil
		}
		b.classBytes = 0
		b.buf = b.buf[:0]
		b.options = RenderOptions{}
		if cap(b.buf) > 1<<20 {
			b.buf = nil
		}
		if cap(b.attrbuf) > 1<<20 {
			b.attrbuf = nil
		}
		p.pool.Put(session)
	}()
	if b.options.MaxOutputBytes == 0 {
		b.options.MaxOutputBytes = 16 << 20
	}
	if b.options.MaxOutputBytes < 0 {
		return "", fmt.Errorf("negative output limit")
	}
	if call == nil {
		if err := session.vm.SetCoverage(options.Coverage); err != nil {
			return "", err
		}
	}
	session.args = append(session.args, values.Value{})
	in := session.args
	in[0] = values.Value{R: values.NativeAddress, P: unsafe.Pointer(b)}
	in = append(in, args...)
	session.args = in
	var err error
	if call != nil {
		var value values.Value
		value, err = fn.Value(call.Program(), fn.Signature())
		if err == nil {
			err = call.RunCallbackArgs(value, in, nil)
		}
	} else if native {
		signature, ok := p.program.EntrySignature(name)
		if !ok || len(signature.Params) == 0 || signature.Params[0] != BuilderType() || len(signature.Results) != 0 {
			return "", fmt.Errorf("renderer %q is missing", name)
		}
		err = session.vm.RunNative(ctx, func(c *evm.Call) error {
			converted, err := c.NativeArguments(signature.Params[1:], nativeArgs)
			if err != nil {
				return err
			}
			in = append(in, converted...)
			session.args = in
			return c.RunEntry(name, in, nil)
		}, options.Budget)
	} else if fn == nil {
		err = session.vm.RunWithBudget(ctx, name, in, nil, options.Budget)
	} else {
		err = session.vm.RunFunctionWithBudget(ctx, fn, in, nil, options.Budget)
	}
	if err != nil {
		var execution *evm.ExecutionError
		if errors.As(err, &execution) && execution.Position.File != "" {
			pos := execution.Position
			return "", fmt.Errorf("%s:%d:%d: %w", pos.File, pos.Line, pos.Column, err)
		}
		return "", err
	}
	if err := b.flush(); err != nil {
		return "", err
	}
	if len(b.tags) != 0 {
		return "", fmt.Errorf("unclosed output element")
	}
	return string(b.buf), nil
}

type renderSession struct {
	vm      *evm.VM
	builder Builder
	args    []values.Value
}

func defaultReport(d Diagnostic) { slog.Warn("GoX output omitted", "source", d.Error()) }

// Package declares and binds the rendering API for mixed EVM packages.
func Package() evm.Package      { return runtimePackage() }
func BuilderType() *values.Type { return builderPointer }
func HTMLType() *values.Type    { return trustedTypes["HTML"] }

// ParametersFor returns declared inputs, excluding the builder, for a named renderer.
func (p *Program) ParametersFor(name string) []Parameter {
	return append([]Parameter(nil), p.template.FunctionParameters[name]...)
}
