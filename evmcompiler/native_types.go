package evmcompiler

import (
	"fmt"
	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/values"
	"go/token"
	"go/types"
	"slices"
	"strings"
)

type importKey struct {
	pkg, name string
	receiver  *values.Type
}
type nativeBinding struct {
	key      importKey
	function *evm.ExternalFunction
}
type nativeType struct {
	pkg  string
	decl evm.TypeDecl
}
type nativeMethodKey struct {
	receiver *values.Type
	name     string
}

func (e *typeEnvironment) goSignature(sig evm.Signature, variadic bool, pkg *types.Package) (*types.Signature, error) {
	params, err := e.goTuple(sig.Params, pkg)
	if err != nil {
		return nil, err
	}
	results, err := e.goTuple(sig.Results, pkg)
	if err != nil {
		return nil, err
	}
	if variadic {
		if params.Len() == 0 {
			return nil, fmt.Errorf("variadic function has no parameters")
		}
		if _, ok := params.At(params.Len() - 1).Type().Underlying().(*types.Slice); !ok {
			return nil, fmt.Errorf("variadic parameter must be a slice")
		}
	}
	return types.NewSignatureType(nil, nil, nil, params, results, variadic), nil
}
func (e *typeEnvironment) goShape(s *values.Shape, pkg *types.Package) (types.Type, error) {
	if s == nil || s.Type == nil {
		return nil, fmt.Errorf("missing native storage shape")
	}
	if e.buildingShapes[s] {
		return nil, fmt.Errorf("recursive unnamed native shape %s", s.Type.Name())
	}
	e.buildingShapes[s] = true
	defer delete(e.buildingShapes, s)
	child := func(s *values.Shape) (types.Type, error) {
		if s == nil {
			return nil, fmt.Errorf("missing child shape")
		}
		if _, ok := e.recordOwners[s.Type]; ok {
			return e.goType(s.Type, pkg)
		}
		if _, ok := e.nativeTypes[s.Type]; ok {
			return e.goType(s.Type, pkg)
		}
		if t := e.goOf(s.Type); t != nil {
			return t, nil
		}
		for _, declaration := range e.packages {
			for _, shared := range declaration.SharedTypes {
				if shared.Type == s.Type {
					return e.goType(s.Type, pkg)
				}
			}
		}
		t, err := e.goShape(s, pkg)
		if err == nil {
			if err := e.bindNative(s.Type, t); err != nil {
				return nil, err
			}
		}
		return t, err
	}
	switch s.Kind {
	case values.TimestampKind:
		return types.NewStruct(nil, nil), nil
	case values.BoolKind:
		return types.Typ[types.Bool], nil
	case values.IntKind, values.UintKind, values.FloatKind, values.ComplexKind:
		var k types.BasicKind
		switch s.Kind {
		case values.IntKind:
			switch s.Bits {
			case 8:
				k = types.Int8
			case 16:
				k = types.Int16
			case 32:
				k = types.Int32
			case 64:
				k = types.Int64
			}
		case values.UintKind:
			switch s.Bits {
			case 8:
				k = types.Uint8
			case 16:
				k = types.Uint16
			case 32:
				k = types.Uint32
			case 64:
				k = types.Uint64
			}
		case values.FloatKind:
			if s.Bits == 32 {
				k = types.Float32
			} else if s.Bits == 64 {
				k = types.Float64
			}
		case values.ComplexKind:
			if s.Bits == 64 {
				k = types.Complex64
			} else if s.Bits == 128 {
				k = types.Complex128
			}
		}
		if k == types.Invalid {
			return nil, fmt.Errorf("invalid numeric shape")
		}
		return types.Typ[k], nil
	case values.StringKind:
		return types.Typ[types.String], nil
	case values.PointerKind, values.SliceKind, values.ArrayKind:
		elem, err := child(s.Elem)
		if err != nil {
			return nil, err
		}
		switch s.Kind {
		case values.PointerKind:
			return types.NewPointer(elem), nil
		case values.SliceKind:
			return types.NewSlice(elem), nil
		default:
			if s.Len > 1<<32 {
				return nil, fmt.Errorf("array too large")
			}
			return types.NewArray(elem, int64(s.Len)), nil
		}
	case values.MapKind:
		key, err := child(s.Key)
		if err != nil {
			return nil, err
		}
		elem, err := child(s.Elem)
		if err != nil {
			return nil, err
		}
		if !s.Key.Comparable() {
			return nil, fmt.Errorf("invalid map key")
		}
		if !types.Comparable(key) {
			e.pendingMapKeys = append(e.pendingMapKeys, key)
		}
		return types.NewMap(key, elem), nil
	case values.StructKind, values.TypedPairKind:
		fields := make([]*types.Var, len(s.Fields))
		tags := make([]string, len(s.Fields))
		names := map[string]bool{}
		for i := range s.Fields {
			f := &s.Fields[i]
			if !token.IsIdentifier(f.Name) || f.Name != "_" && names[f.Name] {
				return nil, fmt.Errorf("invalid or duplicate field %q", f.Name)
			}
			names[f.Name] = true
			t, err := child(f.Shape)
			if err != nil {
				return nil, err
			}
			fields[i] = types.NewField(token.NoPos, pkg, f.Name, t, f.Embedded)
			tags[i] = f.Tag
		}
		return types.NewStruct(fields, tags), nil
	case values.FunctionKind:
		var sig evm.Signature
		for _, p := range s.Params {
			if _, err := child(p); err != nil {
				return nil, err
			}
			sig.Params = append(sig.Params, p.Type)
		}
		for _, r := range s.Results {
			if _, err := child(r); err != nil {
				return nil, err
			}
			sig.Results = append(sig.Results, r.Type)
		}
		return e.goSignature(sig, s.Variadic, pkg)
	case values.InterfaceKind:
		methods := make([]*types.Func, len(s.Methods))
		ids := map[string]bool{}
		for i := range s.Methods {
			m := &s.Methods[i]
			if ids[m.ID] {
				return nil, fmt.Errorf("duplicate interface method")
			}
			ids[m.ID] = true
			name := m.ID
			mp := pkg
			if j := strings.LastIndexByte(name, '.'); j >= 0 {
				path := name[:j]
				name = name[j+1:]
				if path != pkg.Path() {
					return nil, fmt.Errorf("unexported method belongs to another package")
				}
			}
			if !token.IsIdentifier(name) || name == "_" {
				return nil, fmt.Errorf("invalid method name")
			}
			// Different methods can share a Go signature. The method descriptor
			// does not declare a distinct type for that signature.
			t, err := e.goShape(m.Signature, pkg)
			if err != nil {
				return nil, err
			}
			sig, ok := t.Underlying().(*types.Signature)
			if !ok {
				return nil, fmt.Errorf("invalid method signature")
			}
			if m.ID != types.Id(mp, name) {
				return nil, fmt.Errorf("method identity must be %s", types.Id(mp, name))
			}
			methods[i] = types.NewFunc(token.NoPos, mp, name, sig)
		}
		return types.NewInterfaceType(methods, nil).Complete(), nil
	}
	return nil, fmt.Errorf("invalid native shape")
}
func (e *typeEnvironment) addNativeMethods(pkg *types.Package, decl *evm.Package, early bool) error {
	// Many methods share a receiver; scan the declarations once per receiver.
	owned := make(map[*values.Type]bool)
	for i := range decl.Methods {
		m := &decl.Methods[i]
		if early && e.nativeNeedsSource(e.nativeReceiverBase(m.Receiver)) {
			continue
		}
		owns, known := owned[m.Receiver]
		if !known {
			owns = decl.OwnsMethodReceiver(m.Receiver)
			owned[m.Receiver] = owns
		}
		if !owns {
			return fmt.Errorf("method receiver is not declared by package")
		}
		recv, err := e.goType(m.Receiver, pkg)
		if err != nil {
			return err
		}
		base := recv
		if p, ok := base.(*types.Pointer); ok {
			base = p.Elem()
		}
		named, ok := base.(*types.Named)
		if !ok || named.Obj().Pkg() != pkg {
			return fmt.Errorf("method receiver must be declared in package")
		}
		if e.attachedNativeMethods[m] {
			if structure, ok := named.Underlying().(*types.Struct); ok {
				for j := 0; j < structure.NumFields(); j++ {
					if structure.Field(j).Name() == m.Name {
						return fmt.Errorf("method %s conflicts with an existing selector", m.Name)
					}
				}
			}
			continue
		}
		if m.Instantiate != nil {
			if named.TypeArgs().Len() != 0 {
				return fmt.Errorf("native method %s cannot combine a concrete generic receiver with Instantiate", m.Name)
			}
			continue
		}
		if named.TypeArgs().Len() != 0 {
			var obj *types.Func
			for j := 0; j < named.NumMethods(); j++ {
				if method := named.Method(j); method.Name() == m.Name {
					obj = method
					break
				}
			}
			if obj == nil || !e.genericNativeDecls[obj.Origin()] {
				return fmt.Errorf("native method %s requires a source declaration", m.Name)
			}
			if !types.Identical(obj.Signature().Recv().Type(), recv) {
				return fmt.Errorf("native method %s receiver mismatch", m.Name)
			}
			binding := m.Binding()
			actual, err := e.signature(obj.Signature())
			if err != nil || !slices.Equal(actual.Params, binding.Signature.Params) || !slices.Equal(actual.Results, binding.Signature.Results) || obj.Signature().Variadic() != m.Variadic {
				return fmt.Errorf("native source signature mismatch for %s", m.Name)
			}
			if e.nativeReceiverMethods == nil {
				e.nativeReceiverMethods = make(map[nativeReceiverMethod]nativeBinding)
			}
			key := nativeReceiverMethod{obj.Origin(), m.Receiver}
			if _, exists := e.nativeReceiverMethods[key]; exists {
				return fmt.Errorf("duplicate native method %s", m.Name)
			}
			e.nativeReceiverMethods[key] = nativeBinding{importKey{pkg.Path(), m.Name, m.Receiver}, &binding}
			continue
		}
		if named.TypeParams().Len() != 0 {
			return fmt.Errorf("native method %s requires a concrete receiver", m.Name)
		}
		if _, ok := named.Underlying().(*types.Pointer); ok {
			return fmt.Errorf("method receiver base cannot be a pointer type")
		}
		if _, ok := named.Underlying().(*types.Interface); ok {
			return fmt.Errorf("interface receiver cannot have native method")
		}
		if !token.IsIdentifier(m.Name) || m.Name == "_" {
			return fmt.Errorf("invalid method name")
		}
		var existing *types.Func
		for i := 0; i < named.NumMethods(); i++ {
			if obj := named.Method(i); obj.Name() == m.Name {
				if _, native := e.natives[obj]; !decl.HasSource() || !native || e.attachedNativeFunctions[obj] {
					return fmt.Errorf("duplicate method %s", m.Name)
				}
				existing = obj
			}
		}
		if existing != nil {
			binding := m.Binding()
			e.natives[existing] = nativeBinding{importKey{pkg.Path(), m.Name, m.Receiver}, &binding}
			continue
		}
		if std := e.standard[pkg.Path()]; std != nil && !std.declarationsOnly {
			return fmt.Errorf("native method %s requires a source declaration", m.Name)
		}
		if !early {
			if field, index, _ := types.LookupFieldOrMethod(recv, true, pkg, m.Name); field != nil && len(index) == 1 {
				return fmt.Errorf("method %s conflicts with an existing selector", m.Name)
			}
		}
		for _, r := range decl.Records {
			if r.Pointer == m.Receiver && r.Model.PropBySelector(m.Name) != nil {
				return fmt.Errorf("method %s conflicts with a KVO property", m.Name)
			}
		}
		sig, err := e.goSignature(m.Signature, m.Variadic, pkg)
		if err != nil {
			return err
		}
		sig = types.NewSignatureType(types.NewVar(token.NoPos, pkg, "", recv), nil, nil, sig.Params(), sig.Results(), sig.Variadic())
		obj := types.NewFunc(token.NoPos, pkg, m.Name, sig)
		named.AddMethod(obj)
		e.attachedNativeMethods[m] = true
		e.attachedNativeFunctions[obj] = true
		binding := m.Binding()
		e.natives[obj] = nativeBinding{importKey{pkg.Path(), m.Name, m.Receiver}, &binding}
	}
	return nil
}

func (e *typeEnvironment) sameNativeLayout(a, b *values.Shape) bool {
	return e.equalNativeShape(a, b, make(map[[2]*values.Shape]bool))
}
func (e *typeEnvironment) equalNativeShape(a, b *values.Shape, seen map[[2]*values.Shape]bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Type != b.Type {
		return false
	}
	// The compiler owns model layouts. Binding signatures refer to the row
	// identity and declared native allocation, never to virtual facade fields.
	if row := e.recordValues[a.Type]; row != nil && row.NativeAllocation != nil {
		return a.Kind == values.StructKind && b.Kind == values.StructKind && a.Native != nil && b.Native != nil && a.Native.GoType == row.NativeAllocation && b.Native.GoType == row.NativeAllocation
	}
	return e.equalNativeLayout(a, b, seen)
}

func (e *typeEnvironment) equalNativeLayout(a, b *values.Shape, seen map[[2]*values.Shape]bool) bool {
	if a.Type == nil || b.Type == nil {
		return false
	}
	pair := [2]*values.Shape{a, b}
	if seen[pair] {
		return true
	}
	seen[pair] = true

	numeric := a.Kind == values.IntKind || a.Kind == values.UintKind || a.Kind == values.FloatKind || a.Kind == values.ComplexKind
	if a.Type.Repr() != b.Type.Repr() || a.Kind != b.Kind || a.Cells != b.Cells || numeric && a.Bits != b.Bits || a.Len != b.Len || a.Variadic != b.Variadic || len(a.Fields) != len(b.Fields) || len(a.Params) != len(b.Params) || len(a.Results) != len(b.Results) || len(a.Methods) != len(b.Methods) {
		return false
	}
	// Typed pairs use their registered representation, not the synthetic Go
	// facade's fields. Native callbacks may refer to a model by its existing
	// type identity without recreating the compiler's private record facade.
	if a.Kind == values.TypedPairKind {
		return a.PointerLike == b.PointerLike && a.CellStruct == b.CellStruct
	}

	if !e.equalNativeShape(a.Elem, b.Elem, seen) || !e.equalNativeShape(a.Key, b.Key, seen) {
		return false
	}
	for i := range a.Fields {
		f, g := &a.Fields[i], &b.Fields[i]
		if f.Name != g.Name || f.Offset != g.Offset || f.Embedded != g.Embedded || f.Tag != g.Tag || !e.equalNativeShape(f.Shape, g.Shape, seen) {
			return false
		}
	}
	for i := range a.Params {
		if !e.equalNativeShape(a.Params[i], b.Params[i], seen) {
			return false
		}
	}
	for i := range a.Results {
		if !e.equalNativeShape(a.Results[i], b.Results[i], seen) {
			return false
		}
	}
	methods := make(map[string]*values.Shape, len(b.Methods))
	for _, m := range b.Methods {
		methods[m.ID] = m.Signature
	}
	for _, m := range a.Methods {
		// A method signature describes the call; only its arguments and
		// results have independently declared native identities.
		other := methods[m.ID]
		if other == nil || m.Signature == nil || m.Signature.Kind != values.FunctionKind || !e.equalNativeLayout(m.Signature, other, seen) {
			return false
		}
	}
	return true
}

func (e *typeEnvironment) bindNative(t *values.Type, gt types.Type) error {
	if existing := e.typeOf(gt); existing != nil && existing != t {
		// An earlier interface import may have inferred this anonymous
		// signature before a later package declares its native identity.
		// Adopt that identity in the shared shape; never replace another
		// explicitly declared native type.
		_, declared := e.nativeTypes[existing]
		if _, signature := gt.(*types.Signature); signature && !declared {
			if shape := e.shapeOf(gt); shape != nil && shape.Type == existing {
				shape.Type = t
			} else {
				return fmt.Errorf("Go type %s has multiple native type identities: %s and %s", gt, existing.Name(), t.Name())
			}
		} else {
			return fmt.Errorf("Go type %s has multiple native type identities: %s and %s", gt, existing.Name(), t.Name())
		}
	}
	e.toGo[t] = gt
	e.fromGo.Set(gt, t)
	return nil
}

// nativeShapeNeedsSource identifies native declarations whose underlying type
// refers to a source-defined instance. Their named identity is declared first;
// their fields can be resolved after the source instances have been bound.
func (e *typeEnvironment) nativeReceiverBase(t *values.Type) *values.Type {
	if d, ok := e.nativeTypes[t]; ok && d.decl.Name == "" && d.decl.Shape.Kind == values.PointerKind {
		return d.decl.Shape.Elem.Type
	}
	return t
}

// Source dependencies include the complete method set, so a partially exposed
// method set cannot change selector resolution through embedded fields.
func (e *typeEnvironment) nativeNeedsSource(t *values.Type) bool {
	if result, ok := e.nativeSourceMemo[t]; ok {
		return result
	}
	if e.base != nil {
		if result, ok := e.base.nativeSourceMemo[t]; ok {
			return result
		}
	}
	seen := map[*values.Type]bool{}
	var visit func(*values.Type) bool
	var shape func(*values.Shape) bool
	inlineSeen := map[*values.Shape]bool{}
	shape = func(s *values.Shape) bool {
		if s == nil || inlineSeen[s] {
			return false
		}
		inlineSeen[s] = true
		if _, ok := e.nativeTypes[s.Type]; ok {
			return visit(s.Type)
		}
		if e.sharedOwners[s.Type] != "" {
			return true
		}
		if _, ok := e.recordOwners[s.Type]; ok {
			return visit(s.Type)
		}
		if shape(s.Elem) || shape(s.Key) {
			return true
		}
		for _, f := range s.Fields {
			if shape(f.Shape) {
				return true
			}
		}
		for _, p := range s.Params {
			if shape(p) {
				return true
			}
		}
		for _, r := range s.Results {
			if shape(r) {
				return true
			}
		}
		for _, m := range s.Methods {
			if shape(m.Signature) {
				return true
			}
		}
		return false
	}
	visit = func(t *values.Type) bool {
		if t == nil || seen[t] {
			return false
		}
		seen[t] = true
		if e.sharedOwners[t] != "" {
			return true
		}
		d, ok := e.nativeTypes[t]
		if ok {
			if d.decl.SourceName != "" {
				return true
			}
			s := d.decl.Shape
			if shape(s.Elem) || shape(s.Key) {
				return true
			}
			for _, f := range s.Fields {
				if shape(f.Shape) {
					return true
				}
			}
			for _, p := range s.Params {
				if shape(p) {
					return true
				}
			}
			for _, r := range s.Results {
				if shape(r) {
					return true
				}
			}
			for _, m := range s.Methods {
				for _, p := range m.Signature.Params {
					if shape(p) {
						return true
					}
				}
				for _, r := range m.Signature.Results {
					if shape(r) {
						return true
					}
				}
			}
		}
		for _, m := range e.nativeMethodsByBase[e.nativeReceiverBase(t)] {
			if m.Instantiate != nil {
				return true
			}
			for _, p := range m.Signature.Params {
				if visit(p) {
					return true
				}
			}
			for _, r := range m.Signature.Results {
				if visit(r) {
					return true
				}
			}
		}
		return false
	}
	result := visit(t)
	e.nativeSourceMemo[t] = result
	return result
}
