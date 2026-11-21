package evmcompiler

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"slices"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/values"
)

// Environment is a set of host packages prepared for compiling many sources.
//
// NewEnvironment type-checks every package and the standard library once.
// Each compile then checks only its own source against that shared result.
// An Environment never changes after NewEnvironment returns and is safe for
// concurrent use. Host types keep one identity across all of its compiles.
//
// The packages must not change while the Environment is in use.
type Environment struct {
	packages []evm.Package
	// records reports that sources are first checked against the record
	// facade, where rows have fields, and then against row accessors.
	records bool

	// Both are nil in a throwaway environment, which builds a universe for
	// each type check and imports only what the source reaches. The
	// package-list entry points use one to keep their lazy imports.
	facade, real *typeEnvironment
	// files holds the host sources of both universes. Their positions are
	// part of the shared syntax and types, so every compile needs the same
	// files at the same bases; see NewFileSet.
	files []*token.File
}

// NewEnvironment validates and imports the packages. It fails if any package
// cannot be imported, including one that no source would import.
func NewEnvironment(packages ...evm.Package) (env *Environment, err error) {
	defer CatchInternalError(&err)
	env = throwaway(packages)
	files := token.NewFileSet()
	if env.real, err = freeze(env.packages, files, false); err != nil {
		return nil, err
	}
	env.facade = env.real
	if env.records {
		if env.facade, err = freeze(env.packages, files, true); err != nil {
			return nil, err
		}
	}
	files.Iterate(func(f *token.File) bool {
		env.files = append(env.files, f)
		return true
	})
	return env, nil
}

func throwaway(packages []evm.Package) *Environment {
	env := &Environment{packages: slices.Clone(packages)}
	for _, p := range packages {
		if len(p.Records) > 0 {
			env.records = true
			break
		}
	}
	return env
}

// Packages returns the packages the environment was built from.
func (env *Environment) Packages() []evm.Package { return slices.Clone(env.packages) }

// Shapes returns the storage shapes of the host types. The modules the
// environment compiles list a selection of them, the same shapes every time,
// along with shapes of their own. The shapes never change.
func (env *Environment) Shapes() []*values.Shape {
	if env.real == nil {
		return nil
	}
	return slices.Clone(env.real.shapeOrder)
}

// NewLinkEnvironment prepares packages for linking the modules this
// environment compiles. The packages declare what the environment was built
// from and bind the implementations, which a compile does not need. Programs
// linked through the result share the linked form of the host shapes; see
// [evm.LinkOptions].
func (env *Environment) NewLinkEnvironment(packages ...evm.Package) (*evm.LinkEnvironment, error) {
	return evm.LinkOptions{Shapes: env.Shapes()}.NewEnvironment(packages...)
}

// NewFileSet returns a file set for syntax passed to Analyze and AnalyzeFiles.
// It already holds the host sources, so positions of host declarations
// resolve in it. The host files are shared, not copied.
func (env *Environment) NewFileSet() *token.FileSet {
	files := token.NewFileSet()
	files.AddExistingFiles(env.files...)
	return files
}

// Compile compiles one source file. See [Compile].
func (env *Environment) Compile(filename, source string, options Options) (m *evm.Module, err error) {
	defer CatchInternalError(&err)
	return compileTypedFiles(map[string]string{filename: source}, env, options)
}

// CompileFiles compiles one package from named source files. See [CompileFiles].
func (env *Environment) CompileFiles(sources map[string]string, options Options) (m *evm.Module, err error) {
	defer CatchInternalError(&err)
	return compileTypedFiles(sources, env, options)
}

// Analyze checks a frontend's AST. See [Analyze]. The file must have been
// parsed into a file set from NewFileSet.
func (env *Environment) Analyze(files *token.FileSet, file *ast.File, options Options) (*types.Package, *types.Info, error) {
	return env.AnalyzeFiles(files, []*ast.File{file}, options)
}

// AnalyzeFiles checks a package while preserving each file's import scope.
// See [AnalyzeFiles]. The files must have been parsed into a file set from
// NewFileSet.
func (env *Environment) AnalyzeFiles(files *token.FileSet, syntax []*ast.File, options Options) (_ *types.Package, _ *types.Info, err error) {
	defer CatchInternalError(&err)
	return analyzeFiles(files, syntax, env, options)
}

// universe returns the type environment for one type check: an overlay on
// the frozen universe, or a new universe for a throwaway environment.
func (env *Environment) universe(facade bool, files *token.FileSet) (*typeEnvironment, error) {
	if env.real == nil {
		e, err := newTypeEnvironment(env.packages)
		if err != nil {
			return nil, err
		}
		e.files, e.recordFacade = files, facade
		return e, nil
	}
	if len(env.files) != 0 && files.File(token.Pos(env.files[0].Base())) != env.files[0] {
		return nil, fmt.Errorf("the file set does not come from Environment.NewFileSet")
	}
	if facade {
		return env.facade.overlay(files), nil
	}
	return env.real.overlay(files), nil
}

// freeze builds a universe with every package imported and everything that
// depends only on the packages computed, then forbids further changes.
func freeze(packages []evm.Package, files *token.FileSet, facade bool) (*typeEnvironment, error) {
	e, err := newTypeEnvironment(packages)
	if err != nil {
		return nil, err
	}
	e.files, e.recordFacade = files, facade
	// The facade universe declares row fields in the guest package, so that
	// source can select properties with unexported names. No guest package
	// exists yet. go/types compares unexported names by package path, and
	// every guest package has this path, so a stand-in serves them all.
	e.guest = types.NewPackage("plugin", "")
	e.uses = make(map[string]map[string]bool)
	paths := append(evm.StandardLibraryPaths(), slices.Collect(maps.Keys(e.packages))...)
	slices.Sort(paths)
	for _, path := range paths {
		if _, err := e.Import(path); err != nil {
			return nil, fmt.Errorf("import %q: %w", path, err)
		}
	}
	// Dynamic and entry types can name host types no signature mentions.
	// Resolving one can declare it, which must happen before sharing. A type
	// that cannot be resolved fails the compile that reaches it.
	e.lateTypes = make(map[lateType]bool)
	for _, path := range paths {
		decl, pkg := e.packages[path], e.imported[path]
		if decl == nil {
			continue
		}
		for i := range decl.Functions {
			for _, t := range decl.Functions[i].DynamicTypes {
				e.goType(t, pkg)
			}
		}
		for i := range decl.Methods {
			for _, t := range decl.Methods[i].DynamicTypes {
				e.goType(t, pkg)
			}
		}
		for _, t := range decl.EntryTypes {
			e.goType(t, pkg)
		}
	}
	if len(e.importErrors) != 0 {
		path := slices.Sorted(maps.Keys(e.importErrors))[0]
		return nil, fmt.Errorf("import %q: %w", path, e.importErrors[path])
	}
	if len(e.pendingNativeStorage) != 0 || len(e.pendingSourceChecks) != 0 || len(e.pendingMapKeys) != 0 {
		return nil, fmt.Errorf("internal error: unfinished host declarations")
	}
	e.pendingNativeStorage, e.pendingSourceChecks, e.pendingMapKeys = nil, nil, nil

	for _, path := range slices.Sorted(maps.Keys(e.imported)) {
		e.importOrder = append(e.importOrder, e.imported[path])
	}
	e.sourceTexts = make(map[*types.Package]map[string]string)
	e.dereferenceWrites = make(map[token.Pos]bool)
	check := &typedCompiler{env: e, files: files, dereferenceWrites: e.dereferenceWrites, addedLines: map[string]int{}}
	for _, path := range slices.Sorted(maps.Keys(e.standard)) {
		std := e.standard[path]
		texts := make(map[string]string, len(std.files))
		for i, file := range std.files {
			texts[files.PositionFor(file.Pos(), false).Filename] = std.sources[i].Text
			check.recordDereferenceWrites(file)
		}
		e.sourceTexts[e.imported[path]] = texts
		check.checkRecordDerivedTypes(std.info)
	}
	if len(check.errors) > 0 {
		return nil, check.errors
	}
	e.completeTypes()
	e.computeReach()
	e.uses, e.using = nil, nil
	for _, std := range e.standard {
		if std.declarationsOnly {
			// Only packages with function bodies are built again per compile.
			std.files, std.info = nil, nil
		}
	}
	e.frozen = true
	return e, nil
}

// computeReach records, for each package, the packages a source importing it
// can use without importing them: those its sources import, and those whose
// types its declarations name, transitively. A lazily built environment
// imports those packages and no others. A compile selects the same packages
// from the frozen universe: it builds SSA for them and lists their shapes,
// not those of the whole host.
func (e *typeEnvironment) computeReach() {
	index := make(map[*types.Package]int, len(e.importOrder))
	for i, p := range e.importOrder {
		index[p] = i
	}
	// named adds the packages whose types t names. It looks inside the
	// declaration of a named type only if own declares it; another package
	// accounts for its own declarations.
	named := func(set packageSet, own *types.Package, seen map[types.Type]bool, t types.Type) {
		var visit func(types.Type)
		tuple := func(t *types.Tuple) {
			for v := range t.Variables() {
				visit(v.Type())
			}
		}
		owner := func(obj *types.TypeName) bool {
			i, ok := index[obj.Pkg()]
			if ok {
				set.add(i)
			}
			return obj.Pkg() == own
		}
		visit = func(t types.Type) {
			if t == nil || seen[t] {
				return
			}
			seen[t] = true
			switch t := t.(type) {
			case *types.Alias:
				owner(t.Obj())
				for arg := range t.TypeArgs().Types() {
					visit(arg)
				}
				visit(types.Unalias(t))
			case *types.Named:
				for arg := range t.TypeArgs().Types() {
					visit(arg)
				}
				if owner(t.Obj()) {
					visit(t.Underlying())
					for m := range t.Methods() {
						visit(m.Type())
					}
				}
			case *types.TypeParam:
				visit(t.Constraint())
			case *types.Pointer:
				visit(t.Elem())
			case *types.Slice:
				visit(t.Elem())
			case *types.Array:
				visit(t.Elem())
			case *types.Chan:
				visit(t.Elem())
			case *types.Map:
				visit(t.Key())
				visit(t.Elem())
			case *types.Struct:
				for f := range t.Fields() {
					visit(f.Type())
				}
			case *types.Tuple:
				tuple(t)
			case *types.Signature:
				if t.Recv() != nil {
					visit(t.Recv().Type())
				}
				tuple(t.Params())
				tuple(t.Results())
			case *types.Union:
				for term := range t.Terms() {
					visit(term.Type())
				}
			case *types.Interface:
				for embedded := range t.EmbeddedTypes() {
					visit(embedded)
				}
				for m := range t.Methods() {
					visit(m.Type())
				}
			}
		}
		visit(t)
	}

	reach := make([]packageSet, len(e.importOrder))
	for i, p := range e.importOrder {
		set := newPackageSet(len(e.importOrder))
		set.add(i)
		for _, imported := range p.Imports() {
			if j, ok := index[imported]; ok {
				set.add(j)
			}
		}
		for path := range e.uses[p.Path()] {
			if j, ok := index[e.imported[path]]; ok {
				set.add(j)
			}
		}
		seen := make(map[types.Type]bool)
		scope := p.Scope()
		for _, name := range scope.Names() {
			named(set, p, seen, scope.Lookup(name).Type())
		}
		if decl := e.packages[p.Path()]; decl != nil {
			// Support types, dynamic results and entry arguments are types of
			// the package that none of its exported declarations need name.
			for _, d := range decl.Types {
				named(set, p, seen, e.goOf(d.Shape.Type))
			}
			for i := range decl.Functions {
				for _, t := range decl.Functions[i].DynamicTypes {
					named(set, p, seen, e.goOf(t))
				}
			}
			for i := range decl.Methods {
				for _, t := range decl.Methods[i].DynamicTypes {
					named(set, p, seen, e.goOf(t))
				}
			}
			for _, t := range decl.EntryTypes {
				named(set, p, seen, e.goOf(t))
			}
		}
		reach[i] = set
	}
	for changed := true; changed; {
		changed = false
		for _, set := range reach {
			for j := range e.importOrder {
				if set.has(j) && set.addAll(reach[j]) {
					changed = true
				}
			}
		}
	}
	e.reach = make(map[*types.Package]packageSet, len(e.importOrder))
	for i, p := range e.importOrder {
		e.reach[p] = reach[i]
	}

	byShape := make(map[*values.Shape]packageSet, e.shapes.Len())
	e.shapes.Iterate(func(t types.Type, shape any) {
		set := newPackageSet(len(e.importOrder))
		named(set, nil, make(map[types.Type]bool), t)
		byShape[shape.(*values.Shape)] = set
	})
	e.shapePackages = make([]packageSet, len(e.shapeOrder))
	for i, s := range e.shapeOrder {
		e.shapePackages[i] = byShape[s] // A nil set is within every set.
	}
}

// completeTypes finishes what go/types computes on first use, so that
// concurrent compiles only read the shared types. go/types guards lazy
// expansion of instantiated types with a mutex, and documents completed
// interfaces as safe for concurrent use. An interface produced by expanding
// an instance is not completed until its type set is first needed, so walk
// every type the universe can hand out and complete it.
func (e *typeEnvironment) completeTypes() {
	seen := make(map[types.Type]bool)
	var visit func(types.Type)
	tuple := func(t *types.Tuple) {
		for v := range t.Variables() {
			visit(v.Type())
		}
	}
	params := func(list *types.TypeParamList) {
		for p := range list.TypeParams() {
			visit(p)
		}
	}
	visit = func(t types.Type) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		switch t := t.(type) {
		case *types.Alias:
			params(t.TypeParams())
			visit(t.Rhs())
			visit(types.Unalias(t))
		case *types.Named:
			params(t.TypeParams())
			for arg := range t.TypeArgs().Types() {
				visit(arg)
			}
			visit(t.Origin())
			visit(t.Underlying())
			for m := range t.Methods() {
				visit(m.Type())
			}
		case *types.TypeParam:
			visit(t.Constraint())
			visit(t.Underlying())
		case *types.Pointer:
			visit(t.Elem())
		case *types.Slice:
			visit(t.Elem())
		case *types.Array:
			visit(t.Elem())
		case *types.Chan:
			visit(t.Elem())
		case *types.Map:
			visit(t.Key())
			visit(t.Elem())
		case *types.Struct:
			for f := range t.Fields() {
				visit(f.Type())
			}
		case *types.Tuple:
			tuple(t)
		case *types.Signature:
			params(t.RecvTypeParams())
			params(t.TypeParams())
			if t.Recv() != nil {
				visit(t.Recv().Type())
			}
			tuple(t.Params())
			tuple(t.Results())
		case *types.Union:
			for term := range t.Terms() {
				visit(term.Type())
			}
		case *types.Interface:
			t.Complete()
			for embedded := range t.EmbeddedTypes() {
				visit(embedded)
			}
			for m := range t.Methods() {
				visit(m.Type())
			}
		}
	}
	object := func(obj types.Object) {
		if obj != nil {
			visit(obj.Type())
		}
	}
	for _, pkg := range e.imported {
		scope := pkg.Scope()
		for _, name := range scope.Names() {
			object(scope.Lookup(name))
		}
	}
	for _, pkg := range e.declared {
		scope := pkg.Scope()
		for _, name := range scope.Names() {
			object(scope.Lookup(name))
		}
	}
	for _, t := range e.toGo {
		visit(t)
	}
	e.fromGo.Iterate(func(t types.Type, _ any) { visit(t) })
	e.shapes.Iterate(func(t types.Type, _ any) { visit(t) })
	for _, std := range e.standard {
		for _, tv := range std.info.Types {
			visit(tv.Type)
		}
		for _, obj := range std.info.Defs {
			object(obj)
		}
		for _, obj := range std.info.Uses {
			object(obj)
		}
		for _, obj := range std.info.Implicits {
			object(obj)
		}
		for _, instance := range std.info.Instances {
			visit(instance.Type)
			for arg := range instance.TypeArgs.Types() {
				visit(arg)
			}
		}
		for _, selection := range std.info.Selections {
			visit(selection.Recv())
			visit(selection.Type())
		}
	}
}
