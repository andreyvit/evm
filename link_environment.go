package evm

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"

	"github.com/andreyvit/evm/values"
)

// LinkEnvironment is a set of bound host packages prepared for linking many
// modules.
//
// NewLinkEnvironment validates the packages and resolves their bindings
// once. Each link then does only the work its module needs, and the programs
// share what the packages alone determine. A LinkEnvironment never changes
// after NewLinkEnvironment returns and is safe for concurrent use.
//
// The packages must not change while the environment or its programs are in
// use.
type LinkEnvironment struct {
	defaultLimits Limits
	packages      []Package // with the time package

	// The first error of each stage of preparing the packages. A link
	// reports them in the order a link from a package list always has,
	// between its checks of the module.
	packagesErr, codecsErr, bindingsErr, nativeSlicesErr error

	shapes        *sharedShapes // nil unless LinkOptions.Shapes listed some
	nativeCodecs  map[*values.Type]*NativeValueCodec
	records       map[*RecordType]bool
	recordResults map[*values.Type]*RecordType
	pairResults   map[*values.Type]bool
	hostKinds     map[*values.Type]values.Repr
	exports       map[exportKey]linkExport

	nativeSlices            []nativeSliceType // without shapes, which belong to programs
	nativeSliceLayouts      map[*values.NativeLayout]*NativeSliceDecl
	nativeSliceLogicalTypes map[nativeSliceLogicalKey]reflect.Type

	// natives holds every binding a prepared environment resolved. Programs
	// copy the ones their bytecode calls and share the table for the ones
	// only their method tables name; see link_methods.go.
	natives   []linkedNative
	methods   map[*Import]*sharedMethods // by the first import of a list
	methodIDs map[string]uint32
}

type exportKey struct {
	path, name string
	receiver   *values.Type
}

// linkExport is a native binding a module can import: its declaration, with
// the receiver first for a method, or the form that enters a program.
//
// A prepared environment resolves every binding that is not generic and
// drops the declaration. A generic binding is resolved for the type
// arguments of each import, and a throwaway environment resolves the
// bindings its one module imports.
type linkExport struct {
	declared *ExternalFunction
	resolved *linkedNative  // in LinkEnvironment.natives
	errs     *resolveErrors // nil unless resolving failed
}

// resolveErrors are the two failures of resolving a binding. A link compares
// the import with the binding between reporting them.
type resolveErrors struct{ ownership, layout error }

// LinkOptions configures a prepared link environment.
type LinkOptions struct {
	// DefaultLimits supplies the initial budget for each VM. Nil selects the
	// built-in limits. An explicit empty Limits disables the optional quotas.
	DefaultLimits *Limits
	// Shapes lists storage shapes that the modules to link name: usually
	// every shape of the compile environment that produces the modules. The
	// environment snapshots them once and its programs share the snapshots.
	// A program snapshots for itself any other shape its module names.
	// The shapes must not change while the environment is in use.
	Shapes []*values.Shape
	// Methods lists the method tables that the modules to link share, one
	// for each compile environment that produces them. The environment
	// binds them once and its programs share the result. A program binds
	// for itself the method tables of any other module.
	Methods []MethodTables
}

// Link links one module using these options.
func (o LinkOptions) Link(m *Module, packages ...Package) (*Program, error) {
	if len(o.Shapes) == 0 && len(o.Methods) == 0 {
		e := newLinkEnvironment(packages, false)
		if o.DefaultLimits != nil {
			if err := o.DefaultLimits.validate(); err != nil {
				return nil, err
			}
			e.defaultLimits = *o.DefaultLimits
		}
		return e.Link(m)
	}
	e, err := o.NewEnvironment(packages...)
	if err != nil {
		return nil, err
	}
	return e.Link(m)
}

// NewLinkEnvironment validates and binds the packages. It fails where every
// link against them would fail.
func NewLinkEnvironment(packages ...Package) (*LinkEnvironment, error) {
	return LinkOptions{}.NewEnvironment(packages...)
}

// NewEnvironment prepares the packages for linking; see [NewLinkEnvironment].
func (o LinkOptions) NewEnvironment(packages ...Package) (*LinkEnvironment, error) {
	e := newLinkEnvironment(packages, true)
	if o.DefaultLimits != nil {
		if err := o.DefaultLimits.validate(); err != nil {
			return nil, err
		}
		e.defaultLimits = *o.DefaultLimits
	}
	if err := cmp.Or(e.packagesErr, e.codecsErr, e.bindingsErr, e.nativeSlicesErr); err != nil {
		return nil, err
	}
	if len(o.Shapes) != 0 {
		e.shapes = newSharedShapes(o.Shapes)
	}
	if len(o.Methods) != 0 {
		e.prepareMethods(o.Methods)
	}
	return e, nil
}

// Packages returns the packages the environment was built from, including
// the built-in time package.
func (e *LinkEnvironment) Packages() []Package { return slices.Clone(e.packages) }

// Link binds a module to the packages. It does not run it.
func (e *LinkEnvironment) Link(m *Module) (*Program, error) { return e.link(m, nil) }

// LinkWithHostData links like Link and retains host-owned metadata for the
// lifetime of the program; see [LinkWithHostData].
func (e *LinkEnvironment) LinkWithHostData(m *Module, hostData any) (*Program, error) {
	return e.link(m, hostData)
}

// newLinkEnvironment prepares the packages and keeps the errors for link to
// report. A prepared environment resolves every binding now; a throwaway one,
// built for a single link, resolves only what its module imports.
func newLinkEnvironment(packages []Package, prepared bool) *LinkEnvironment {
	e := &LinkEnvironment{defaultLimits: defaultExecutionLimits}
	e.packages, e.packagesErr = WithTimePackage(packages)
	if e.packagesErr != nil {
		return e
	}
	e.codecsErr = e.prepareCodecs()
	e.records = make(map[*RecordType]bool)
	e.recordResults = recordResults(e.packages)
	e.pairResults = make(map[*values.Type]bool)
	for _, pkg := range e.packages {
		for _, r := range pkg.Records {
			if r != nil {
				e.records[r] = true
			}
		}
		for _, decl := range pkg.Types {
			if decl.Shape != nil && decl.Shape.Kind == values.TypedPairKind {
				e.pairResults[decl.Shape.Type] = true
			}
		}
	}
	e.hostKinds = hostTypes(e.packages)
	if e.codecsErr != nil {
		return e
	}
	e.bindingsErr = e.prepareBindings()
	if e.bindingsErr != nil {
		return e
	}
	if prepared {
		count := 0
		for _, x := range e.exports {
			if x.declared.Instantiate == nil {
				count++
			}
		}
		e.natives = make([]linkedNative, 0, count)
		for key, x := range e.exports {
			if x.declared.Instantiate == nil {
				e.natives = append(e.natives, linkedNative{})
				resolved := &e.natives[len(e.natives)-1]
				if errs, ok := e.resolveQuietly(resolved, *x.declared, key.receiver != nil); ok {
					e.exports[key] = linkExport{resolved: resolved, errs: errs}
				} else {
					e.natives = e.natives[:len(e.natives)-1]
				}
			}
		}
		// Programs share the table: an append must never write into it.
		e.natives = slices.Clip(e.natives)
	}
	e.nativeSlicesErr = e.prepareNativeSlices()
	return e
}

func (e *LinkEnvironment) prepareCodecs() error {
	e.nativeCodecs = make(map[*values.Type]*NativeValueCodec)
	for _, pkg := range e.packages {
		for _, d := range pkg.Types {
			if d.NativeCodec == nil {
				continue
			}
			if d.Shape == nil || d.Shape.Type == nil || d.NativeCodec.Decode == nil || d.NativeCodec.Encode == nil {
				return fmt.Errorf("invalid native value codec")
			}
			if old := e.nativeCodecs[d.Shape.Type]; old != nil && old != d.NativeCodec {
				return fmt.Errorf("conflicting native value codecs for %v", d.Shape.Type)
			}
			e.nativeCodecs[d.Shape.Type] = d.NativeCodec
		}
	}
	for typ, codec := range e.nativeCodecs {
		copy := *codec
		e.nativeCodecs[typ] = &copy
	}
	return nil
}

func (e *LinkEnvironment) prepareBindings() error {
	if err := validateDirectBindings(e.packages, e.hostKinds); err != nil {
		return err
	}
	e.exports = make(map[exportKey]linkExport)
	paths := make(map[string]bool)
	for i := range e.packages {
		pkg := &e.packages[i]
		if _, reserved := StandardLibrarySource(pkg.Path); reserved || pkg.Path == "evm/debug" {
			return fmt.Errorf("reserved virtual package %q", pkg.Path)
		}
		if pkg.Path == "" || paths[pkg.Path] {
			return fmt.Errorf("empty or duplicate package %q", pkg.Path)
		}
		paths[pkg.Path] = true
		receivers := pkg.methodReceivers()
		for i := range pkg.Methods {
			m := &pkg.Methods[i]
			if !receivers.owns(m.Receiver) {
				return fmt.Errorf("method receiver is not declared by package %s", pkg.Path)
			}
			key := exportKey{pkg.Path, m.Name, m.Receiver}
			if m.Receiver == nil || m.Name == "" || m.Call == nil && m.Direct.Call == nil && m.Instantiate == nil {
				return fmt.Errorf("invalid native method")
			}
			if _, ok := e.exports[key]; ok {
				return fmt.Errorf("duplicate native method")
			}
			if m.Direct.Op != 0 && m.Direct.Call == nil {
				return fmt.Errorf("unbound direct method %s.%s", pkg.Path, m.Name)
			}
			binding := m.Binding()
			e.exports[key] = linkExport{declared: &binding}
		}

		for _, v := range pkg.Values {
			key := exportKey{pkg.Path, v.LinkName(), nil}
			if _, exists := e.exports[key]; exists || v.Name == "" || v.Type == nil || v.Read == nil {
				return fmt.Errorf("invalid or duplicate package value %s.%s", pkg.Path, v.Name)
			}
			e.exports[key] = linkExport{declared: &ExternalFunction{Name: v.LinkName(), Signature: Signature{Results: []*values.Type{v.Type}}, Call: v.Read}}
		}

		for i := range pkg.Functions {
			f := &pkg.Functions[i]
			key := exportKey{pkg.Path, f.LinkName(), nil}
			if _, ok := e.exports[key]; ok || f.Name == "" {
				return fmt.Errorf("invalid or duplicate external function %s.%s", pkg.Path, f.Name)
			}
			if f.Direct.Op != 0 && f.Direct.Call == nil {
				return fmt.Errorf("unbound direct function %s.%s", pkg.Path, f.Name)
			}
			if f.Call == nil && f.Direct.Call == nil && f.Instantiate == nil {
				return fmt.Errorf("%s.%s needs a native binding", pkg.Path, f.Name)
			}
			e.exports[key] = linkExport{declared: f}
		}
	}
	return nil
}

// resolve copies what a program keeps of a binding into dst and derives its
// call layout. It reads only the environment's tables.
func (e *LinkEnvironment) resolve(dst *linkedNative, f ExternalFunction, method bool) (ownershipErr, layoutErr error) {
	*dst = linkedNative{ExternalFunction: f}
	if err := f.Ownership.Validate(f.Signature); err != nil {
		return err, nil
	}
	f.Ownership = f.Ownership.Clone()
	f.Signature = copySignature(f.Signature)
	dst.ExternalFunction = f
	args, err := ParameterLayout(f.Signature.Params, method)
	if err != nil {
		return nil, err
	}
	results, err := LayoutOf(f.Signature.Results)
	if err != nil {
		return nil, err
	}
	g := linkNative(f, args, results)
	if g.resultString {
		g.resultString = !e.pairResults[f.Signature.Results[0]]
	}
	if g.form != nil && (g.form.result == values.Pointer || g.form.result == values.Pair) {
		if record := e.recordResults[f.Signature.Results[0]]; record != nil {
			snapshot := *record
			g.record = &snapshot
			g.resultString = false
		}
	}
	*dst = g
	return nil, nil
}

// resolveQuietly resolves a binding no module may ever import. If that
// panics, it reports false and the link that imports the binding resolves it
// again.
func (e *LinkEnvironment) resolveQuietly(dst *linkedNative, f ExternalFunction, method bool) (errs *resolveErrors, ok bool) {
	defer func() {
		if recover() != nil {
			errs, ok = nil, false
		}
	}()
	if ownership, layout := e.resolve(dst, f, method); ownership != nil || layout != nil {
		return &resolveErrors{ownership, layout}, true
	}
	return nil, true
}

func (e *LinkEnvironment) prepareNativeSlices() error {
	e.nativeSliceLayouts = make(map[*values.NativeLayout]*NativeSliceDecl)
	e.nativeSliceLogicalTypes = make(map[nativeSliceLogicalKey]reflect.Type)
	logical := make(map[reflect.Type]*values.Type)
	for _, pkg := range e.packages {
		for _, decl := range pkg.Types {
			if decl.NativeSlice == nil {
				continue
			}
			if err := validateModelLayout(decl.NativeSlice.Layout, make(map[*values.NativeLayout]bool)); err != nil {
				return err
			}
			if decl.Shape.Kind != values.SliceKind || decl.NativeSlice.Layout.Slice == nil || decl.NativeSlice.Element == nil || decl.NativeSlice.Element != decl.NativeSlice.Layout.Slice.Element {
				return fmt.Errorf("native slice declaration layout mismatch")
			}
			if err := validateModelShape(decl.Shape, decl.NativeSlice.Layout); err != nil {
				return err
			}
			goType := decl.NativeSlice.Layout.GoType
			if old, ok := logical[goType]; ok && old != decl.Shape.Type {
				return fmt.Errorf("native slice %s has conflicting logical types", goType)
			}
			logical[goType] = decl.Shape.Type
			seen := make(map[*values.NativeLayout]*values.NativeLayout)
			binding := &NativeSliceDecl{Layout: snapshotNativeLayout(decl.NativeSlice.Layout, seen), Element: snapshotNativeLayout(decl.NativeSlice.Element, seen)}
			e.nativeSlices = append(e.nativeSlices, nativeSliceType{logical: decl.Shape.Type, decl: binding})
			e.nativeSliceLayouts[decl.NativeSlice.Layout] = binding
			e.nativeSliceLogicalTypes[nativeSliceLogicalKey{decl.Shape.Type, goType.Elem()}] = goType
		}
	}
	return nil
}
