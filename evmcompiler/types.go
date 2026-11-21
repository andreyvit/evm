package evmcompiler

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"math"
	"path"
	"reflect"
	"slices"
	"strings"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/values"
	"golang.org/x/tools/go/types/typeutil"
)

var goBasicValues = map[types.BasicKind]*values.Type{
	types.Bool: values.Boolean, types.Int: values.IntType, types.Int8: values.Int8, types.Int16: values.Int16, types.Int32: values.Int32, types.Int64: values.Int64,
	types.Uint: values.UintType, types.Uint8: values.Uint8, types.Uint16: values.Uint16, types.Uint32: values.Uint32, types.Uint64: values.Uint64, types.Uintptr: values.UintptrType,
	types.Float32: values.Float32Type, types.Float64: values.Float64, types.String: values.StringType,
}

type lateType struct {
	pkg  *types.Package
	name string
}

type sourceInstanceCheck struct {
	generic types.Type
	args    []types.Type
}

type typeEnvironment struct {
	// base is the frozen environment this one extends; see overlay.go.
	base *typeEnvironment
	// frozen environments never import or declare: every package was
	// imported before freezing. A frozen environment is read concurrently.
	frozen bool

	// Derived from the package declarations alone. Never written after
	// newTypeEnvironment returns.
	packages            map[string]*evm.Package
	nativeTypes         map[*values.Type]nativeType
	unnamedNativeTypes  []*values.Type
	recordOwners        map[*values.Type]string
	recordValues        map[*values.Type]*evm.RecordType
	readRecords         map[*values.Type]bool // record pointer to "is read-only"
	nativeMethodsByBase map[*values.Type][]*evm.ExternalMethod
	sharedOwners        map[*values.Type]string
	nativeAllocations   map[*values.Type]*values.NativeLayout
	collectionLayouts   map[nativeCollectionKey]*values.NativeLayout
	nativeViewLeaves    map[*values.Type]bool

	// Written while importing packages. An overlay shares these with its base.
	declared                map[string]*types.Package
	imported                map[string]*types.Package
	importErrors            map[string]error
	standard                map[string]*standardPackage
	natives                 map[*types.Func]nativeBinding
	nativeReceiverMethods   map[nativeReceiverMethod]nativeBinding
	genericNativeDecls      map[*types.Func]bool
	attachedNativeMethods   map[*evm.ExternalMethod]bool
	attachedNativeFunctions map[*types.Func]bool
	recordOperations        map[string]string // internal identifier to public type selector
	recordFields            map[*types.Var]recordProperty
	recordMethods           map[*types.Func]recordProperty
	recordFacade            bool

	// lateTypes are the types declared while freezing resolved dynamic and
	// entry types; see goType.
	lateTypes map[lateType]bool

	// Set when an environment is frozen; see freeze in environment.go.
	importOrder       []*types.Package              // by path
	reach             map[*types.Package]packageSet // what a source importing the key can use
	shapePackages     []packageSet                  // by shapeOrder index: the packages whose types the shape's type names
	sourceTexts       map[*types.Package]map[string]string
	dereferenceWrites map[token.Pos]bool
	// The method tables its modules share; see method_tables.go.
	methodImports  []evm.Import
	methodBindings []*evm.ExternalFunction // by method import
	methodNumbers  map[importKey]uint32
	hostMethods    map[*values.Shape][]evm.DynamicMethod

	// Written while importing and while compiling. An overlay has its own
	// and reads the base through the accessors in overlay.go.
	toGo             map[*values.Type]types.Type
	fromGo           typeutil.Map
	shapes           typeutil.Map
	shapeOrder       []*values.Shape
	nativeLayouts    map[*values.Type]*values.NativeLayout
	syntheticTypes   map[*values.Shape]reflect.Type
	nativeSourceMemo map[*values.Type]bool
	guest            *types.Package
	files            *token.FileSet

	// uses records, while a universe is built for freezing, which packages
	// each package imports, declares or takes types from as it is imported.
	// A lazily built environment imports those along with it.
	uses  map[string]map[string]bool
	using []string // the packages being imported or declared, innermost last

	// Work in progress.
	pendingSourceChecks  []sourceInstanceCheck
	pendingMapKeys       []types.Type
	pendingNativeStorage []nativeStorageShape
	importDepth          int
	importing            map[string]bool
	shaping              map[*values.Shape]bool
	buildingShapes       map[*values.Shape]bool
	resolving            map[*values.Type]bool
}

func newTypeEnvironment(packages []evm.Package) (*typeEnvironment, error) {
	packages, err := evm.WithTimePackage(packages)
	if err != nil {
		return nil, err
	}
	if err := evm.ValidateDirectBindings(packages); err != nil {
		return nil, err
	}
	e := &typeEnvironment{nativeAllocations: map[*values.Type]*values.NativeLayout{}, nativeLayouts: map[*values.Type]*values.NativeLayout{}, recordOwners: make(map[*values.Type]string), readRecords: make(map[*values.Type]bool), recordValues: make(map[*values.Type]*evm.RecordType), recordMethods: make(map[*types.Func]recordProperty), recordFields: make(map[*types.Var]recordProperty), standard: make(map[string]*standardPackage), buildingShapes: make(map[*values.Shape]bool), nativeTypes: make(map[*values.Type]nativeType), natives: make(map[*types.Func]nativeBinding), resolving: make(map[*values.Type]bool), packages: make(map[string]*evm.Package), imported: make(map[string]*types.Package), toGo: make(map[*values.Type]types.Type)}
	for k, v := range goBasicValues {
		e.toGo[v] = types.Typ[k]
		e.fromGo.Set(types.Typ[k], v)
	}
	e.toGo[values.AnyType] = types.NewInterfaceType(nil, nil).Complete()
	e.fromGo.Set(e.goOf(values.AnyType), values.AnyType)
	e.toGo[values.ErrorType] = types.Universe.Lookup("error").Type()
	e.fromGo.Set(e.goOf(values.ErrorType), values.ErrorType)
	e.toGo[evm.ErrorSliceType] = types.NewSlice(e.goOf(values.ErrorType))
	e.fromGo.Set(e.goOf(evm.ErrorSliceType), evm.ErrorSliceType)
	for i := range packages {
		p := &packages[i]
		if err := validateSourceFiles(p.Path, p.Sources()); err != nil {
			return nil, err
		}
		if _, reserved := evm.StandardLibrarySource(p.Path); reserved || p.Path == "evm/debug" {
			return nil, fmt.Errorf("reserved virtual package %q", p.Path)
		}
		if p.Path == "" {
			return nil, fmt.Errorf("empty external package path")
		}
		if _, ok := e.packages[p.Path]; ok {
			return nil, fmt.Errorf("duplicate package %q", p.Path)
		}
		aliasNames := map[string]bool{}
		for _, a := range p.Aliases {
			if !token.IsIdentifier(a.Name) || a.Name == "_" || a.Target == nil {
				return nil, fmt.Errorf("invalid alias %s.%s", p.Path, a.Name)
			}
			if aliasNames[a.Name] {
				return nil, fmt.Errorf("duplicate alias %s.%s", p.Path, a.Name)
			}
			aliasNames[a.Name] = true
		}
		for i := range p.Functions {
			f := &p.Functions[i]
			if _, err := evm.LayoutOf(f.Signature.Params); err != nil {
				return nil, fmt.Errorf("%s.%s parameters: %w", p.Path, f.Name, err)
			}
			if _, err := evm.LayoutOf(f.Signature.Results); err != nil {
				return nil, fmt.Errorf("%s.%s results: %w", p.Path, f.Name, err)
			}
		}
		for _, r := range p.Records {
			if r == nil || r.Model == nil || r.Pointer == nil || r.Value == nil {
				return nil, fmt.Errorf("incomplete record declaration")
			}
			if _, ok := e.recordOwners[r.Pointer]; ok {
				return nil, fmt.Errorf("duplicate record pointer declaration")
			}
			if r.NativeAllocation != nil {
				entity, ok := r.Model.Type().(*kvo.EntityType)
				if r.Mutable || !ok || !slices.Contains(entity.NativeTypes(), r.NativeAllocation) {
					return nil, fmt.Errorf("invalid native allocation type %v for model %s", r.NativeAllocation, r.Name)
				}
			}
			e.recordOwners[r.Pointer] = p.Path
			e.recordOwners[r.Value] = p.Path
			e.recordValues[r.Value] = r
			e.readRecords[r.Pointer] = !r.Mutable
		}
		for _, d := range p.Types {
			if d.SourceName != "" && !p.HasSource() {
				return nil, fmt.Errorf("source type %s requires package source", d.SourceName)
			}
			if d.Shape == nil || d.Shape.Type == nil {
				return nil, fmt.Errorf("missing native type shape")
			}
			if err := evm.ValidateShape(d.Shape); err != nil {
				return nil, err
			}
			t := d.Shape.Type
			if old, exists := e.nativeTypes[t]; exists {
				if d.Name == "" && old.decl.Name == "" && e.sameNativeLayout(old.decl.Shape, d.Shape) {
					continue
				}
				return nil, fmt.Errorf("duplicate native type")
			}
			if e.goOf(t) != nil {
				// Native builtin interfaces add storage codecs, not new Go types.
				if d.Name == "" && d.Shape.Kind == values.InterfaceKind && ((t == values.AnyType && d.NativeCodec == evm.NativeAnyCodec && len(d.Shape.Methods) == 0) || (t == values.ErrorType && d.NativeCodec == evm.NativeErrorCodec)) {
					continue
				}
				return nil, fmt.Errorf("cannot redefine builtin type")
			}
			e.nativeTypes[t] = nativeType{p.Path, d}
			if d.Name == "" && d.SourceName == "" {
				e.unnamedNativeTypes = append(e.unnamedNativeTypes, t)
			}
		}
		var collect func(*values.Shape)
		seen := map[*values.Shape]bool{}
		collect = func(s *values.Shape) {
			if s == nil || seen[s] {
				return
			}
			seen[s] = true
			if s.Native != nil {
				e.nativeLayouts[s.Type] = s.Native
			}
			for _, f := range s.Fields {
				collect(f.Shape)
			}
			collect(s.Elem)
			collect(s.Key)
		}
		for _, d := range p.Types {
			collect(d.Shape)
		}
		e.packages[p.Path] = p
	}
	for _, p := range packages {
		for _, r := range p.Records {
			if r.NativeAllocation != nil {
				e.nativeAllocations[r.Pointer] = evm.NativeModelPointerLayout(r.Model, r.NativeAllocation)
				e.nativeLayouts[r.Value] = evm.NativeModelValueLayout(r.Model, r.NativeAllocation)
				e.nativeAllocations[r.Value] = e.nativeLayout(r.Value)
			}
		}
	}
	e.collectNativeSlots(packages)
	e.collectNativeViewLeaves()
	e.nativeSourceMemo = make(map[*values.Type]bool)
	e.nativeMethodsByBase = make(map[*values.Type][]*evm.ExternalMethod)
	e.sharedOwners = make(map[*values.Type]string)
	e.attachedNativeMethods = make(map[*evm.ExternalMethod]bool)
	e.attachedNativeFunctions = make(map[*types.Func]bool)
	for path, pkg := range e.packages {
		for _, shared := range pkg.SharedTypes {
			e.sharedOwners[shared.Type] = path
		}
		for i := range pkg.Methods {
			m := &pkg.Methods[i]
			base := e.nativeReceiverBase(m.Receiver)
			e.nativeMethodsByBase[base] = append(e.nativeMethodsByBase[base], m)
		}
	}
	return e, nil
}
func (e *typeEnvironment) Import(p string) (_ *types.Package, importErr error) {
	if e.frozen {
		return e.frozenPackage(p)
	}
	defer func() {
		if importErr != nil && e.importDepth == 0 {
			e.pendingSourceChecks = nil
		}
	}()
	pkg, err := e.importPackage(p)
	if err != nil {
		return nil, err
	}
	if e.importing[p] || !pkg.Complete() {
		return nil, fmt.Errorf("import cycle through %q", p)
	}
	if e.importDepth != 0 {
		return pkg, nil
	}
	// Record identities can be referenced without importing their owner's source.
	// Complete those owners once the referring package is ready for source checking.
	for {
		var pending []string
		for path := range e.declared {
			if e.imported[path] == nil {
				pending = append(pending, path)
			}
		}
		if len(pending) == 0 {
			break
		}
		slices.Sort(pending)
		for _, path := range pending {
			if _, err := e.importPackage(path); err != nil {
				return nil, err
			}
		}
	}
	// Native declarations can refer to a named key before its underlying type
	// is defined. Check Go comparability after the whole import graph is ready.
	for _, key := range e.pendingMapKeys {
		if !types.Comparable(key) {
			return nil, fmt.Errorf("invalid map key %s", key)
		}
	}
	e.pendingMapKeys = nil
	// Constraint methods may belong to a package whose types were declared first.
	checks := e.pendingSourceChecks
	e.pendingSourceChecks = nil
	for _, check := range checks {
		if _, err := types.Instantiate(nil, check.generic, check.args, true); err != nil {
			return nil, fmt.Errorf("invalid source instance %s: %w", check.generic, err)
		}
	}
	e.pendingSourceChecks = nil
	return pkg, nil
}
func (e *typeEnvironment) importPackage(p string) (_ *types.Package, importErr error) {
	if e.frozen {
		return e.frozenPackage(p)
	}
	if e.uses != nil {
		defer e.enter(p)()
	}
	e.importDepth++
	defer func() { e.importDepth-- }()
	if err := e.importErrors[p]; err != nil {
		return nil, err
	}
	defer func() {
		if importErr != nil {
			delete(e.imported, p)
			if e.importErrors == nil {
				e.importErrors = make(map[string]error)
			}
			e.importErrors[p] = importErr
		}
	}()
	if pkg := e.imported[p]; pkg != nil {
		return pkg, nil
	}
	decl, ok := e.packages[p]
	if !ok {
		if source, exists := evm.StandardLibrarySource(p); exists {
			return e.importStandard(p, source)
		}
		return nil, fmt.Errorf("package %q is not exposed", p)
	}
	pkg, err := e.declarePackage(p)
	if err != nil {
		return nil, err
	}
	// Defining record properties can import shared scalar packages, whose
	// source can in turn finish importing this package.
	if imported := e.imported[p]; imported != nil {
		return imported, nil
	}
	e.imported[p] = pkg
	if e.importing == nil {
		e.importing = map[string]bool{}
	}
	e.importing[p] = true
	defer delete(e.importing, p)
	insert := func(obj types.Object) error {
		if !token.IsIdentifier(obj.Name()) || obj.Name() == "_" {
			return fmt.Errorf("invalid export name %q", obj.Name())
		}
		if pkg.Scope().Insert(obj) != nil {
			return fmt.Errorf("duplicate export %s.%s", p, obj.Name())
		}
		return nil
	}
	var pendingNative []evm.TypeDecl
	if !decl.HasSource() {
		for _, d := range decl.Types {
			if d.SourceName != "" || d.ScalarBase != nil || d.Shape.Type == values.ErrorType && d.NativeCodec == evm.NativeErrorCodec {
				continue
			}
			if d.Name != "" && e.goOf(d.Shape.Type).Underlying() != nil {
				continue
			}
			typ, err := e.goShape(d.Shape, pkg)
			if err != nil {
				return nil, err
			}
			if d.Name != "" {
				e.goOf(d.Shape.Type).(*types.Named).SetUnderlying(typ.Underlying())
			} else if err := e.bindNative(d.Shape.Type, typ); err != nil {
				return nil, err
			}
		}
	}
	for _, d := range decl.Types {
		if d.Name != "" && d.SourceName == "" && d.ScalarBase == nil && e.nativeNeedsSource(d.Shape.Type) {
			pendingNative = append(pendingNative, d)
		}
	}
	if decl.HasSource() {
		if _, err := e.importSource(p, decl.Sources(), pkg); err != nil {
			if len(pendingNative) != 0 {
				names := make([]string, len(pendingNative))
				for i, d := range pendingNative {
					names[i] = d.Name
				}
				return nil, fmt.Errorf("source checking failed with deferred native types %s (their fields and layout-dependent constraints are unavailable until source instances are bound): %w", strings.Join(names, ", "), err)
			}
			return nil, err
		}
		for _, shared := range decl.SharedTypes {
			obj, ok := pkg.Scope().Lookup(shared.Name).(*types.TypeName)
			if !ok || shared.Native == nil || shared.Type == nil {
				return nil, fmt.Errorf("invalid shared type %s.%s", p, shared.Name)
			}
			basic, ok := obj.Type().Underlying().(*types.Basic)
			if !ok || basic.Name() != shared.Native.Kind().String() {
				return nil, fmt.Errorf("shared type %s.%s must match native scalar %v", p, shared.Name, shared.Native)
			}
			e.toGo[shared.Type] = obj.Type()
			e.fromGo.Set(obj.Type(), shared.Type)
		}
		boundSource := map[*values.Type]bool{}
		bindingSource := map[*values.Type]bool{}
		var bindSource func(evm.TypeDecl) error
		var bindArgument func(*values.Shape, map[*values.Shape]bool) error
		bindArgument = func(shape *values.Shape, seen map[*values.Shape]bool) error {
			if shape == nil || seen[shape] || e.goOf(shape.Type) != nil {
				return nil
			}
			seen[shape] = true
			if dep, ok := e.nativeTypes[shape.Type]; ok && dep.decl.ScalarBase != nil {
				_, err := e.declarePackage(dep.pkg)
				return err
			}
			if dep, ok := e.nativeTypes[shape.Type]; ok {
				if dep.pkg == p && dep.decl.SourceName != "" {
					return bindSource(dep.decl)
				}
				if dep.decl.Name != "" || dep.decl.SourceName != "" {
					return nil
				}
			}
			children := []*values.Shape{shape.Elem, shape.Key}
			for _, field := range shape.Fields {
				children = append(children, field.Shape)
			}
			children = append(children, shape.Params...)
			children = append(children, shape.Results...)
			for _, method := range shape.Methods {
				children = append(children, method.Signature)
			}
			for _, child := range children {
				if err := bindArgument(child, seen); err != nil {
					return err
				}
			}
			if dep, ok := e.nativeTypes[shape.Type]; ok && dep.decl.Name == "" && dep.decl.SourceName == "" {
				owner, err := e.declarePackage(dep.pkg)
				if err != nil {
					return err
				}
				gt, err := e.goShape(shape, owner)
				if err != nil {
					return err
				}
				return e.bindNative(shape.Type, gt)
			}
			return nil
		}
		bindSource = func(d evm.TypeDecl) error {
			if boundSource[d.Shape.Type] {
				return nil
			}
			if bindingSource[d.Shape.Type] {
				return fmt.Errorf("recursive source type arguments for %s", d.SourceName)
			}
			bindingSource[d.Shape.Type] = true
			defer delete(bindingSource, d.Shape.Type)
			if d.Name != "" {
				return fmt.Errorf("native type cannot have both Name and SourceName")
			}
			obj, ok := pkg.Scope().Lookup(d.SourceName).(*types.TypeName)
			if !ok {
				return fmt.Errorf("%s is not a source type", d.SourceName)
			}
			gt := obj.Type()
			named, _ := types.Unalias(gt).(*types.Named)
			generic := named != nil && named.TypeArgs().Len() == 0 && named.TypeParams().Len() != 0
			if len(d.TypeArgs) != 0 || generic {
				if !generic {
					return fmt.Errorf("source type %s is not generic", d.SourceName)
				}
				if len(d.TypeArgs) != named.TypeParams().Len() {
					return fmt.Errorf("source type %s needs %d type arguments, got %d", d.SourceName, named.TypeParams().Len(), len(d.TypeArgs))
				}
				args := make([]types.Type, len(d.TypeArgs))
				for i, arg := range d.TypeArgs {
					if dep, ok := e.nativeTypes[arg]; ok {
						if err := bindArgument(dep.decl.Shape, map[*values.Shape]bool{}); err != nil {
							return err
						}
					}
					var err error
					args[i], err = e.goType(arg, pkg)
					if err != nil {
						return err
					}
				}
				var err error
				e.pendingSourceChecks = append(e.pendingSourceChecks, sourceInstanceCheck{gt, args})
				gt, err = types.Instantiate(nil, gt, args, false)
				if err != nil {
					return err
				}
			}
			if err := e.bindNative(d.Shape.Type, gt); err != nil {
				return err
			}
			boundSource[d.Shape.Type] = true
			return nil
		}
		for _, d := range decl.Types {
			if d.SourceName != "" {
				if err := bindSource(d); err != nil {
					return nil, err
				}
			}
		}
		for _, d := range pendingNative {
			t, err := e.goShape(d.Shape, pkg)
			if err != nil {
				return nil, err
			}
			e.goOf(d.Shape.Type).(*types.Named).SetUnderlying(t.Underlying())
		}
		for _, d := range decl.Types {
			if d.Name != "" || d.SourceName != "" {
				continue
			}
			if d.Shape.Type == values.ErrorType && d.NativeCodec == evm.NativeErrorCodec {
				continue
			}
			gt, err := e.goShape(d.Shape, pkg)
			if err != nil {
				return nil, err
			}
			if err := e.bindNative(d.Shape.Type, gt); err != nil {
				return nil, err
			}
		}
		// The first declaration of a receiver and name binds its source method.
		methods := make(map[nativeMethodKey]*evm.ExternalMethod, len(decl.Methods))
		for i := range decl.Methods {
			m := &decl.Methods[i]
			key := nativeMethodKey{m.Receiver, m.Name}
			if methods[key] == nil {
				methods[key] = m
			}
		}
		for _, file := range e.standard[p].files {
			for _, d := range file.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok || fn.Body != nil || fn.Recv == nil {
					continue
				}
				obj := e.standard[p].info.Defs[fn.Name].(*types.Func)
				if obj.Signature().RecvTypeParams().Len() != 0 {
					if obj.Signature().TypeParams().Len() != 0 {
						return nil, fmt.Errorf("native method %s cannot have both receiver and method type parameters", obj.Name())
					}
					if e.genericNativeDecls == nil {
						e.genericNativeDecls = make(map[*types.Func]bool)
					}
					e.genericNativeDecls[obj.Origin()] = true
					continue
				}
				recv, err := e.valueType(obj.Signature().Recv().Type())
				if err != nil {
					return nil, err
				}
				if m := methods[nativeMethodKey{recv, obj.Name()}]; m != nil {
					binding := m.Binding()
					e.natives[obj] = nativeBinding{importKey{p, m.Name, recv}, &binding}
				}
			}
		}
	}
	for _, v := range decl.Values {
		t, err := e.goType(v.Type, pkg)
		if err != nil {
			return nil, err
		}
		if err := insert(types.NewVar(token.NoPos, pkg, v.Name, t)); err != nil {
			return nil, err
		}
	}
	for _, c := range decl.Constants {
		if c.Untyped != nil {
			k := map[constant.Kind]types.BasicKind{constant.Bool: types.UntypedBool, constant.String: types.UntypedString, constant.Int: types.UntypedInt, constant.Float: types.UntypedFloat, constant.Complex: types.UntypedComplex}[c.Untyped.Kind()]
			if c.Rune && k == types.UntypedInt {
				k = types.UntypedRune
			}
			if k == types.Invalid {
				return nil, fmt.Errorf("invalid untyped constant %s", c.Name)
			}
			if err := insert(types.NewConst(token.NoPos, pkg, c.Name, types.Typ[k], c.Untyped)); err != nil {
				return nil, err
			}
			continue
		}
		t, err := e.goType(c.Type, pkg)
		if err != nil {
			return nil, err
		}
		var value constant.Value
		switch b := t.Underlying().(type) {
		case *types.Basic:
			switch {
			case b.Info()&types.IsBoolean != 0:
				value = constant.MakeBool(c.Value.R != 0)
			case b.Info()&types.IsString != 0:
				value = constant.MakeString(c.Value.AsString())
			case b.Info()&types.IsFloat != 0:
				x := c.Value.AsFloat()
				if b.Kind() == types.Float32 {
					x = float64(math.Float32frombits(uint32(c.Value.R)))
				}
				if math.IsNaN(x) || math.IsInf(x, 0) {
					return nil, fmt.Errorf("non-finite constant %s", c.Name)
				}
				value = constant.MakeFloat64(x)
			case b.Info()&types.IsUnsigned != 0:
				value = constant.MakeUint64(c.Value.R)
			case b.Info()&types.IsInteger != 0:
				value = constant.MakeInt64(c.Value.AsInt())
			}
		}
		if value == nil {
			return nil, fmt.Errorf("unsupported constant %s", c.Name)
		}
		if err := insert(types.NewConst(token.NoPos, pkg, c.Name, t, value)); err != nil {
			return nil, err
		}
	}
	if err := e.addNativeMethods(pkg, decl, false); err != nil {
		return nil, err
	}
	for i := range decl.Functions {
		f := &decl.Functions[i]
		if f.Instantiate != nil {
			continue
		}
		sig, err := e.goSignature(f.Signature, f.Variadic, pkg)
		if err != nil {
			return nil, err
		}
		if len(f.ParamNames) != 0 {
			if len(f.ParamNames) != sig.Params().Len() {
				return nil, fmt.Errorf("parameter name count for %s", f.Name)
			}
			vars := make([]*types.Var, len(f.ParamNames))
			for i, name := range f.ParamNames {
				vars[i] = types.NewVar(token.NoPos, pkg, name, sig.Params().At(i).Type())
			}
			sig = types.NewSignatureType(nil, nil, nil, types.NewTuple(vars...), sig.Results(), sig.Variadic())
		}
		name := f.Name
		if f.On != nil {
			if !token.IsIdentifier(name) || !token.IsExported(name) || !slices.Contains(decl.Records, f.On) {
				return nil, fmt.Errorf("invalid record operation %s", f.LinkName())
			}
			name = operationIdent(p, f)
			if e.recordOperations == nil {
				e.recordOperations = make(map[string]string)
			}
			e.recordOperations[name] = f.LinkName()
		}
		obj := types.NewFunc(token.NoPos, pkg, name, sig)
		if existing, ok := pkg.Scope().Lookup(name).(*types.Func); ok {
			if _, native := e.natives[existing]; decl.HasSource() && native && f.On == nil {
				e.natives[existing] = nativeBinding{importKey{p, f.LinkName(), nil}, f}
				continue
			}
		}
		if f.On == nil {
			if std := e.standard[p]; std != nil && !std.declarationsOnly {
				return nil, fmt.Errorf("native function %s requires a source declaration", name)
			}
			if err := insert(obj); err != nil {
				return nil, err
			}
		} else if pkg.Scope().Insert(obj) != nil {
			return nil, fmt.Errorf("duplicate record operation %s", f.LinkName())
		}
		e.natives[obj] = nativeBinding{importKey{p, f.LinkName(), nil}, f}
	}

	for _, d := range decl.Types {
		actual, err := e.shape(e.goOf(d.Shape.Type))
		if err != nil {
			return nil, err
		}
		if !e.sameNativeLayout(d.Shape, actual) {
			return nil, fmt.Errorf("native storage declaration differs from Go layout for %s", d.Shape.Type.Name())
		}
	}
	for _, alias := range decl.Aliases {
		if !e.knownAliasTarget(alias.Target) {
			return nil, fmt.Errorf("alias %s.%s has undeclared target %v", p, alias.Name, alias.Target.Name())
		}
		target, err := e.goType(alias.Target, pkg)
		if err != nil {
			return nil, err
		}
		obj := types.NewTypeName(token.NoPos, pkg, alias.Name, nil)
		types.NewAlias(obj, target)
		if err := insert(obj); err != nil {
			return nil, err
		}
	}
	pkg.MarkComplete()
	return pkg, nil
}
func (e *typeEnvironment) goTuple(ts []*values.Type, pkg *types.Package) (*types.Tuple, error) {
	vars := make([]*types.Var, len(ts))
	for i, t := range ts {
		gt, err := e.goType(t, pkg)
		if err != nil {
			return nil, err
		}
		vars[i] = types.NewVar(token.NoPos, pkg, "", gt)
	}
	return types.NewTuple(vars...), nil
}
func (e *typeEnvironment) goType(t *values.Type, pkg *types.Package) (types.Type, error) {
	if len(e.importErrors) != 0 {
		if err := e.importErrors[e.nativeTypes[t].pkg]; err != nil {
			return nil, err
		}
		if err := e.importErrors[e.recordOwners[t]]; err != nil {
			return nil, err
		}
		for path, err := range e.importErrors {
			if owner := e.packages[path]; owner != nil {
				for _, shared := range owner.SharedTypes {
					if shared.Type == t {
						return nil, err
					}
				}
			}
		}
	}
	if gt := e.goOf(t); gt != nil {
		if e.uses != nil {
			// Without the binding this would declare or import the owner.
			for _, owner := range []string{e.nativeTypes[t].pkg, e.recordOwners[t], e.sharedOwners[t]} {
				e.use(owner)
			}
		}
		if named, ok := gt.(*types.Named); ok && named.Underlying() == nil && e.nativeNeedsSource(t) {
			owner := e.nativeTypes[t].pkg
			if e.importing[owner] {
				if pkg == nil || pkg.Path() != owner {
					return nil, fmt.Errorf("import cycle through %q for source-dependent native type %s", owner, t.Name())
				}
			} else {
				if _, err := e.importPackage(owner); err != nil {
					return nil, err
				}
				if named.Underlying() == nil {
					return nil, fmt.Errorf("source-dependent native type %s is not defined", t.Name())
				}
			}
		}
		return gt, nil
	}
	if t == nil {
		return nil, fmt.Errorf("nil external type")
	}
	for path, p := range e.packages {
		for _, shared := range p.SharedTypes {
			if shared.Type == t {
				if _, err := e.importPackage(path); err != nil {
					return nil, err
				}
				if gt := e.goOf(t); gt != nil {
					return gt, nil
				}
				return nil, fmt.Errorf("source type %s is not bound yet", t.Name())
			}
		}
	}
	if owner, ok := e.recordOwners[t]; ok {
		if _, err := e.declarePackage(owner); err != nil {
			return nil, err
		}
		if gt := e.goOf(t); gt != nil {
			return gt, nil
		}
		return nil, fmt.Errorf("unresolved record type %s", t.Name())
	}
	if d, ok := e.nativeTypes[t]; ok {
		var owner *types.Package
		var err error
		if !e.nativeNeedsSource(t) {
			owner, err = e.declarePackage(d.pkg)
		} else {
			owner, err = e.importPackage(d.pkg)
		}
		if err != nil {
			return nil, err
		}
		if gt := e.goOf(t); gt != nil {
			return gt, nil
		}
		if e.resolving[t] {
			return nil, fmt.Errorf("recursive unnamed native type %s", t.Name())
		}
		e.resolving[t] = true
		defer delete(e.resolving, t)
		if d.decl.SourceName != "" {
			return nil, fmt.Errorf("source type %s is not bound yet", t.Name())
		}
		gt, err := e.goShape(d.decl.Shape, owner)
		if err != nil {
			return nil, err
		}
		if err := e.bindNative(t, gt); err != nil {
			return nil, err
		}
		return gt, nil
	}
	if t.Repr() != values.Raw {
		return nil, fmt.Errorf("external type %s needs an explicit definition", t.Name())
	}
	name := t.Name()
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	if !token.IsIdentifier(name) {
		return nil, fmt.Errorf("invalid type name %s", name)
	}
	late := lateType{pkg, name}
	if e.frozen {
		// Declaring the type would write to a package shared by other compiles.
		// Freezing resolved every type a declaration names, so only a name
		// that is already taken reaches this point.
		if pkg.Scope().Lookup(name) != nil || e.lateTypes[late] {
			return nil, fmt.Errorf("duplicate type name %s", name)
		}
		return nil, fmt.Errorf("external type %s is not part of the prepared environment", t.Name())
	}
	obj := types.NewTypeName(token.NoPos, pkg, name, nil)
	gt := types.NewNamed(obj, types.Typ[types.Int64], nil)
	if e.lateTypes != nil {
		// A lazily built environment declares this type when lowering reaches
		// it, after the source was checked, so the source cannot name it.
		// Keep it out of the scope that every later source is checked against.
		if pkg.Scope().Lookup(name) != nil || e.lateTypes[late] {
			return nil, fmt.Errorf("duplicate type name %s", name)
		}
		e.lateTypes[late] = true
	} else if pkg.Scope().Insert(obj) != nil {
		return nil, fmt.Errorf("duplicate type name %s", name)
	}
	e.toGo[t] = gt
	e.fromGo.Set(gt, t)
	return gt, nil
}
func (e *typeEnvironment) valueType(t types.Type) (*values.Type, error) {
	s, err := e.shape(t)
	if err != nil {
		return nil, err
	}
	return s.Type, nil
}
func (e *typeEnvironment) signature(sig *types.Signature) (evm.Signature, error) {
	var result evm.Signature
	if sig.Recv() != nil {
		t, err := e.valueType(sig.Recv().Type())
		if err != nil {
			return result, err
		}
		result.Params = append(result.Params, t)
	}
	for i := 0; i < sig.Params().Len(); i++ {
		t, err := e.valueType(sig.Params().At(i).Type())
		if err != nil {
			return result, err
		}
		result.Params = append(result.Params, t)
	}
	for i := 0; i < sig.Results().Len(); i++ {
		t, err := e.valueType(sig.Results().At(i).Type())
		if err != nil {
			return result, err
		}
		result.Results = append(result.Results, t)
	}
	return result, nil
}
func numericType(t types.Type) (evm.NumericType, bool) {
	b, ok := t.Underlying().(*types.Basic)
	if !ok {
		return evm.NumericType{}, false
	}
	n := evm.NumericType{Bits: 64}
	switch b.Kind() {
	case types.Int8, types.Uint8:
		n.Bits = 8
	case types.Int16, types.Uint16:
		n.Bits = 16
	case types.Int32, types.Uint32, types.Float32:
		n.Bits = 32
	}
	switch {
	case b.Info()&types.IsFloat != 0:
		n.Kind = evm.NumericFloat
	case b.Info()&types.IsUnsigned != 0:
		n.Kind = evm.NumericUnsigned
	case b.Info()&types.IsInteger != 0:
		n.Kind = evm.NumericSigned
	default:
		return n, false
	}
	return n, true
}

// Aliases never use goType's fallback declaration for unknown host types.
func (e *typeEnvironment) knownAliasTarget(t *values.Type) bool {
	if t == nil {
		return false
	}
	if t == values.AnyType || t == values.ErrorType || t == evm.ErrorSliceType {
		return true
	}
	for _, builtin := range goBasicValues {
		if t == builtin {
			return true
		}
	}
	if _, ok := e.nativeTypes[t]; ok {
		return true
	}
	if _, ok := e.recordOwners[t]; ok {
		return true
	}
	for _, p := range e.packages {
		for _, shared := range p.SharedTypes {
			if shared.Type == t {
				return true
			}
		}
	}
	return false
}

// Record references need type identities, not the owner's source or methods.
func (e *typeEnvironment) declarePackage(p string) (_ *types.Package, declarationErr error) {
	if e.frozen {
		if pkg := e.declared[p]; pkg != nil {
			return pkg, nil
		}
		if _, ok := e.packages[p]; ok {
			return e.frozenPackage(p)
		}
		return nil, fmt.Errorf("package %q is not exposed", p)
	}
	if e.uses != nil {
		defer e.enter(p)()
	}
	if err := e.importErrors[p]; err != nil {
		return nil, err
	}
	defer func() {
		if declarationErr != nil {
			if e.importErrors == nil {
				e.importErrors = map[string]error{}
			}
			e.importErrors[p] = declarationErr
		}
	}()
	if pkg := e.declared[p]; pkg != nil {
		return pkg, nil
	}
	decl := e.packages[p]
	if decl == nil {
		return nil, fmt.Errorf("package %q is not exposed", p)
	}
	name := path.Base(p)
	if decl.HasSource() {
		file, err := parser.ParseFile(e.files, p+"/"+decl.Sources()[0].Name, decl.Sources()[0].Text, parser.PackageClauseOnly)
		if err != nil {
			return nil, err
		}
		name = file.Name.Name
	}
	if !token.IsIdentifier(name) {
		return nil, fmt.Errorf("invalid package name %q", name)
	}
	pkg := types.NewPackage(p, name)

	if err := e.declareRecordIdentities(pkg, decl.Records); err != nil {
		return nil, err
	}
	if e.declared == nil {
		e.declared = map[string]*types.Package{}
	}
	e.declared[p] = pkg
	// Native identities do not depend on the owner's source imports.
	for _, d := range decl.Types {
		if d.Name == "" || d.SourceName != "" || d.ScalarBase != nil {
			continue
		}
		if !token.IsIdentifier(d.Name) || d.Name == "_" {
			return nil, fmt.Errorf("invalid export name %q", d.Name)
		}
		obj := types.NewTypeName(token.NoPos, pkg, d.Name, nil)
		named := types.NewNamed(obj, nil, nil)
		if pkg.Scope().Insert(obj) != nil {
			return nil, fmt.Errorf("duplicate export %s.%s", p, d.Name)
		}
		e.toGo[d.Shape.Type] = named
		e.fromGo.Set(named, d.Shape.Type)
	}
	// Native scalar identities need no package functions or source checking.
	// Declare them early so generic instances can refer back across packages.
	for _, d := range decl.Types {
		if d.Name == "" || d.ScalarBase == nil {
			continue
		}
		if !token.IsIdentifier(d.Name) || d.Name == "_" {
			return nil, fmt.Errorf("invalid export name %q", d.Name)
		}
		base := e.goOf(d.ScalarBase)
		if _, ok := base.(*types.Basic); !ok {
			return nil, fmt.Errorf("invalid scalar base for %s", d.Name)
		}
		shape, err := e.shape(base)
		if err != nil || shape.Kind != d.Shape.Kind || shape.Bits != d.Shape.Bits && (shape.Kind == values.IntKind || shape.Kind == values.UintKind || shape.Kind == values.FloatKind) {
			return nil, fmt.Errorf("incompatible scalar base for %s", d.Name)
		}
		obj := types.NewTypeName(token.NoPos, pkg, d.Name, nil)
		named := types.NewNamed(obj, base, nil)
		if pkg.Scope().Insert(obj) != nil {
			return nil, fmt.Errorf("duplicate export %s.%s", p, d.Name)
		}
		e.toGo[d.Shape.Type] = named
		e.fromGo.Set(named, d.Shape.Type)
	}
	if err := e.defineRecords(pkg, decl.Records); err != nil {
		return nil, err
	}
	for _, d := range decl.Types {
		if d.SourceName != "" || d.ScalarBase != nil || e.nativeNeedsSource(d.Shape.Type) {
			continue
		}
		if d.Name != "" {
			named := e.goOf(d.Shape.Type).(*types.Named)
			if named.Underlying() != nil {
				continue
			}
			underlying, err := e.goShape(d.Shape, pkg)
			if err != nil {
				return nil, err
			}
			named.SetUnderlying(underlying.Underlying())
		}
	}
	if err := e.addNativeMethods(pkg, decl, true); err != nil {
		return nil, err
	}
	return pkg, nil
}
