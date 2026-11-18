package evmbind

import (
	"fmt"
	"go/ast"
	"go/types"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/values"
)

type modelDeclaration struct {
	native    reflect.Type
	row       *evm.RecordType
	mutable   *evm.RecordType
	binding   *kvo.Binding
	ambiguous bool
}

func prepareModels(files []*ast.File, selected []string, typesOnly bool, opt Options) (map[string]*modelDeclaration, []string, error) {
	result := map[string]*modelDeclaration{}
	if opt.Models == nil {
		return result, selected, nil
	}
	declared := map[string]*ast.TypeSpec{}
	for _, f := range files {
		for _, d := range f.Decls {
			if g, ok := d.(*ast.GenDecl); ok {
				for _, spec := range g.Specs {
					if t, ok := spec.(*ast.TypeSpec); ok {
						declared[t.Name.Name] = t
					}
				}
			}
		}
	}
	nativePath := opt.NativePackage
	for _, row := range opt.Models.Records {
		if row.Mutable {
			continue
		}
		entity, ok := row.Model.Type().(*kvo.EntityType)
		if !ok {
			continue
		}
		for _, t := range entity.NativeTypes() {
			declaration := declared[modelBaseName(t.Name())]
			if declaration == nil {
				continue
			}
			if nativePath == "" {
				return nil, nil, fmt.Errorf("model candidate %s requires NativePackage", t.Name())
			}
			if t.PkgPath() != nativePath {
				continue
			}
			if declaration.Assign.IsValid() || t.Kind() != reflect.Struct {
				return nil, nil, fmt.Errorf("model %s must declare a named struct type", t.Name())
			}
			if old := result[t.Name()]; old != nil {
				if old.row.Model != row.Model {
					old.ambiguous = true
				}
				continue
			}
			result[t.Name()] = &modelDeclaration{native: t, row: row, mutable: opt.Models.Record(row.Model, true), binding: kvo.Bind(t, row.Model)}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(result)) {
		if !typesOnly {
			return nil, nil, fmt.Errorf("local model %s requires a selective schema (Options.Only or Options.Selective); import its schema for callable declarations", name)
		}
		base := modelBaseName(name)
		if _, selectedFields := opt.Fields[base]; selectedFields || len(opt.OmitFields[base]) != 0 {
			return nil, nil, fmt.Errorf("native model fields are defined by model metadata: %s", name)
		}
		if len(opt.Methods[base]) != 0 {
			return nil, nil, fmt.Errorf("native model methods are not yet bound: %s", name)
		}
		declared[base].Type = &ast.StructType{Fields: &ast.FieldList{}}

	}
	selected = slices.Clone(selected)
	for i, name := range selected {
		if result[name] != nil {
			selected[i] = modelBaseName(name)
		}
	}
	return result, selected, nil
}

func modelBaseName(name string) string {
	base, _, _ := strings.Cut(name, "[")
	return base
}

func (s *source) modelNode(t types.Type, name string) *node {
	base := t
	pointer := false
	if p, ok := t.(*types.Pointer); ok {
		base = p.Elem()
		pointer = true
	}
	named, ok := base.(*types.Named)
	if !ok || named.Obj().Pkg() != s.goPackage {
		return nil
	}
	model := s.models[named.Obj().Name()]
	if model == nil {
		return nil
	}
	if model.ambiguous {
		fail("native type %s has multiple models", model.native)
	}
	n := &node{name: name, kind: nodeRecord, model: model, native: model.native, named: !pointer,
		shape: evm.ModelValueShape(model.row)}
	if model.row.NativeAllocation == model.native {
		n.nativeLayout = evm.NativeModelValueLayout(model.row.Model, model.native)
	}
	if pointer {
		n.shape = &values.Shape{Type: model.row.Pointer, Kind: values.TypedPairKind, PointerLike: true, Cells: 1}
		n.native = reflect.PointerTo(model.native)
		n.nativeLayout = modelPointerLayout(n)
		n.elem = s.node(base)
	}
	s.nodes[name] = n
	return n
}

// nativeTypePath preserves Go identity while script packages are consolidated.
func (s *source) nativeTypePath(p *types.Package) string {
	if p == nil {
		return ""
	}
	if p == s.goPackage {
		if s.nativePackage != "" {
			return s.nativePackage
		}
		return p.Path()
	}
	if imp, ok := s.nativeImports[p]; ok {
		if imp.schema.src.nativePackage != "" {
			return imp.schema.src.nativePackage
		}
		return imp.path
	}
	return p.Path()
}

func (s *source) genericModelNode(t types.Type) *node {
	base := types.Unalias(t)
	pointer := false
	if p, ok := base.(*types.Pointer); ok {
		base = types.Unalias(p.Elem())
		pointer = true
	}
	named, ok := base.(*types.Named)
	if !ok || named.TypeArgs().Len() == 0 {
		return nil
	}
	owner := s
	if named.Obj().Pkg() != s.goPackage {
		imp, ok := s.nativeImports[named.Obj().Pkg()]
		if !ok {
			return nil
		}
		owner = imp.schema.src
	}
	template := named.Obj().Name()
	found := false
	for name := range owner.models {
		if modelBaseName(name) == template {
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	args := make([]string, named.TypeArgs().Len())
	for i := range args {
		args[i] = s.modelArgumentName(named.TypeArgs().At(i))
	}
	concrete := template + "[" + strings.Join(args, ",") + "]"
	model := owner.models[concrete]
	if model == nil {
		fail("unregistered native model instantiation %s", concrete)
	}
	if model.ambiguous {
		fail("native type %s has multiple models", model.native)
	}
	name := s.nativeTypePath(named.Obj().Pkg()) + "." + concrete
	if pointer {
		name = "*" + name
	}
	if n := s.nodes[name]; n != nil {
		return n
	}
	n := &node{name: name, kind: nodeRecord, model: model, modelType: canonicalModelType(t), native: model.native, named: !pointer,
		nativeImport: model.native.PkgPath(), shape: evm.ModelValueShape(model.row)}
	if model.row.NativeAllocation == model.native {
		n.nativeLayout = evm.NativeModelValueLayout(model.row.Model, model.native)
	}
	if pointer {
		n.shape = &values.Shape{Type: model.row.Pointer, Kind: values.TypedPairKind, PointerLike: true, Cells: 1}
		n.native = reflect.PointerTo(model.native)
		n.nativeLayout = modelPointerLayout(n)
		n.elem = s.node(base)
	}
	s.nodes[name] = n
	return n
}

func (s *source) modelArgumentName(t types.Type) string {
	t = types.Unalias(t)
	switch t := t.(type) {
	case *types.Basic:
		if t.Kind() == types.UnsafePointer {
			return "unsafe.Pointer"
		}
		return types.Typ[t.Kind()].Name()
	case *types.Pointer:
		return "*" + s.modelArgumentName(t.Elem())
	case *types.Slice:
		return "[]" + s.modelArgumentName(t.Elem())
	case *types.Array:
		return fmt.Sprintf("[%d]%s", t.Len(), s.modelArgumentName(t.Elem()))
	case *types.Map:
		return "map[" + s.modelArgumentName(t.Key()) + "]" + s.modelArgumentName(t.Elem())
	case *types.Interface:
		if t.Empty() {
			return "interface {}"
		}
		fail("unsupported native model type argument %s", t)
		return ""
	case *types.Named:
		if t.Obj().Pkg() != nil && !t.Obj().Exported() {
			fail("unexported native model type argument %s", t)
		}
		name := t.Obj().Name()
		if path := s.nativeTypePath(t.Obj().Pkg()); path != "" {
			name = path + "." + name
		}
		if t.TypeArgs().Len() != 0 {
			args := make([]string, t.TypeArgs().Len())
			for i := range args {
				args[i] = s.modelArgumentName(t.TypeArgs().At(i))
			}
			name += "[" + strings.Join(args, ",") + "]"
		}
		return name
	default:
		fail("unsupported native model type argument %s", t)
		return ""
	}
}

func canonicalModelType(t types.Type) types.Type {
	t = types.Unalias(t)
	switch t := t.(type) {
	case *types.Pointer:
		return types.NewPointer(canonicalModelType(t.Elem()))
	case *types.Slice:
		return types.NewSlice(canonicalModelType(t.Elem()))
	case *types.Array:
		return types.NewArray(canonicalModelType(t.Elem()), t.Len())
	case *types.Map:
		return types.NewMap(canonicalModelType(t.Key()), canonicalModelType(t.Elem()))
	case *types.Named:
		if t.TypeArgs().Len() == 0 {
			return t
		}
		args := make([]types.Type, t.TypeArgs().Len())
		for i := range args {
			args[i] = canonicalModelType(t.TypeArgs().At(i))
		}
		result, err := types.Instantiate(nil, t.Origin(), args, false)
		if err != nil {
			fail("canonical model type: %v", err)
		}
		return result
	default:
		return t
	}
}

func sourceNativePackage(files []*ast.File, opt Options) (string, error) {
	if opt.NativePackage != "" {
		return opt.NativePackage, nil
	}
	declared := map[string]*ast.TypeSpec{}
	for _, file := range files {
		for _, decl := range file.Decls {
			if g, ok := decl.(*ast.GenDecl); ok {
				for _, spec := range g.Specs {
					if t, ok := spec.(*ast.TypeSpec); ok {
						declared[t.Name.Name] = t
					}
				}
			}
		}
	}
	nativePath := opt.NativePackage
	if nativePath == "" {
		candidates := append([]reflect.Type{}, opt.Types...)
		seen := map[reflect.Type]bool{}
		var collectFunctionTypes func(reflect.Type)
		collectFunctionTypes = func(t reflect.Type) {
			if t == nil || seen[t] {
				return
			}
			seen[t] = true
			if t.Name() != "" {
				candidates = append(candidates, t)
			}
			switch t.Kind() {
			case reflect.Func:
				for i := 0; i < t.NumIn(); i++ {
					collectFunctionTypes(t.In(i))
				}
				for i := 0; i < t.NumOut(); i++ {
					collectFunctionTypes(t.Out(i))
				}
			case reflect.Pointer, reflect.Slice, reflect.Array:
				collectFunctionTypes(t.Elem())
			case reflect.Map:
				collectFunctionTypes(t.Key())
				collectFunctionTypes(t.Elem())
			}
		}
		for _, fn := range opt.Functions {
			typ := reflect.TypeOf(fn)
			if typ == nil || typ.Kind() != reflect.Func {
				continue
			}
			for i := 0; i < typ.NumIn(); i++ {
				collectFunctionTypes(typ.In(i))
			}
			for i := 0; i < typ.NumOut(); i++ {
				collectFunctionTypes(typ.Out(i))
			}
		}

		for _, impl := range opt.Implementations {
			candidates = append(candidates, impl.typ)
		}
		if opt.Static != nil {
			for name, t := range opt.Static.Types {
				if declared[name] == nil {
					continue
				}
				candidates = append(candidates, t)
			}
		}
		for _, t := range candidates {
			for t.Kind() == reflect.Pointer {
				t = t.Elem()
			}
			declaration := declared[modelBaseName(t.Name())]
			if declaration == nil || declaration.Assign.IsValid() || t.PkgPath() == "" {
				continue
			}
			if nativePath != "" && nativePath != t.PkgPath() {
				return "", fmt.Errorf("ambiguous native package; set NativePackage")
			}
			nativePath = t.PkgPath()
		}
	}

	if nativePath == "" && opt.Host != nil {
		host := opt.Host
		for host.Kind() == reflect.Pointer {
			host = host.Elem()
		}
		nativePath = host.PkgPath()
	}
	return nativePath, nil
}

func (s *source) callNode(t types.Type) *node {
	n := s.node(t)
	if n.kind == nodeRecord && n.native.Kind() != reflect.Pointer && n.model.row.NativeAllocation != n.native {
		fail("native model value %s requires its declared NativeAllocation type; use a pointer or explicit conversion", n.name)
	}
	return n
}

func modelAllocationName(model *modelDeclaration) string {
	t := model.row.NativeAllocation
	if t == nil {
		return "<none>"
	}
	return t.PkgPath() + "." + t.Name()
}
