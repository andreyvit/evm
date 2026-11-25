package evmbind

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"
	"unsafe"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/nativebind"
	"github.com/andreyvit/evm/values"
)

type Mode = nativebind.Mode

const (
	Automatic  = nativebind.Automatic
	StaticOnly = nativebind.StaticOnly
)

type Wrapper func(*Frame)

// DirectFactory binds a generated register wrapper once per external receiver.
type DirectFactory func(host any) evm.DirectFunc

type Static struct {
	Fields      map[string]*nativebind.GeneratedFields
	Direct      map[string]DirectFactory
	Types       map[string]reflect.Type
	Fingerprint string
	Calls       map[string]Wrapper
}

// MutableSlots selects zero-based explicit parameters and results, excluding
// method receivers and the host-only *evm.VM argument. Only direct pointers
// to modeled native structs are supported.
type MutableSlots struct{ Params, Results []int }

type Options struct {
	// InterfaceAdapters builds native implementations for concurrent interface
	// parameters. Each factory receives the checked methods bound to one guest receiver.
	InterfaceAdapters map[reflect.Type]func(map[string]evm.Callable) any
	// Selective exposes only what the options name, instead of everything the
	// source exports: the types in Only (every exported type when Only is nil),
	// the methods in Methods, the functions in Functions and GenericFunctions,
	// and the typed constants of exposed types. Use it to pick declarations
	// out of a large native package.
	Selective bool
	// Only lists the types a selective schema exposes, together with the local
	// types they reference. A non-nil list implies Selective.
	Only []string
	// Omit leaves exported declarations out of a schema that exposes everything.
	// Entries name a type, function or constant, or a method as Type.Method.
	Omit []string

	// CellStructs replaces value-copy storage while retaining native struct fields.
	CellStructs map[string]*values.CellStruct

	// Mutable exposes selected native model pointer slots as editable rows.
	// Keys are Function or Type.Method, using native declaration names.
	Mutable map[string]MutableSlots

	// TypeNames renames nongeneric native type declarations in the guest package.
	// Fields, Methods and native lookup keep their original declaration names.
	// Method signatures and memory layout keep their original native types.
	// Model rows cannot be renamed here; configure their model row names.
	TypeNames map[string]string

	// GenericFunctions supplies compiled native instances of generic package functions.
	GenericFunctions map[string][]FunctionInstance
	// Instances selects concrete generic struct instantiations. Unlisted
	// instantiations are rejected; field and method selections use template names.
	Instances []Instance
	// Values binds exported package variables to their native Go values. Only
	// bound variables are exposed; the source must declare their types.
	// Scripts read a variable and use its methods; they cannot assign it.
	Values map[string]any
	// Functions binds package functions to their actual native Go values.
	// A selective schema exposes exactly these functions. Otherwise every
	// exported function in the source needs a binding here, in
	// GenericFunctions or as a Host method.
	Functions map[string]any

	// Models supplies the shared model package. Native model structs use its
	// immutable row types instead of separate native struct declarations.
	Models *evm.Package
	// NativePackage identifies the original Go package when its script path differs.
	// If omitted, selected native Types supply the package identity.
	NativePackage string
	// Methods selects the exposed native methods. A nil map exposes every
	// exported method, unless the schema is selective. Otherwise, for
	// interfaces, a listed type selects direct methods or whole embedded
	// interfaces by name; an empty list exposes none, an absent type keeps all.
	// For concrete types, unlisted methods remain outside the source API.
	Methods map[string][]string

	// Fields selects the exposed direct fields of a native struct. An empty
	// list exposes none; an absent type keeps the default of exposing all fields.
	// Fields and OmitFields cannot both configure the same type.
	Fields map[string][]string

	// OmitFields hides selected native struct fields from script declarations.
	// Native memory and offsets remain unchanged.
	OmitFields map[string][]string
	// Imports maps native Go import paths to shared script declaration schemas.
	// Imported types retain the dependency's nominal identity and native layout.
	Imports map[string]*Schema
	Mode    Mode
	Static  *Static
	// Host is the concrete receiver of package functions. Its methods take a
	// leading *evm.VM for access to the session and source position.
	Host reflect.Type
	// Types associate guest names with native Go declarations.
	Types []reflect.Type
	// Implementations are pointer types admitted into guest interfaces.
	Implementations []Implementation
}

// Implementation binds one concrete Go pointer type to its program-local ID.
// The conversion functions preserve the pointer without allocating an interface cell.
type Implementation struct {
	typ      reflect.Type
	load     func(unsafe.Pointer) any
	pointer  func(any) unsafe.Pointer
	receiver func(unsafe.Pointer, *evm.VM) any
}

func Pointer[T any]() Implementation {
	return Implementation{typ: reflect.TypeFor[*T](), load: func(p unsafe.Pointer) any { return (*T)(p) }, pointer: func(x any) unsafe.Pointer { return unsafe.Pointer(x.(*T)) }}
}

// PointerForCall lets a native receiver use the current external call (for
// example to narrow a budget). The interface value still carries the original
// pointer. The receiver returned by bind is used only for this method call.
func PointerForCall[T any](bind func(*T, *evm.VM) *T) Implementation {
	impl := Pointer[T]()
	impl.receiver = func(p unsafe.Pointer, c *evm.VM) any { return bind((*T)(p), c) }
	return impl
}

type nativePointer struct {
	Implementation

	vmType *values.Type
}
type entry struct {
	function any
	direct   DirectFactory
	op       *operation
	wrapper  Wrapper
	pointer  *nativePointer
}
type Schema struct {
	interfaceAdapters map[reflect.Type]func(map[string]evm.Callable) any
	nodesByNative     map[*node]reflect.Type
	values            []entry // bound package variables, parallel to pkg.Values
	src               *source
	pkg               evm.Package
	funcs, methods    []entry
	host              reflect.Type
	pointers          map[reflect.Type]*nativePointer
	reverse           map[*values.Type]*nativePointer
	static            bool
	fields            map[reflect.Type]*nativebind.Struct
	nativeTypes       map[reflect.Type]*values.Type
	nativeModels      map[reflect.Type]*node
	nativeFunctions   map[*node]*nativeFunctionBinding
}

func (s *Schema) UsesStatic() bool     { return s.static }
func (s *Schema) Package() evm.Package { return s.pkg }
func (s *Schema) Type(name string) *values.Type {
	if n := s.src.aliases[name]; n != nil {
		return n.shape.Type
	}
	if n := s.src.nodes[name]; n != nil {
		return n.shape.Type
	}
	return nil
}

// New builds a schema from the Go source of a package. It exposes every
// exported type with its exported methods, every exported package function
// and every exported constant. Function bodies, variables, unexported
// declarations and unexported struct fields are ignored, so the package's own
// source can be embedded and supplied as is.
//
// Go cannot look a function or type up by name, so the host supplies them:
// Options.Functions binds package functions, and Options.Types adds native
// types that no bound signature or struct field reaches. A declaration with
// no native counterpart, or with a type the binding does not support, fails
// schema construction; Options.Omit leaves it out. Options.Selective and
// Options.Only expose a chosen part of a larger package instead.
//
// Cache the returned schema and reuse it across environments; parsing and
// type checking happen only here.
func New(path string, files fs.FS, opt Options) (schema *Schema, err error) {
	src, err := parseOptions(path, files, opt.Only, opt.Selective || opt.Only != nil, opt)
	if err != nil {
		return nil, err
	}
	return newSchema(src, opt)
}

func newSchema(src *source, opt Options) (schema *Schema, err error) {
	path := src.pkg.Path
	defer func() {
		if x := recover(); x != nil {
			if e, ok := x.(schemaError); ok {
				schema = nil
				err = e
			} else {
				panic(x)
			}
		}
	}()
	s := &Schema{interfaceAdapters: maps.Clone(opt.InterfaceAdapters), src: src, pkg: src.pkg, host: opt.Host, pointers: map[reflect.Type]*nativePointer{}, reverse: map[*values.Type]*nativePointer{}}
	if opt.Mode > StaticOnly {
		fail("invalid binding mode")
	}
	var expected []*operation
	expected = append(expected, src.ops...)
	boundFunctions := make(map[string]any, len(opt.Functions)+len(opt.Values))
	for name, value := range opt.Functions {
		fn := reflect.ValueOf(value)
		if !fn.IsValid() || fn.Kind() != reflect.Func || fn.IsNil() {
			fail("invalid native function %s", name)
		}
		if fn.Type().Name() != "" {
			typ := fn.Type()
			inputs := make([]reflect.Type, typ.NumIn())
			outputs := make([]reflect.Type, typ.NumOut())
			for i := range inputs {
				inputs[i] = typ.In(i)
			}
			for i := range outputs {
				outputs[i] = typ.Out(i)
			}
			fn = fn.Convert(reflect.FuncOf(inputs, outputs, typ.IsVariadic()))
		}
		boundFunctions[name] = fn.Interface()
		if !slices.ContainsFunc(src.ops, func(op *operation) bool { return op.recv == nil && op.name == name }) {
			fail("native function %s is not declared in source", name)
		}
	}
	var interfaces []*node
	for _, n := range sortedNodes(src) {
		if n.imported == nil && n.kind == nodeInterface {
			interfaces = append(interfaces, n)
			expected = append(expected, n.methods...)
		}
	}
	if src.hasGenericInstances() && opt.Mode == StaticOnly {
		fail("generic native instances do not support StaticOnly bindings")
	}
	native := resolveNativeTypes(src, opt)
	for n, t := range native {
		n.native = t
	}
	// A bound variable reads through a function that returns its value, typed
	// as the source declares it, so the ordinary call machinery converts it.
	functions := maps.Clone(opt.Functions)
	for _, op := range src.ops {
		if !op.value {
			continue
		}
		declared, value := native[op.outs[0]], reflect.ValueOf(opt.Values[op.name])
		if !value.IsValid() || declared == nil || !value.Type().AssignableTo(declared) {
			fail("native value %s does not have the declared type %s", op.name, op.outs[0].name)
		}
		result := reflect.New(declared).Elem()
		result.Set(value)
		if functions == nil {
			functions = map[string]any{}
		}
		functions[op.name] = reflect.MakeFunc(reflect.FuncOf(nil, []reflect.Type{declared}, false), func([]reflect.Value) []reflect.Value { return []reflect.Value{result} }).Interface()
		boundFunctions[op.name] = functions[op.name]
	}
	var fingerprint string
	compatible := opt.Static != nil && !src.hasGenericInstances()
	if opt.Static != nil && !src.hasGenericInstances() {
		fingerprint = opt.Static.Fingerprint
		compatible = len(opt.Static.Calls) == len(expected)
		validDirect := 0
		for _, op := range expected {
			if opt.Static.Direct[op.key] != nil && op.directOpcode() != 0 {
				validDirect++
			}
			if opt.Static.Calls[op.key] == nil {
				compatible = false
				break
			}
		}
		compatible = compatible && validDirect == len(opt.Static.Direct)
		for _, n := range src.nodes {
			if n.named && native[n] != opt.Static.Types[n.name] {
				compatible = false
				break
			}
		}
	}
	s.static = opt.Mode.Select(path, src.fingerprint, fingerprint, compatible)
	s.nodesByNative = native
	s.fields = make(map[reflect.Type]*nativebind.Struct)
	s.nativeTypes = make(map[reflect.Type]*values.Type)
	s.nativeModels = make(map[reflect.Type]*node)
	for name, model := range src.models {
		if src.goPackage.Scope().Lookup(modelBaseName(name)) == nil {
			continue
		}
		t := reflect.PointerTo(model.native)
		if model.ambiguous {
			s.nativeModels[t] = nil
		} else {
			s.nativeModels[t] = &node{kind: nodeRecord, model: model, native: t}
		}
	}
	s.nativeFunctions = make(map[*node]*nativeFunctionBinding)
	for n, t := range native {
		if n.kind == nodeNativeFunction {
			s.nativeTypes[t] = n.shape.Type
			if n.imported != nil {
				s.nativeFunctions[n] = n.imported.nativeFunctions[n.origin]
				if s.nativeFunctions[n] == nil {
					fail("missing native function binding %s", n.name)
				}
				continue
			}
			s.installNativeFunction(n, t, native)
			continue
		}
		if n.kind == nodeRetainedFunction {
			if n.imported != nil || n.boxedFunction {
				continue
			}
			n.shape.Native = nativebind.Layout(t, nil, s.static)
			codec := &evm.NativeValueCodec{
				Decode: func(c *evm.VM, v values.Value) (values.Value, error) {
					return (*evm.Callable)(v.P).Value(c.Program(), n.functionSignature)
				},
				Encode: func(c *evm.VM, v values.Value) (values.Value, error) {
					if v.R == 0 {
						return values.Value{}, nil
					}
					if err := c.ChargeAllocation(uint64(unsafe.Sizeof(evm.Callable{}))); err != nil {
						return values.Value{}, err
					}
					fn, err := c.Program().Callable(v)
					if err != nil {
						return values.Value{}, err
					}
					if err = fn.CheckSignature(n.functionSignature); err != nil {
						return values.Value{}, err
					}
					return values.Value{P: unsafe.Pointer(&fn)}, nil
				},
			}
			for i := range src.pkg.Types {
				if src.pkg.Types[i].Shape.Type == n.shape.Type {
					src.pkg.Types[i].NativeCodec = codec
				}
			}
			continue
		}
		if n.kind == nodeRecord {
			if n.model.row.Mutable {
				continue
			}
			if n.native.Kind() == reflect.Pointer {
				if old, ok := s.nativeModels[t]; ok && (old == nil || old.model.row.Model != n.model.row.Model) {
					s.nativeModels[t] = nil
				} else {
					s.nativeModels[t] = n
				}
			}
			continue
		}
		if n.kind == nodeCallback {
			continue
		}
		s.nativeTypes[t] = n.shape.Type
		if n.imported != nil {
			if n.kind == nodeStruct {
				s.fields[t] = n.imported.fields[t]
			}
			continue
		}
		if isModelSlice(n) {
			n.nativeLayout = modelSliceLayout(n, native, s.static)
			for i := range s.pkg.Types {
				if s.pkg.Types[i].Shape == n.shape {
					s.pkg.Types[i].NativeSlice = &evm.NativeSliceDecl{Layout: n.nativeLayout, Element: n.elem.nativeLayout}
				}
			}
			continue
		}
		n.shape.Native = nativebind.Layout(t, nil, s.static)
		if n.kind == nodeInterface {
			codec := evm.NativeInterfaceCodec(t)
			for i := range s.pkg.Types {
				if s.pkg.Types[i].Shape == n.shape {
					s.pkg.Types[i].NativeCodec = codec
				}
			}
		}
		if n.kind == nodeStruct {
			var g *nativebind.GeneratedFields
			if s.static {
				g = opt.Static.Fields[n.name]
			}
			var selected []string
			if len(n.omittedFields) > 0 {
				for _, field := range n.fields {
					selected = append(selected, field.name)
				}
				if selected == nil {
					selected = []string{}
				}
			}
			s.fields[t] = nativebind.BindSelectedFields(t, selected, g, opt.Mode)
			n.shape.Native = nativebind.Layout(t, s.fields[t], s.static)
			if len(n.omittedFields) > 0 {
				n.shape.Native.OpaqueCell = n.shape.Cells
			}
			if cell := n.shape.CellStruct; cell != nil {
				n.shape.Native.Load = cell.LoadNative
				n.shape.Native.Store = cell.StoreNative
				n.shape.Native.Codec = &values.NativeValueCodec{
					Decode: func(v values.Value) (values.Value, error) { return v, nil },
					Encode: func(v values.Value, _ func(uint64) error) (values.Value, error) { return v, nil },
				}
			}
		}
	}

	// Resolve field storage after all nodes have their physical layouts.
	for n := range native {
		if n.kind == nodeMap && n.imported == nil {
			n.shape.Native.Map.Key = n.key.nativeLayout
			n.shape.Native.Map.Element = n.elem.nativeLayout
		}
		if n.kind != nodeStruct || n.imported != nil {
			continue
		}
		for i, field := range n.fields {
			if field.t.nativeLayout != nil {
				n.shape.Native.Fields[i].Layout = field.t.nativeLayout
			}
		}
	}

	wrappers := map[string]Wrapper{}
	for _, op := range expected {
		if len(op.concurrentInterfaces) > 0 {
			if s.pkg.ConcurrentInterfaceParams == nil {
				s.pkg.ConcurrentInterfaceParams = map[string][]int{}
			}
			s.pkg.ConcurrentInterfaceParams[strings.TrimPrefix(op.key, "*")] = op.concurrentInterfaces
			for _, i := range op.concurrentInterfaces {
				if s.interfaceAdapters[native[op.args[i]]] == nil {
					fail("%s: missing InterfaceAdapters entry for %s", op.key, op.args[i].name)
				}
			}
		}
		for i, arg := range op.args {
			if arg.concurrentFunction {
				if s.pkg.ConcurrentCallbackParams == nil {
					s.pkg.ConcurrentCallbackParams = map[string][]int{}
				}
				key := strings.TrimPrefix(op.key, "*")
				s.pkg.ConcurrentCallbackParams[key] = append(s.pkg.ConcurrentCallbackParams[key], i)
			}
		}
		// Validate the actual host ABI once, even when generated code is supplied.
		w := reflectWrapper(op, opt.Host, native, functions[op.name])
		if s.static {
			w = opt.Static.Calls[op.key]
		}
		wrappers[op.key] = w
	}
	for _, op := range src.ops {
		e := entry{op: op, wrapper: wrappers[op.key], function: boundFunctions[op.name]}
		var direct evm.DirectBinding
		if s.static && opt.Static.Direct[op.key] != nil {
			direct.Op = op.directOpcode()
			direct.Window = op.recv == nil
			e.direct = opt.Static.Direct[op.key]
		}
		if op.value {
			s.pkg.Values = append(s.pkg.Values, evm.ExternalValue{Name: op.name, Type: op.outs[0].shape.Type})
			s.values = append(s.values, e)
		} else if op.recv == nil {
			s.pkg.Functions = append(s.pkg.Functions, evm.ExternalFunction{ParamNames: op.paramNames, Doc: op.doc, Direct: direct, Name: op.name, Signature: op.sig(), Variadic: op.variadic})
			s.funcs = append(s.funcs, e)
		} else {
			s.method(op, op.recv.shape.Type, e, direct)
		}
	}
	s.installGenericFunctions(native)
	for _, impl := range opt.Implementations {
		s.addImplementation(impl, interfaces, native, wrappers)
	}
	if src.nodes["any"] != nil || len(interfaces) != 0 || len(s.nativeFunctions) != 0 {
		// Native interface results can contain registered types absent from guest
		// expressions. Link their concrete identities before execution.
		candidates := map[*values.Type]bool{}
		for _, typ := range basics {
			candidates[typ] = true
		}
		for _, n := range src.nodes {
			if n.shape.Kind != values.InterfaceKind && n.shape.Kind != values.FunctionKind {
				candidates[n.shape.Type] = true
			}
		}
		var types []*values.Type
		for typ := range candidates {
			types = append(types, typ)
		}
		slices.SortFunc(types, func(a, b *values.Type) int { return strings.Compare(a.Name(), b.Name()) })
		s.pkg.EntryTypes = append(s.pkg.EntryTypes, types...)
	}
	return s, nil
}
func resolveNativeTypes(src *source, opt Options) map[*node]reflect.Type {
	reg := map[string]reflect.Type{}
	for _, in := range src.instances {
		reg[src.node(in.typ).name] = in.native
	}
	// Go cannot enumerate a package by name. Discover concrete data types from
	// host signatures. Explicit roots cover standalone types such as entry interfaces.
	seenTypes := map[reflect.Type]bool{}
	var collect func(reflect.Type)
	collect = func(t reflect.Type) {
		if t == nil || seenTypes[t] || t == reflect.TypeFor[time.Time]() {
			return
		}
		seenTypes[t] = true
		if t.Kind() == reflect.Func {
			for i := 0; i < t.NumIn(); i++ {
				collect(t.In(i))
			}
			for i := 0; i < t.NumOut(); i++ {
				collect(t.Out(i))
			}
		}
		if t.Kind() == reflect.Map {
			collect(t.Key())
			collect(t.Elem())
		}
		if t.Kind() == reflect.Slice {
			collect(t.Elem())
		}
		if t.Kind() == reflect.Pointer {
			collect(t.Elem())
			return
		}
		n := src.nodes[t.Name()]
		for _, in := range src.instances {
			if in.native == t {
				n = src.node(in.typ)
				break
			}
		}
		if n != nil && n.named {
			if prev := reg[n.name]; prev != nil && prev != t {
				fail("conflicting native type %s", n.name)
			}
			reg[n.name] = t
			if t.Kind() == reflect.Struct {
				for _, field := range n.fields {
					nativeField, ok := t.FieldByName(field.name)
					if !ok || len(nativeField.Index) != 1 {
						fail("native type %s has no direct field %s", t, field.name)
					}
					collect(nativeField.Type)
				}
			}
		}
	}
	for _, fn := range opt.Functions {
		typ := reflect.TypeOf(fn)
		for i := 0; i < typ.NumIn(); i++ {
			collect(typ.In(i))
		}
		for i := 0; i < typ.NumOut(); i++ {
			collect(typ.Out(i))
		}
	}
	for _, t := range opt.Types {
		collect(t)
	}
	for _, v := range opt.Values {
		collect(reflect.TypeOf(v))
	}
	for _, in := range src.instances {
		collect(in.native)
	}
	for _, fn := range src.genericFunctions {
		for _, in := range fn.instances {
			collect(reflect.TypeOf(in.function))
		}
	}
	for _, op := range src.ops {
		if op.recv == nil && opt.Host != nil && !op.packageFunction {
			if m, ok := opt.Host.MethodByName(op.name); ok {
				for i := 2; i < m.Type.NumIn(); i++ {
					collect(m.Type.In(i))
				}
				for i := 0; i < m.Type.NumOut(); i++ {
					collect(m.Type.Out(i))
				}
			}
		}
	}
	if opt.Static != nil {
		for name, t := range opt.Static.Types {
			if reg[name] == nil {
				reg[name] = t
			}
		}
	}
	// Method result types can introduce receivers whose names sort earlier.
	// Visit newly discoverable methods until their signature graph is covered.
	visited := map[*operation]bool{}
	for {
		progress := false
		for _, op := range src.ops {
			if op.recv == nil || visited[op] {
				continue
			}
			base := op.recv
			for base.kind == nodePointer {
				base = base.elem
			}
			if reg[base.name] == nil {
				continue
			}
			receiver := nativeType(op.recv, reg, map[*node]reflect.Type{})
			if method, ok := receiver.MethodByName(op.name); ok {
				for i := 1; i < method.Type.NumIn(); i++ {
					collect(method.Type.In(i))
				}
				for i := 0; i < method.Type.NumOut(); i++ {
					collect(method.Type.Out(i))
				}
			}
			visited[op] = true
			progress = true
		}
		if !progress {
			break
		}
	}
	native := map[*node]reflect.Type{}
	for _, n := range src.nodes {
		nativeType(n, reg, native)
	}
	return native
}
func (s *Schema) addImplementation(impl Implementation, interfaces []*node, native map[*node]reflect.Type, wrappers map[string]Wrapper) {

	t := impl.typ
	if t == nil || t.Kind() != reflect.Pointer || s.pointers[t] != nil || s.nativeTypes[t] != nil {
		fail("invalid host pointer %v", t)
	}
	// An opaque pointer has no guest-accessible fields. Never describe the Go
	// object's memory as EVM cells.
	name := fmt.Sprintf("evmOpaque_%x", sha256.Sum256([]byte(t.Elem().PkgPath()+"."+t.Elem().Name())))
	if s.src.nodes[name] != nil {
		fail("reserved host type %s", name)
	}
	empty := &values.Shape{Type: values.NewType(s.pkg.Path+"."+name, values.Pointer), Kind: values.StructKind}
	ptr := &values.Shape{Type: values.NewType("*"+s.pkg.Path+"."+name, values.Pointer), Kind: values.PointerKind, Elem: empty, Cells: 1, Native: nativebind.Layout(t, nil, true)}
	np := &nativePointer{Implementation: impl, vmType: ptr.Type}
	s.pointers[t] = np
	s.reverse[ptr.Type] = np
	s.pkg.Types = append(s.pkg.Types, evm.TypeDecl{Name: name, Shape: empty}, evm.TypeDecl{Shape: ptr})
	s.pkg.EntryTypes = append(s.pkg.EntryTypes, ptr.Type)
	seen := map[string]*operation{}
	matches := false
	for _, n := range interfaces {
		if t.Implements(native[n]) {
			matches = true
			for _, op := range n.methods {
				if prev := seen[op.name]; prev != nil {
					if !slices.Equal(prev.concurrentInterfaces, op.concurrentInterfaces) || !slices.Equal(s.pkg.ConcurrentCallbackParams[prev.key], s.pkg.ConcurrentCallbackParams[op.key]) {
						fail("%s implements conflicting concurrent callback requirements for %s and %s", t, prev.key, op.key)
					}
				} else {
					s.method(op, ptr.Type, entry{op: op, wrapper: wrappers[op.key], pointer: np}, evm.DirectBinding{})
					seen[op.name] = op
				}
			}
		}
	}
	if !matches {
		fail("%s implements no exposed interface", t)
	}

}
func (s *Schema) method(op *operation, recv *values.Type, e entry, direct evm.DirectBinding) {
	var effects *evm.OwnershipEffects
	if b := op.byteBorrow; b != nil {
		effects = &evm.OwnershipEffects{NoRetain: true, NoInvalidate: true, Borrowed: []evm.BorrowSource{{Domain: b.domain, ScopeParameter: 1}}}
	}
	if b := op.callbackBorrow; b != nil {
		if effects == nil {
			effects = &evm.OwnershipEffects{}
		}
		effects.CallbackBytes = b.domain
	}
	s.pkg.Methods = append(s.pkg.Methods, evm.ExternalMethod{Ownership: effects, Direct: direct, Name: op.name, Receiver: recv, Signature: op.sig(), Variadic: op.variadic})
	s.methods = append(s.methods, e)
}
func (s *Schema) Bind(host any) evm.Package {
	if reflect.TypeOf(host) != s.host {
		panic(fmt.Sprintf("evmbind: expected host %s, got %T", s.host, host))
	}
	pkg := s.pkg
	pkg.Functions = slices.Clone(pkg.Functions)
	pkg.Methods = slices.Clone(pkg.Methods)
	for i, e := range s.funcs {
		boundHost := host
		if e.op.packageFunction {
			boundHost = e.function
		}
		pkg.Functions[i].Call = s.bind(e, boundHost)
		if e.direct != nil {
			pkg.Functions[i].Direct.Call = e.direct(boundHost)
		}
	}
	pkg.Values = slices.Clone(pkg.Values)
	for i, e := range s.values {
		pkg.Values[i].Read = s.bind(e, e.function)
	}
	for i, e := range s.methods {
		pkg.Methods[i].Call = s.bind(e, host)
		if e.direct != nil {
			pkg.Methods[i].Direct.Call = e.direct(host)
		}
	}
	return pkg
}
func (s *Schema) bind(e entry, host any) evm.Native {
	key := strings.TrimPrefix(e.op.key, "*")
	concurrentParams := s.pkg.ConcurrentCallbackParams[key]

	return func(c *evm.VM) (err error) {
		defer Catch(e.op.key, &err)
		f := Frame{VM: c, Host: host, s: s}
		for _, i := range e.op.concurrentInterfaces {
			slot := i
			if e.op.recv != nil {
				slot++
			}
			if c.CallArgument(slot) != (values.Value{}) {
				if err := c.CheckConcurrentBorrow(i); err != nil {
					return err
				}
			}
		}
		for _, i := range concurrentParams {
			slot := i
			if e.op.recv != nil {
				slot++
			}
			v := c.CallArgument(slot)
			if e.op.args[i].boxedFunction {
				shape, raw, err := c.Program().UnpackInterface(v)
				if err != nil {
					return err
				}
				if shape != nil && shape.Kind != values.FunctionKind {
					return evm.ErrFunction
				}
				v = raw
			}
			if v == (values.Value{}) {
				continue
			}
			fn, err := c.Program().Callable(v)
			if err != nil {
				return err
			}
			if err := fn.CheckConcurrentUse(); err != nil {
				return err
			}
			if fn.HasCaptures() {
				if err := c.CheckConcurrentBorrow(i); err != nil {
					return err
				}
			}
		}
		if e.pointer != nil {
			p := c.CallArgument(0).P
			if e.pointer.receiver != nil {
				f.Receiver = e.pointer.receiver(p, c)
			} else {
				f.Receiver = restore(e.pointer, p)
			}
		}
		e.wrapper(&f)
		return nil
	}
}

// Catch converts a panic from a generated conversion or Go method into the
// same source-positioned failure as the reflection binding.
func Catch(name string, err *error) {
	if x := recover(); x != nil {
		if callback := evm.PreserveCallbackPanic(x); callback != nil {
			*err = callback
			return
		}
		if cause, ok := x.(error); ok {
			*err = fmt.Errorf("%s: %w", name, cause)
		} else {
			*err = fmt.Errorf("%s: %v", name, x)
		}
	}
}
func restore(t *nativePointer, p unsafe.Pointer) any {
	return t.load(p)
}

// PackInterface creates a program-local R+P argument. P is the original Go pointer,
// including a nil pointer in a non-nil interface.
func (s *Schema) PackInterface(p *evm.Program, x any) (values.Value, error) {
	if x == nil {
		return values.Value{}, nil
	}
	n := s.pointers[reflect.TypeOf(x)]
	if n != nil {
		return p.PointerInterface(n.vmType, n.pointer(x))
	}
	t := reflect.TypeOf(x)
	if vt := s.nativeTypes[t]; vt != nil && t.Kind() == reflect.Pointer && s.fields[t.Elem()] != nil {
		return p.PointerInterface(vt, reflect.ValueOf(x).UnsafePointer())
	}
	return values.Value{}, fmt.Errorf("unregistered host pointer %T", x)
}

// UnpackInterface recovers a native pointer without a running call.
func (s *Schema) UnpackInterface(p *evm.Program, v values.Value) (any, error) {
	if v == (values.Value{}) {
		return nil, nil
	}
	if n := s.reverse[p.InterfaceType(v)]; n != nil {
		return restore(n, v.P), nil
	}
	shape, value, err := p.UnpackInterface(v)
	if err != nil {
		return nil, err
	}
	if shape != nil && shape.Kind == values.PointerKind && shape.Native != nil && shape.Native.GoType != nil {
		if shape.Type.Repr() == values.Pointer || value.R == values.NativeAddress || value.P == nil {
			return reflect.NewAt(shape.Native.GoType.Elem(), value.P).Interface(), nil
		}
	}
	return nil, fmt.Errorf("interface does not contain a native pointer")
}

// nativeInterface also supports native values, charging their copies to the call.
func (s *Schema) nativeInterface(c *evm.VM, v values.Value) (any, error) {
	if v == (values.Value{}) {
		return nil, nil
	}
	n := s.reverse[c.Program().InterfaceType(v)]
	if n == nil {
		return c.NativeAny(v)
	}
	return restore(n, v.P), nil
}

func sortedNodes(s *source) []*node {
	var ns []*node
	for _, n := range s.nodes {
		ns = append(ns, n)
	}
	sort.Slice(ns, func(i, j int) bool { return ns[i].name < ns[j].name })
	return ns
}

// NativeFields returns the shared live-field binding for an exposed Go struct.
// Generated mode and reflection use the same operations available to KVO.
func (s *Schema) NativeFields(t reflect.Type) *nativebind.Struct { return s.fields[t] }

// NativeType returns the existing value-type identity for an exposed Go type.
func (s *Schema) NativeType(t reflect.Type) *values.Type { return s.nativeTypes[t] }

// NativePointer borrows a registered data struct pointer for a guest argument.
// The VM keeps the original object alive. Guest writes and native methods act
// on that object immediately; concurrent access requires caller synchronization.
func (s *Schema) NativePointer(x any) (values.Value, error) {
	v := reflect.ValueOf(x)
	if v.IsValid() {
		if n, ok := s.nativeModels[v.Type()]; ok {
			if n == nil {
				return values.Value{}, fmt.Errorf("native type %T has multiple models", x)
			}
			return n.model.binding.Record(v).Value(), nil
		}
	}
	if !v.IsValid() || v.Kind() != reflect.Pointer || s.nativeTypes[v.Type()] == nil || s.fields[v.Type().Elem()] == nil {
		return values.Value{}, fmt.Errorf("unregistered native struct pointer %T", x)
	}
	return values.Value{R: values.NativeAddress, P: v.UnsafePointer()}, nil
}
