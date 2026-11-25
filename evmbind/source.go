// Package evmbind builds a reusable EVM host schema from Go declarations.
package evmbind

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"maps"
	"math"
	"path"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/values"
)

// nodeKind classifies Go types for binding traversal and code generation.
// Aliases such as byte and rune use their underlying scalar kind; node.name
// retains their Go spelling for emitted declarations.
type nodeKind uint8

const (
	nodeInvalid nodeKind = iota
	nodeRecord
	nodeTimestamp
	nodeBool
	nodeInt
	nodeInt8
	nodeInt16
	nodeInt32
	nodeInt64
	nodeUint
	nodeUint8
	nodeUint16
	nodeUint32
	nodeUint64
	nodeFloat32
	nodeFloat64
	nodeString
	nodeCallback
	nodeRetainedFunction
	nodeNativeFunction
	nodeSlice
	nodeMap
	nodeAny
	nodeError
	nodeStruct
	nodePointer
	nodeInterface
)

func (k nodeKind) String() string {
	switch k {
	case nodeTimestamp:
		return "timestamp"
	case nodeRecord:
		return "record"
	case nodeInvalid:
		return "invalid"
	case nodeBool:
		return "bool"
	case nodeInt:
		return "int"
	case nodeInt8:
		return "int8"
	case nodeInt16:
		return "int16"
	case nodeInt32:
		return "int32"
	case nodeInt64:
		return "int64"
	case nodeUint:
		return "uint"
	case nodeUint8:
		return "uint8"
	case nodeUint16:
		return "uint16"
	case nodeUint32:
		return "uint32"
	case nodeUint64:
		return "uint64"
	case nodeFloat32:
		return "float32"
	case nodeFloat64:
		return "float64"
	case nodeString:
		return "string"
	case nodeRetainedFunction:
		return "retainedFunction"
	case nodeNativeFunction:
		return "nativeFunction"
	case nodeCallback:
		return "callback"
	case nodeMap:
		return "map"
	case nodeSlice:
		return "slice"
	case nodeAny:
		return "any"
	case nodeError:
		return "error"
	case nodeStruct:
		return "struct"
	case nodePointer:
		return "ptr"
	case nodeInterface:
		return "interface"
	default:
		return "nodeKind(" + strconv.Itoa(int(k)) + ")"
	}
}

func basicNodeKind(k types.BasicKind) nodeKind {
	switch k {
	case types.Bool:
		return nodeBool
	case types.Int:
		return nodeInt
	case types.Int8:
		return nodeInt8
	case types.Int16:
		return nodeInt16
	case types.Int32:
		return nodeInt32
	case types.Int64:
		return nodeInt64
	case types.Uint:
		return nodeUint
	case types.Uint8:
		return nodeUint8
	case types.Uint16:
		return nodeUint16
	case types.Uint32:
		return nodeUint32
	case types.Uint64:
		return nodeUint64
	case types.Float32:
		return nodeFloat32
	case types.Float64:
		return nodeFloat64
	case types.String:
		return nodeString
	default:
		return nodeInvalid
	}
}

type node struct {
	concurrentFunction         bool
	functionSignatureReady     bool
	boxedFunction              bool
	genericInstance            bool
	genericType                types.Type
	nativeLayout               *values.NativeLayout
	modelType                  types.Type
	model                      *modelDeclaration
	origin                     *node        // declaration node retained through transitive imports
	native                     reflect.Type // exact native ABI, independent of the shared VM shape
	omittedFields              []string
	imported                   *Schema
	nativeImport, nativeName   string
	name                       string
	shape                      *values.Shape
	functionSignature          evm.Signature
	functionArgs, functionOuts []*node
	kind                       nodeKind
	fields                     []field
	key                        *node
	elem                       *node
	named                      bool
	methods                    []*operation
}
type field struct {
	name   string
	t      *node
	offset uint64
}
type byteBorrow struct {
	domain string
	method string
}

type callbackBorrow struct {
	domain    string
	parameter string // empty for the yield function of a returned iterator
	argument  int    // index in operation.args, or -1 for a returned iterator
}
type operation struct {
	concurrentInterfaces []int
	callbackBorrow       *callbackBorrow
	byteBorrow           *byteBorrow
	paramNames           []string
	doc                  string
	packageFunction      bool
	value                bool // a bound package variable, read through its function
	variadic             bool
	name, key            string
	recv                 *node
	args, outs           []*node
}
type source struct {
	concurrentParams   map[token.Pos]bool
	cellStructs        map[string]*values.CellStruct
	mutable            map[string]MutableSlots
	mutableUsed        map[string]bool
	typeNames          map[string]string
	genericFunctions   []*sourceGenericFunction
	instanceInputs     []Instance
	resolvingInstances map[reflect.Type]bool
	instances          []sourceInstance
	aliases            map[string]*node
	nativePackage      string
	models             map[string]*modelDeclaration
	nativeImports      map[*types.Package]schemaImport
	omittedFields      map[string][]string
	functionShapes     map[string]*values.Shape
	imports            map[string]*Schema
	importedNodes      map[*node]*node
	goPackage          *types.Package
	pkg                evm.Package
	nodes              map[string]*node
	ops                []*operation
	fingerprint        string
	name               string
}

func spelling(t types.Type) string {
	return types.TypeString(t, func(*types.Package) string { return "" })
}
func parse(path string, files fs.FS) (*source, error) {
	return parseTypes(path, files, nil, false)
}

func parseTypes(path string, files fs.FS, selected []string, typesOnly bool) (*source, error) {
	return parseImported(path, files, selected, typesOnly, nil)
}

func parseImported(path string, files fs.FS, selected []string, typesOnly bool, imports map[string]*Schema) (*source, error) {
	return parseImportedFields(path, files, selected, typesOnly, imports, nil)
}
func parseImportedFields(path string, files fs.FS, selected []string, typesOnly bool, imports map[string]*Schema, omitted map[string][]string) (s *source, err error) {
	return parseImportedSelections(path, files, selected, typesOnly, imports, omitted, nil)
}
func parseImportedSelections(path string, files fs.FS, selected []string, typesOnly bool, imports map[string]*Schema, omitted map[string][]string, methods map[string][]string) (s *source, err error) {
	return parseOptions(path, files, selected, typesOnly, Options{Imports: imports, OmitFields: omitted, Methods: methods})
}
func parseOptions(path string, files fs.FS, selected []string, typesOnly bool, opt Options) (s *source, err error) {
	imports, omitted, methods := maps.Clone(opt.Imports), opt.OmitFields, opt.Methods
	opt.Types = slices.Clone(opt.Types)
	if selected != nil {
		selected = slices.Clone(selected)
	}
	for _, in := range opt.Instances {
		opt.Types = append(opt.Types, in.Native)
		if selected != nil && !slices.Contains(selected, in.Type) {
			selected = append(selected, in.Type)
		}
	}
	// Schema errors originate in recursive type traversal; return them at this boundary.
	defer func() {
		if x := recover(); x != nil {
			if e, ok := x.(schemaError); ok {
				err = e
				s = nil
			} else {
				panic(x)
			}
		}
	}()
	names, err := fs.Glob(files, "*.go")
	if err != nil {
		return nil, err
	}
	var syntax []*ast.File
	docs := map[string]string{}
	concurrentParams := map[token.Pos]bool{}
	callbackBorrows := map[token.Pos]*callbackBorrow{}
	byteBorrows := map[token.Pos]*byteBorrow{}
	fset := token.NewFileSet()
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, e := fs.ReadFile(files, name)
		if e != nil {
			return nil, e
		}
		f, e := parser.ParseFile(fset, name, b, parser.SkipObjectResolution|parser.ParseComments)
		if e != nil {
			return nil, e
		}
		syntax = append(syntax, f)
		collectConcurrentParams(f, concurrentParams)
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Doc != nil {
				for _, comment := range fn.Doc.List {
					if fn.Recv == nil {
						docs[fn.Name.Name] += comment.Text + "\n"
					}
					parts := strings.Fields(comment.Text)
					if len(parts) > 0 && (parts[0] == "//evm:borrow-yield-bytes" || parts[0] == "//evm:borrow-callback-bytes") {
						direct := parts[0] == "//evm:borrow-callback-bytes"
						if fn.Recv == nil || (!direct && len(parts) != 2) || (direct && len(parts) != 3) || callbackBorrows[fn.Name.Pos()] != nil {
							fail("%s: invalid %s declaration", fn.Name.Name, parts[0])
						}
						b := &callbackBorrow{domain: parts[1]}
						if direct {
							b.parameter = parts[2]
						}
						callbackBorrows[fn.Name.Pos()] = b
						continue
					}
					if len(parts) == 0 || parts[0] != "//evm:borrow-bytes" {
						continue
					}
					if fn.Recv == nil || len(parts) < 2 || len(parts) > 3 || byteBorrows[fn.Name.Pos()] != nil {
						fail("%s: expected //evm:borrow-bytes domain [nativeMethod] on a method", fn.Name.Name)
					}
					method := fn.Name.Name
					if len(parts) == 3 {
						method = parts[2]
						if !token.IsIdentifier(method) || !ast.IsExported(method) {
							fail("invalid borrowed byte method %q", method)
						}
					}
					byteBorrows[fn.Name.Pos()] = &byteBorrow{parts[1], method}
				}
			}
		}
	}
	if len(syntax) == 0 {
		return nil, fmt.Errorf("missing Go declarations")
	}
	opt.NativePackage, err = sourceNativePackage(syntax, opt)
	if err != nil {
		return nil, err
	}
	if selected != nil {
		localTypes := map[string]bool{}
		for _, file := range syntax {
			for _, decl := range file.Decls {
				if d, ok := decl.(*ast.GenDecl); ok {
					for _, spec := range d.Specs {
						if t, ok := spec.(*ast.TypeSpec); ok {
							localTypes[t.Name.Name] = true
						}
					}
				}
			}
		}
		var addArg func(reflect.Type)
		addArg = func(t reflect.Type) {
			if t == nil {
				return
			}
			if t.PkgPath() == opt.NativePackage && t.Name() != "" {
				name := modelBaseName(t.Name())
				if localTypes[name] && !slices.Contains(selected, name) {
					selected = append(selected, name)
				}
			}
			switch t.Kind() {
			case reflect.Pointer, reflect.Slice, reflect.Array:
				addArg(t.Elem())
			case reflect.Map:
				addArg(t.Key())
				addArg(t.Elem())
			}
		}
		for _, in := range opt.Instances {
			for _, arg := range in.Args {
				addArg(arg)
			}
		}
		for _, instances := range opt.GenericFunctions {
			for _, in := range instances {
				for _, arg := range in.Args {
					addArg(arg)
				}
			}
		}
	}
	omitted, err = selectedFieldOmissions(syntax, opt.Fields, omitted)
	if err != nil {
		return nil, err
	}
	opt.OmitFields = omitted
	models, selected, err := prepareModels(syntax, selected, typesOnly, opt)
	if err != nil {
		return nil, err
	}
	// Unexported fields are never script API. Native storage keeps them.
	omitted = omitUnexportedFields(syntax, omitted)
	opt.OmitFields = omitted
	if err := omitTypeFields(syntax, omitted); err != nil {
		return nil, err
	}
	// Selection keeps the imports that remaining declarations name.
	nameSchemaImports(syntax, imports)
	if err := declareBoundValues(syntax, opt); err != nil {
		return nil, err
	}
	functions := functionNames(opt)
	omit := map[string]bool{}
	if !typesOnly {
		selected, methods, functions, omit, err = wholeSelection(syntax, opt)
		if err != nil {
			return nil, err
		}
	} else if len(opt.Omit) != 0 {
		return nil, fmt.Errorf("Options.Omit applies to a schema that exposes everything; a selective schema lists what it exposes")
	}
	syntax, exposed, err := selectSyntax(syntax, selected, methods, slices.Sorted(maps.Keys(functions)), !typesOnly, omit)
	if err != nil {
		return nil, err
	}
	// Selection decides what the checked package exports to scripts.
	exports := func(obj types.Object) bool {
		switch o := obj.(type) {
		case *types.TypeName:
			return exposed[o.Name()]
		case *types.Func:
			return functions[o.Name()]
		case *types.Const:
			if !typesOnly {
				return !omit[o.Name()]
			}
			named, ok := o.Type().(*types.Named)
			return ok && exposed[named.Obj().Name()]
		}
		return false
	}
	importer, err := schemaImporter(fset, syntax, imports)
	if err != nil {
		return nil, err
	}
	pkg, err := (&types.Config{GoVersion: "go1.24", Sizes: types.SizesFor("gc", "amd64"), Importer: importer}).Check(path, fset, syntax, nil)
	if err != nil {
		return nil, explainImportError(err, syntax)
	}
	s = &source{concurrentParams: concurrentParams, cellStructs: opt.CellStructs, mutable: maps.Clone(opt.Mutable), mutableUsed: map[string]bool{}, typeNames: maps.Clone(opt.TypeNames), nativePackage: opt.NativePackage, models: models, nativeImports: importer.(nativeSchemaImporter).byPackage, omittedFields: omitted, imports: imports, importedNodes: map[*node]*node{}, goPackage: pkg, name: pkg.Name(), pkg: evm.Package{Path: path}, nodes: map[string]*node{}}
	for name, guest := range opt.TypeNames {
		obj, ok := pkg.Scope().Lookup(name).(*types.TypeName)
		if !ok || !obj.Exported() || !exposed[name] {
			return nil, fmt.Errorf("renamed type %s is not exposed", name)
		}
		named, ok := obj.Type().(*types.Named)
		if !ok || named.TypeParams().Len() != 0 || models[name] != nil {
			return nil, fmt.Errorf("renamed type %s must be a nongeneric native type declaration", name)
		}
		if !token.IsIdentifier(guest) || !ast.IsExported(guest) {
			return nil, fmt.Errorf("invalid guest type name %q for %s", guest, name)
		}
	}
	seenNames := map[string]string{}
	for _, name := range pkg.Scope().Names() {
		obj := pkg.Scope().Lookup(name)
		if !obj.Exported() || !exports(obj) {
			continue
		}
		guest := s.guestTypeName(name)
		if prev := seenNames[guest]; prev != "" {
			return nil, fmt.Errorf("guest export %s conflicts between %s and %s", guest, prev, name)
		}
		seenNames[guest] = name
	}
	s.prepareInstances(opt.Instances)
	for name := range opt.GenericFunctions {
		if _, ok := opt.Functions[name]; ok {
			fail("function %s has both ordinary and generic bindings", name)
		}
		if o, ok := pkg.Scope().Lookup(name).(*types.Func); !ok || !o.Exported() {
			fail("generic function %s is not declared or exported", name)
		}
	}
	var abi []string
	for name, guest := range opt.TypeNames {
		abi = append(abi, "type export "+name+" "+guest)
	}
	for name, model := range models {
		if pkg.Scope().Lookup(modelBaseName(name)) != nil {
			abi = append(abi, "native model "+name+" "+model.row.Pointer.Name())
		}
	}
	for path, dep := range imports {
		abi = append(abi, "import "+path+" "+dep.src.fingerprint)
	}
	for _, name := range pkg.Scope().Names() {
		obj := pkg.Scope().Lookup(name)
		if !obj.Exported() || !exports(obj) {
			continue
		}
		switch o := obj.(type) {
		case *types.TypeName:
			if alias, ok := o.Type().(*types.Alias); ok {
				if alias.TypeParams().Len() != 0 {
					fail("generic native alias %s requires an instantiated declaration", name)
				}
				target := alias.Rhs()
				if aliasesFunctionOf(target) {
					fail("alias %s of an EVM callable is not supported", name)
				}
				target = canonicalAliasType(target)
				n := s.node(target)
				if s.aliases == nil {
					s.aliases = map[string]*node{}
				}
				s.aliases[name] = n
				if n.kind == nodeStruct || n.kind == nodeRecord || basics[n.kind] != nil {
					s.aliases["*"+name] = s.node(types.NewPointer(target))
				}
				sameName := n.shape.Type.Name() == path+"."+name
				for _, imp := range s.nativeImports {
					if imp.schema.src.pkg.Path == path && imp.schema.src.guestTypeName(name) == name && imp.schema.Type(name) == n.shape.Type {
						sameName = true
						break
					}
				}
				if !sameName {
					s.pkg.Aliases = append(s.pkg.Aliases, evm.TypeAlias{Name: name, Target: n.shape.Type})
				}
				abi = append(abi, "alias "+name+" = "+n.shape.Type.Name())
				continue
			}
			if iface, ok := o.Type().Underlying().(*types.Interface); ok && !iface.IsMethodSet() {
				continue // a type constraint, not a value type
			}
			if named, ok := o.Type().(*types.Named); ok && named.TypeParams().Len() != 0 {
				found := false
				for concrete, model := range models {
					if modelBaseName(concrete) == name {
						if model.ambiguous {
							fail("native type %s has multiple models", model.native)
						}
						abi = append(abi, "model "+concrete+" "+model.row.Pointer.Name()+" value "+model.row.Value.Name()+" allocation "+modelAllocationName(model))
						found = true
					}
				}
				if !found {
					for _, in := range s.instances {
						if in.typ.Origin() == named {
							found = true
							break
						}
					}
				}
				if found {
					abi = append(abi, "model template "+types.TypeString(named, nil))
					continue
				}
			}
			n := s.node(o.Type())
			if n.kind == nodeStruct || n.kind == nodeRecord || basics[n.kind] != nil {
				s.node(types.NewPointer(o.Type()))
			}
			if n.kind == nodeRecord {
				abi = append(abi, "model "+name+" "+n.model.row.Pointer.Name()+" value "+n.model.row.Value.Name()+" allocation "+modelAllocationName(n.model))
			} else {
				abi = append(abi, name+" "+spelling(o.Type().Underlying()))
			}
			named, ok := o.Type().(*types.Named)
			if !ok {
				fail("unsupported alias %s", name)
			}
			if n.kind == nodeInterface {
				for _, op := range n.methods {
					abi = append(abi, op.abi())
				}
			} else {
				for i := 0; i < named.NumMethods(); i++ {
					m := named.Method(i)
					if m.Exported() {
						sig := m.Type().(*types.Signature)
						op := s.op(m, s.node(sig.Recv().Type()))
						if borrow := byteBorrows[m.Pos()]; borrow != nil {
							if len(op.outs) != 1 || op.outs[0].shape.Type != evm.ByteSliceType {
								fail("%s: borrowed byte method must return []byte", op.key)
							}
							op.byteBorrow = borrow
						}
						if b := callbackBorrows[m.Pos()]; b != nil {
							op.checkCallbackBorrow(b)
							op.callbackBorrow = b
						}
						s.ops = append(s.ops, op)
						abi = append(abi, op.abi())
					}
				}
			}
		case *types.Func:
			if instances, ok := opt.GenericFunctions[o.Name()]; ok {
				s.addGenericFunction(o, instances)
				continue
			}
			if o.Signature().TypeParams().Len() != 0 {
				fail("generic function %s needs its instances in Options.GenericFunctions; Options.Omit leaves it out", name)
			}
			op := s.op(o, nil)
			op.doc = docs[o.Name()]
			_, op.packageFunction = opt.Functions[o.Name()]
			if _, op.value = opt.Values[o.Name()]; op.value {
				op.packageFunction = true
			}
			s.ops = append(s.ops, op)
			abi = append(abi, op.abi())
		case *types.Const:
			duplicate := s.reexportedConstant(o)
			if b, ok := o.Type().(*types.Basic); ok && b.Info()&types.IsUntyped != 0 {
				if !duplicate {
					s.pkg.Constants = append(s.pkg.Constants, evm.ExternalConstant{Name: name, Untyped: o.Val(), Rune: b.Kind() == types.UntypedRune})
				}
				abi = append(abi, "const "+name+" "+b.Name()+" "+o.Val().ExactString())
				continue
			}
			t := types.Default(o.Type())
			n := s.node(t)
			var v values.Value
			switch n.shape.Kind {
			case values.IntKind:
				x, ok := constant.Int64Val(o.Val())
				if !ok {
					fail("constant overflow: %s", name)
				}
				v = values.Int(x)
			case values.UintKind:
				x, ok := constant.Uint64Val(o.Val())
				if !ok {
					fail("constant overflow: %s", name)
				}
				v = values.Uint(x)
			case values.FloatKind:
				if n.shape.Bits == 32 {
					x, _ := constant.Float32Val(o.Val())
					v = values.Uint(uint64(math.Float32bits(x)))
				} else {
					x, _ := constant.Float64Val(o.Val())
					v = values.Float(x)
				}
			case values.BoolKind:
				v = values.Bool(constant.BoolVal(o.Val()))
			case values.StringKind:
				v = values.String(constant.StringVal(o.Val()))
			default:
				fail("unsupported constant %s", name)
			}
			if !duplicate {
				s.pkg.Constants = append(s.pkg.Constants, evm.ExternalConstant{Name: name, Type: n.shape.Type, Value: v})
			}
			abi = append(abi, "const "+name+" "+n.name+" "+o.Val().ExactString())
		default:
			fail("unsupported export %s", name)
		}
	}
	for _, fn := range s.genericFunctions {
		for _, in := range fn.instances {
			names := make([]string, len(in.args))
			for i, t := range in.args {
				names[i] = t.Name()
			}
			abi = append(abi, "generic function "+fn.name+"["+strings.Join(names, ",")+"] "+in.op.abi())
		}
	}
	if source := s.finishInstances(); source != "" {
		abi = append(abi, source)
		for _, in := range s.instances {
			abi = append(abi, "instance "+types.TypeString(in.typ, nil)+" native "+in.native.PkgPath()+"."+in.native.Name())
		}
	}
	for name, annotation := range s.mutable {
		abi = append(abi, fmt.Sprintf("mutable %s params %v results %v", name, annotation.Params, annotation.Results))
		if !s.mutableUsed[name] {
			fail("mutable annotation %s does not name an exposed operation", name)
		}
	}
	// Older generated slice readers treated a native view header as backing
	// storage. Require regeneration when this schema contains such readers.
	for _, n := range s.nodes {
		if n.kind == nodeSlice && !isModelSlice(n) {
			abi = append(abi, "native slice view readers 1")
			break
		}
	}
	for name, n := range s.nodes {
		if n.shape.CellStruct != nil {
			abi = append(abi, "cell struct 1 "+name)
		}
	}
	sort.Strings(abi)
	s.fingerprint = fmt.Sprintf("%x", sha256.Sum256([]byte("evmbind2\n"+path+"\n"+strings.Join(abi, "\n"))))
	return s, nil
}

type schemaError struct{ error }

func fail(f string, a ...any) { panic(schemaError{fmt.Errorf(f, a...)}) }
func (op *operation) abi() string {
	var p, r []string
	for i, n := range op.args {
		if slices.Contains(op.concurrentInterfaces, i) {
			p = append(p, "concurrent-interface "+n.name)
			continue
		}
		p = append(p, n.name)
	}
	for _, n := range op.outs {
		r = append(r, n.name)
	}
	if op.variadic {
		p[len(p)-1] = "..." + strings.TrimPrefix(p[len(p)-1], "[]")
	}
	prefix := ""
	if op.packageFunction {
		prefix = "package function "
	}
	if op.value {
		prefix = "package value "
	}
	if b := op.byteBorrow; b != nil {
		prefix += "borrow-bytes " + b.domain + " " + b.method + " "
	}
	if b := op.callbackBorrow; b != nil {
		prefix += fmt.Sprintf("callback-bytes %s %d ", b.domain, b.argument)
	}
	return prefix + op.key + "(" + strings.Join(p, ",") + ")(" + strings.Join(r, ",") + ")"
}
func (s *source) op(f *types.Func, recv *node) *operation {
	sig := f.Type().(*types.Signature)
	if sig.TypeParams().Len() > 0 {
		fail("unsupported signature %s", f.Name())
	}
	op := &operation{name: f.Name(), key: f.Name(), recv: recv, variadic: sig.Variadic()}
	if recv != nil {
		op.key = recv.name + "." + op.name
	}
	for i := 0; i < sig.Params().Len(); i++ {
		arg := sig.Params().At(i).Type()
		n := s.callNode(arg)
		if s.concurrentParams[sig.Params().At(i).Pos()] {
			if n.kind != nodeInterface || n.shape == nil || n.shape.Kind != values.InterfaceKind {
				fail("%s: concurrent parameter requires a named interface", op.key)
			}
			op.concurrentInterfaces = append(op.concurrentInterfaces, i)
		}
		op.args = append(op.args, n)
		op.paramNames = append(op.paramNames, sig.Params().At(i).Name())
	}
	for i := 0; i < sig.Results().Len(); i++ {
		n := s.callNode(sig.Results().At(i).Type())
		if n.inputOnly() {
			fail("%s: %s is only supported as a host input", f.Name(), n.name)
		}
		op.outs = append(op.outs, n)
	}
	s.annotateMutable(op)
	return op
}
func (op *operation) checkCallbackBorrow(b *callbackBorrow) {
	if op.byteBorrow != nil {
		fail("%s: callback borrow conflicts with borrowed result", op.key)
	}
	b.argument = -1
	var callback *node
	if b.parameter != "" {
		b.argument = slices.Index(op.paramNames, b.parameter)
		if b.argument >= 0 {
			callback = op.args[b.argument]
		}
	} else if len(op.outs) == 1 {
		iter := op.outs[0]
		if iter.kind == nodeNativeFunction && len(iter.functionArgs) == 1 && len(iter.functionOuts) == 0 {
			callback = iter.functionArgs[0]
		}
	}
	if callback == nil || callback.kind != nodeNativeFunction || !slices.ContainsFunc(callback.functionArgs, func(n *node) bool { return n.shape.Type == evm.ByteSliceType }) {
		fail("%s: expected callback with a []byte argument", op.key)
	}
}

func nodesTypes(ns []*node) []*values.Type {
	var out []*values.Type
	for _, n := range ns {
		out = append(out, n.shape.Type)
	}
	return out
}
func (op *operation) nativeMethod() string {
	if op.byteBorrow != nil {
		return op.byteBorrow.method
	}
	return op.name
}

func (op *operation) directOpcode() evm.Opcode {
	if len(op.concurrentInterfaces) > 0 || op.variadic || op.byteBorrow != nil || op.callbackBorrow != nil {
		return 0
	}
	for _, n := range append(slices.Clone(op.args), op.outs...) {
		if n.concurrentFunction || n.kind == nodeRecord && n.model.row.Mutable {
			return 0
		}
	}
	var receiver values.Repr
	if op.recv != nil {
		receiver = directBank(op.recv.shape)
		if receiver != values.Pointer {
			return 0
		}
	}
	args, results := make([]values.Repr, len(op.args)), make([]values.Repr, len(op.outs))
	for i, n := range op.args {
		args[i] = directBank(n.shape)
	}
	for i, n := range op.outs {
		results[i] = directBank(n.shape)
	}
	return directOpcode(receiver, args, results)
}
func (op *operation) sig() evm.Signature {
	return evm.Signature{Params: nodesTypes(op.args), Results: nodesTypes(op.outs)}
}

var basics = map[nodeKind]*values.Type{
	nodeFloat32: values.Float32Type,
	nodeFloat64: values.Float64,
	nodeString:  values.StringType,
	nodeBool:    values.Boolean,
	nodeInt:     values.IntType,
	nodeInt8:    values.Int8,
	nodeInt16:   values.Int16,
	nodeInt32:   values.Int32,
	nodeInt64:   values.Int64,
	nodeUint:    values.UintType,
	nodeUint8:   values.Uint8,
	nodeUint16:  values.Uint16,
	nodeUint32:  values.Uint32,
	nodeUint64:  values.Uint64,
}

func (n *node) inputOnly() bool {
	return n.kind == nodeCallback || n.kind == nodeRetainedFunction && (n.boxedFunction || n.concurrentFunction)
}

var errorMethodShape = &values.Shape{
	Type: values.NewType("error.Error", values.Pair), Kind: values.FunctionKind, Cells: 1,
	Results: []*values.Shape{{Type: values.StringType, Kind: values.StringKind, Cells: 1}},
}

// Unnamed func() has structural identity across source schemas.
var voidFunctionType = values.NewType("func()", values.Pair)

var emptyStructType = values.NewType("struct{}", values.Pointer)

func (s *source) node(t types.Type) *node {
	t = canonicalAliasType(t)

	if named, ok := t.(*types.Named); ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "time" && named.Obj().Name() == "Time" {
		if n := s.nodes["time.Time"]; n != nil {
			return n
		}
		n := &node{kind: nodeTimestamp, name: "time.Time", named: true, native: reflect.TypeFor[time.Time](), nativeImport: "time", nativeName: "Time", shape: evm.TimePackage().Types[0].Shape}
		s.nodes[n.name] = n
		return n
	}

	base := t
	if ptr, ok := t.(*types.Pointer); ok {
		base = ptr.Elem()
	}
	if named, ok := base.(*types.Named); ok && named.TypeArgs().Len() > 0 {
		owner := s
		if imp, ok := s.nativeImports[named.Obj().Pkg()]; ok {
			owner = imp.schema.src
		}
		if in := owner.instance(named); in != nil && named != in.typ {
			if _, ptr := t.(*types.Pointer); ptr {
				return s.node(types.NewPointer(in.typ))
			}
			return s.node(in.typ)
		}
	}
	if n := s.genericModelNode(t); n != nil {
		return n
	}
	if ptr, ok := t.(*types.Pointer); ok {
		if named, ok := ptr.Elem().(*types.Named); ok && named.Obj().Pkg() != nil && named.Obj().Pkg() != s.goPackage {
			if imp, ok := s.nativeImports[named.Obj().Pkg()]; ok {
				n := imp.schema.src.nodes["*"+named.Obj().Name()]
				if named.TypeArgs().Len() > 0 {
					if in := imp.schema.src.instance(named); in != nil {
						n = imp.schema.src.node(types.NewPointer(in.typ))
					}
				}
				if n == nil {
					fail("unregistered imported pointer type %s", t)
				}
				return s.importNode(imp.path, imp.schema, n)
			}
		}
	}
	if named, ok := t.(*types.Named); ok && named.Obj().Pkg() != nil && named.Obj().Pkg() != s.goPackage {
		if imp, ok := s.nativeImports[named.Obj().Pkg()]; ok {
			n := imp.schema.src.nodes[named.Obj().Name()]
			if named.TypeArgs().Len() > 0 {
				if in := imp.schema.src.instance(named); in != nil {
					n = imp.schema.src.node(in.typ)
				}
			}
			if n == nil {
				fail("unregistered imported type %s", named)
			}
			return s.importNode(imp.path, imp.schema, n)
		}
	}
	name := types.TypeString(t, func(p *types.Package) string {
		if p.Path() == "time" {
			if _, named := t.(*types.Named); !named {
				return "time"
			}
		}
		if p == s.goPackage {
			return ""
		}
		if imp, ok := s.nativeImports[p]; ok {
			return s.importQualifier(imp.path, imp.schema)
		}
		return ""
	})
	if ptr, ok := t.(*types.Pointer); ok {
		if named, ok := ptr.Elem().(*types.Named); ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "time" && named.Obj().Name() == "Time" {
			name = "*time.Time"
		}
	}
	if n := s.nodes[name]; n != nil {
		if n.kind == nodePointer && n.shape.Type == nil {
			// A collection can close a cycle through a pointer still being
			// parsed. Its named struct already has an identity; publish the
			// pointer identity before the collection uses it.
			n.elem = s.node(t.(*types.Pointer).Elem())
			if n.elem.kind != nodeStruct && n.elem.kind != nodeTimestamp && basics[n.elem.kind] == nil {
				fail("expected pointer to data struct or scalar: %s", name)
			}
			n.shape.Elem = n.elem.shape
			n.shape.Type = collectionType(n)
		}
		return n
	}
	if n := s.modelNode(t, name); n != nil {
		return n
	}
	n := &node{omittedFields: slices.Clone(s.omittedFields[name]), name: name, shape: &values.Shape{Cells: 1}}
	s.nodes[name] = n
	if types.Identical(t, types.Universe.Lookup("error").Type()) {
		n.kind = nodeError
		n.shape.Type = values.ErrorType
		n.shape.Kind = values.InterfaceKind
		n.shape.Methods = []values.Method{{ID: "Error", Signature: errorMethodShape}}
		s.pkg.Types = append(s.pkg.Types, evm.TypeDecl{Shape: n.shape, NativeCodec: evm.NativeErrorCodec})
		return n
	}
	named, ok := t.(*types.Named)
	n.named = ok
	if ok && named.TypeArgs().Len() > 0 {
		in := s.instance(named)
		if in == nil {
			fail("unregistered generic instantiation %s; add Options.Instances", named)
		}
		n.genericInstance = true
		n.genericType = named
		n.omittedFields = slices.Clone(s.omittedFields[named.Obj().Name()])
	}
	if ok && (!named.Obj().Exported() || named.TypeParams().Len() > 0 && s.instance(named) == nil) {
		fail("unsupported native binding type %s", name)
	}
	sh := n.shape
	if types.Identical(t, types.Universe.Lookup("any").Type()) {
		n.kind = nodeAny
		sh.Type = values.AnyType
		sh.Kind = values.InterfaceKind
		s.pkg.Types = append(s.pkg.Types, evm.TypeDecl{Shape: sh, NativeCodec: evm.NativeAnyCodec})
		// Ordinary dynamic containers must also have native storage when they
		// occur only inside any, rather than in a declared host signature.
		s.node(types.NewSlice(t))
		s.node(types.NewMap(types.Typ[types.String], t))
		return n
	}
	if ptr, ok := t.(*types.Pointer); ok {
		if alias, ok := ptr.Elem().(*types.Alias); ok && isFunctionOf(alias) {
			n.concurrentFunction = alias.Obj().Name() == "ConcurrentFunctionOf"
			if types.Identical(alias.TypeArgs().At(0), types.Universe.Lookup("any").Type()) {
				n.kind = nodeRetainedFunction
				n.boxedFunction = true
				n.shape = s.node(types.Universe.Lookup("any").Type()).shape
				return n
			}
			sig, ok := alias.TypeArgs().At(0).Underlying().(*types.Signature)
			if !ok || sig.Variadic() {
				fail("FunctionOf requires a nonvariadic function signature: %s", name)
			}
			n.kind = nodeRetainedFunction
			sh.Kind, sh.Type = values.FunctionKind, values.NewType(name, values.Pair)
			if sig.Params().Len() == 0 && sig.Results().Len() == 0 {
				sh.Type = voidFunctionType
			}
			for i := 0; i < sig.Params().Len(); i++ {
				sh.Params = append(sh.Params, s.functionShape(sig.Params().At(i).Type()))
			}
			for i := 0; i < sig.Results().Len(); i++ {
				sh.Results = append(sh.Results, s.functionShape(sig.Results().At(i).Type()))
			}
			for _, p := range sh.Params {
				n.functionSignature.Params = append(n.functionSignature.Params, p.Type)
			}
			for _, r := range sh.Results {
				n.functionSignature.Results = append(n.functionSignature.Results, r.Type)
			}
			n.functionSignatureReady = true
			for _, other := range s.nodes {
				if other != n && other.functionSignatureReady && !other.boxedFunction && (other.kind == nodeCallback || other.kind == nodeRetainedFunction) && slices.Equal(other.functionSignature.Params, n.functionSignature.Params) && slices.Equal(other.functionSignature.Results, n.functionSignature.Results) {
					n.shape = other.shape
					return n
				}
			}
			s.pkg.Types = append(s.pkg.Types, evm.TypeDecl{Shape: sh})
			return n
		}
	}
	repr := values.Raw
	switch u := t.Underlying().(type) {
	case *types.Basic:
		n.kind = basicNodeKind(u.Kind())
		base := basics[n.kind]
		if base == nil {
			fail("unsupported scalar %s", name)
		}
		sh.Type = base
		repr = base.Repr()
		switch {
		case u.Info()&types.IsString != 0:
			sh.Kind = values.StringKind
		case u.Info()&types.IsBoolean != 0:
			sh.Kind = values.BoolKind
		case u.Info()&types.IsFloat != 0:
			sh.Kind = values.FloatKind
		case u.Info()&types.IsUnsigned != 0:
			sh.Kind = values.UintKind
		default:
			sh.Kind = values.IntKind
		}
		if u.Info()&(types.IsInteger|types.IsFloat) != 0 {
			sh.Bits = uint8(types.SizesFor("gc", "amd64").Sizeof(u) * 8)
		}
	case *types.Signature:
		n.kind, repr, sh.Kind = nodeCallback, values.Pair, values.FunctionKind
		if n.named || u.Params().Len() != 0 || u.Results().Len() != 0 {
			if u.Variadic() {
				fail("variadic native callback %s is unsupported", name)
			}
			n.kind = nodeNativeFunction
			for i := 0; i < u.Params().Len(); i++ {
				a := s.callNode(u.Params().At(i).Type())
				n.functionArgs = append(n.functionArgs, a)
				sh.Params = append(sh.Params, a.shape)
				n.functionSignature.Params = append(n.functionSignature.Params, a.shape.Type)
			}
			for i := 0; i < u.Results().Len(); i++ {
				a := s.callNode(u.Results().At(i).Type())
				n.functionOuts = append(n.functionOuts, a)
				sh.Results = append(sh.Results, a.shape)
				n.functionSignature.Results = append(n.functionSignature.Results, a.shape.Type)
			}
		}
	case *types.Slice:
		n.kind = nodeSlice
		n.elem = s.node(u.Elem())
		if n.elem.kind == nodeRecord && n.elem.nativeLayout == nil {
			fail("native model value slice requires matching NativeAllocation: %s", name)
		}
		if n.elem.kind == nodeError {
			fail("native []error storage is not supported")
		}
		if n.kind == nodeSlice && (n.elem.inputOnly() || n.elem.kind == nodeRetainedFunction) {
			fail("unsupported native collection element %s", u.Elem())
		}
		repr = values.Pair
		sh.Kind = values.SliceKind
		sh.Elem = s.node(u.Elem()).shape
	case *types.Map:
		repr = values.Pointer
		sh.Kind = values.MapKind
		n.key = s.node(u.Key())
		sh.Key = n.key.shape
		n.kind = nodeMap
		n.elem = s.node(u.Elem())
		if n.key.inputOnly() || n.key.kind == nodeInterface || n.elem.inputOnly() || n.elem.kind == nodeRetainedFunction {
			fail("unsupported native collection type %s", name)
		}
		if n.key.kind == nodeRecord || n.elem.kind == nodeRecord {
			fail("native model collection layout is not yet bound: %s", name)
		}
		sh.Elem = n.elem.shape

	case *types.Struct:
		if n.named {
			// Recursive pointers need the named identity before its fields.
			sh.Type = values.NewType(s.pkg.Path+"."+s.guestTypeName(name), values.Pointer)
		}
		if !n.named {
			if u.NumFields() != 0 {
				fail("anonymous struct %s", name)
			}
			n.native = reflect.TypeFor[struct{}]()
		}
		repr = values.Pointer
		n.kind = nodeStruct
		sh.Kind = values.StructKind
		sh.Cells = 0
		for i := 0; i < u.NumFields(); i++ {
			f := u.Field(i)
			if !f.Exported() {
				fail("unsupported field %s.%s", name, f.Name())
			}
			c := s.node(f.Type())
			if c.kind == nodeRecord && c.nativeLayout == nil {
				fail("native model value field requires explicit pointer storage: %s.%s", name, f.Name())
			}
			if c.inputOnly() {
				fail("reference field %s.%s is not supported", name, f.Name())
			}
			n.fields = append(n.fields, field{f.Name(), c, sh.Cells})
			sh.Fields = append(sh.Fields, values.Field{Name: f.Name(), Tag: u.Tag(i), Shape: c.shape, Offset: sh.Cells, Embedded: f.Embedded()})
			sh.Cells += c.shape.Cells
		}
		if len(n.omittedFields) > 0 {
			sh.Cells++
		}
		if cell := s.cellStructs[name]; cell != nil {
			if len(n.fields) != 2 || cell.Type == nil || cell.Type.Repr() != values.Pair || cell.Get == nil || cell.Pack == nil || cell.LoadNative == nil || cell.StoreNative == nil || len(n.omittedFields) != 0 {
				fail("invalid cell struct %s", name)
			}
			sh.CellStruct, sh.Type, sh.Kind, sh.Cells = cell, cell.Type, values.TypedPairKind, 1
			repr = values.Pair
		}
	case *types.Pointer:
		if n.named {
			fail("named pointer type %s is unsupported", name)
		}
		repr = values.Pair
		n.kind = nodePointer
		sh.Kind = values.PointerKind
		n.elem = s.node(u.Elem())
		sh.Elem = n.elem.shape
		if n.elem.kind != nodeStruct && n.elem.kind != nodeTimestamp && basics[n.elem.kind] == nil {
			fail("expected pointer to data struct or scalar: %s", name)
		}
	case *types.Interface:
		repr = values.Pair
		n.kind = nodeInterface
		sh.Kind = values.InterfaceKind
		if !u.IsMethodSet() || !n.named {
			fail("expected named method interface: %s", name)
		}
		for i := 0; i < u.NumMethods(); i++ {
			m := u.Method(i)
			if !m.Exported() {
				fail("unexported interface method %s", m.Name())
			}
			op := s.op(m, n)
			n.methods = append(n.methods, op)
			fs := &values.Shape{Type: values.NewType(op.key, values.Pair), Kind: values.FunctionKind, Cells: 1, Variadic: op.variadic}
			for _, x := range op.args {
				fs.Params = append(fs.Params, x.shape)
			}
			for _, x := range op.outs {
				fs.Results = append(fs.Results, x.shape)
			}
			sh.Methods = append(sh.Methods, values.Method{ID: m.Name(), Signature: fs})
		}
	default:
		fail("unsupported host boundary type %s", name)
	}
	// Bare func() callbacks are input-only. They use the same guest function
	// identity as an opaque handle; the binding node still selects how the
	// host receives that function.
	if n.kind == nodeCallback {
		n.functionSignatureReady = true
		for _, other := range s.nodes {
			if other != n && other.functionSignatureReady && !other.boxedFunction && (other.kind == nodeRetainedFunction) && len(other.functionSignature.Params) == 0 && len(other.functionSignature.Results) == 0 {
				n.shape = other.shape
				return n
			}
		}
	}
	if n.named || n.kind == nodePointer || sh.Type == nil {
		if !n.named && n.kind == nodeCallback {
			sh.Type = voidFunctionType
		} else if !n.named && n.kind == nodeStruct {
			sh.Type = emptyStructType
		} else if !n.named && (n.kind == nodeSlice || n.kind == nodeMap || n.kind == nodePointer) {
			sh.Type = collectionType(n)
		} else if !n.named || n.kind != nodeStruct {
			sh.Type = values.NewType(s.pkg.Path+"."+s.guestTypeName(name), repr)
		}
		decl := evm.TypeDecl{Shape: sh}
		if n.named {
			if named.TypeArgs().Len() > 0 {
				decl.SourceName = named.Obj().Name()
				decl.TypeArgs = s.instanceTypeArgs(named)
			} else {
				decl.Name = s.guestTypeName(name)
				decl.ScalarBase = basics[n.kind]
			}
		}
		s.pkg.Types = append(s.pkg.Types, decl)
	}
	return n
}

// Function signatures describe guest values; they do not marshal each argument
// through the native registration call that receives the opaque handle.
func (s *source) functionShape(t types.Type) *values.Shape {
	t = canonicalAliasType(t)
	if types.Identical(t, types.Universe.Lookup("any").Type()) {
		return &values.Shape{Type: values.AnyType, Kind: values.InterfaceKind, Cells: 1}
	}
	// Reuse collection/callback identities already supported by native calls.
	if types.Identical(t, types.NewSlice(types.Typ[types.String])) {
		return s.node(t).shape
	}
	if sig, ok := t.(*types.Signature); ok && !sig.Variadic() && sig.Params().Len() == 0 && sig.Results().Len() == 0 {
		return s.node(t).shape
	}
	switch t := t.(type) {
	case *types.Slice, *types.Signature:
		name := types.TypeString(t, nil)
		if shape := s.functionShapes[name]; shape != nil {
			return shape
		}
		if s.functionShapes == nil {
			s.functionShapes = make(map[string]*values.Shape)
		}
		shape := &values.Shape{Type: values.NewType(name, values.Pair), Cells: 1}
		s.functionShapes[name] = shape
		switch t := t.(type) {
		case *types.Slice:
			shape.Kind, shape.Elem = values.SliceKind, s.functionShape(t.Elem())
			shape.Type = (collectionTypeKey{kind: nodeSlice, elem: shape.Elem.Type}).typeValue()
		case *types.Signature:
			if t.Variadic() || t.TypeParams().Len() != 0 {
				fail("unsupported function signature %s", name)
			}
			shape.Kind = values.FunctionKind
			for i := 0; i < t.Params().Len(); i++ {
				shape.Params = append(shape.Params, s.functionShape(t.Params().At(i).Type()))
			}
			for i := 0; i < t.Results().Len(); i++ {
				shape.Results = append(shape.Results, s.functionShape(t.Results().At(i).Type()))
			}
		}
		s.pkg.Types = append(s.pkg.Types, evm.TypeDecl{Shape: shape})
		return shape
	}
	return s.callNode(t).shape
}

func (s *source) guestTypeName(name string) string {
	if guest := s.typeNames[name]; guest != "" {
		return guest
	}
	return name
}

// explainImportError names the exposed declarations that need a package the
// binding cannot import, so the host knows what to omit.
func explainImportError(err error, files []*ast.File) error {
	var typeErr types.Error
	if !errors.As(err, &typeErr) {
		return err
	}
	rest, ok := strings.CutPrefix(typeErr.Msg, "could not import ")
	if !ok {
		return err
	}
	importPath, _, _ := strings.Cut(rest, " ")
	var users []string
	for _, file := range files {
		local := ""
		for _, imp := range file.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == importPath {
				local = path.Base(p)
				if imp.Name != nil {
					local = imp.Name.Name
				}
			}
		}
		if local == "" {
			continue
		}
		for _, decl := range file.Decls {
			uses := false
			ast.Inspect(decl, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == local {
						uses = true
					}
				}
				return !uses
			})
			if !uses {
				continue
			}
			switch d := decl.(type) {
			case *ast.FuncDecl:
				name := d.Name.Name
				if d.Recv != nil {
					name = embeddedFieldName(d.Recv.List[0].Type) + "." + name
				}
				users = append(users, name)
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch spec := spec.(type) {
					case *ast.TypeSpec:
						users = append(users, spec.Name.Name)
					case *ast.ValueSpec:
						for _, name := range spec.Names {
							users = append(users, name.Name)
						}
					}
				}
			}
		}
	}
	if len(users) == 0 {
		return err
	}
	sort.Strings(users)
	return fmt.Errorf("%w: needed by %s; import a schema for %s through Options.Imports, omit the field with Options.OmitFields, or leave the declaration out with Options.Omit", err, strings.Join(users, ", "), importPath)
}

// declareBoundValues rewrites each variable bound in Options.Values into a
// bodyless function of the same name and type. Selection, type checking and
// binding then treat reading the variable as calling that function.
func declareBoundValues(files []*ast.File, opt Options) error {
	pending := maps.Clone(opt.Values)
	for name := range opt.Values {
		if _, ok := opt.Functions[name]; ok {
			return fmt.Errorf("%s is bound both as a value and as a function", name)
		}
	}
	for _, file := range files {
		var decls []ast.Decl
		for _, decl := range file.Decls {
			decls = append(decls, decl)
			g, ok := decl.(*ast.GenDecl)
			if !ok || g.Tok != token.VAR {
				continue
			}
			for _, spec := range g.Specs {
				v := spec.(*ast.ValueSpec)
				for _, name := range v.Names {
					if _, bound := pending[name.Name]; !bound {
						continue
					}
					if !name.IsExported() {
						return fmt.Errorf("bound value %s is not exported", name.Name)
					}
					if v.Type == nil {
						return fmt.Errorf("bound value %s needs a declared type in source", name.Name)
					}
					decls = append(decls, &ast.FuncDecl{Name: ast.NewIdent(name.Name), Type: &ast.FuncType{Func: name.Pos(), Params: &ast.FieldList{}, Results: &ast.FieldList{List: []*ast.Field{{Type: v.Type}}}}})
					delete(pending, name.Name)
				}
			}
		}
		file.Decls = decls
	}
	for name := range pending {
		return fmt.Errorf("bound value %s is not declared as a variable in source", name)
	}
	return nil
}

// //evm:concurrent names interface parameters on a function or interface method.
func collectConcurrentParams(file *ast.File, result map[token.Pos]bool) {
	mark := func(doc *ast.CommentGroup, sig *ast.FuncType) {
		if doc == nil {
			return
		}
		for _, comment := range doc.List {
			words := strings.Fields(comment.Text)
			if len(words) == 0 || words[0] != "//evm:concurrent" {
				continue
			}
			if len(words) != 2 {
				fail("expected //evm:concurrent parameter")
			}
			found := false
			for _, field := range sig.Params.List {
				for _, name := range field.Names {
					if name.Name == words[1] {
						if result[name.Pos()] {
							fail("duplicate concurrent parameter %s", name)
						}
						result[name.Pos()] = true
						found = true
					}
				}
			}
			if !found {
				fail("unknown concurrent parameter %s", words[1])
			}
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			mark(x.Doc, x.Type)
			return false
		case *ast.InterfaceType:
			for _, m := range x.Methods.List {
				if sig, ok := m.Type.(*ast.FuncType); ok {
					mark(m.Doc, sig)
				}
			}
			return false
		}
		return true
	})
}
