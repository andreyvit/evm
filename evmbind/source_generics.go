package evmbind

import (
	"fmt"
	"go/token"
	"go/types"
	"maps"
	"path"
	"reflect"
	"slices"
	"strings"

	"github.com/andreyvit/evm/values"
)

// Instance explicitly associates a source generic struct with a native instance.
// Args are required because reflection cannot recover generic type arguments.
type Instance struct {
	Type   string
	Native reflect.Type
	Args   []reflect.Type
}

type sourceInstance struct {
	typ    *types.Named
	native reflect.Type
}

func (s *source) instance(t types.Type) *sourceInstance {
	for i := range s.instances {
		if types.Identical(s.instances[i].typ, t) {
			return &s.instances[i]
		}
	}
	return nil
}

func (s *source) prepareInstances(instances []Instance) {
	s.instanceInputs = instances
	s.resolvingInstances = map[reflect.Type]bool{}
	seen := map[reflect.Type]bool{}
	for _, in := range instances {
		if seen[in.Native] {
			fail("duplicate native instance %v", in.Native)
		}
		seen[in.Native] = true
	}
	for _, in := range instances {
		s.addInstance(in)
	}
}

func (s *source) addInstance(in Instance) *types.Named {
	for _, done := range s.instances {
		if done.native == in.Native {
			return done.typ
		}
	}
	if s.resolvingInstances[in.Native] {
		fail("cyclic native instance arguments for %v", in.Native)
	}
	s.resolvingInstances[in.Native] = true
	defer delete(s.resolvingInstances, in.Native)

	obj, ok := s.goPackage.Scope().Lookup(in.Type).(*types.TypeName)
	if !ok {
		fail("generic type %s is not declared", in.Type)
	}
	origin, ok := obj.Type().(*types.Named)
	if !ok || origin.TypeParams().Len() == 0 {
		fail("%s is not a generic named type", in.Type)
	}
	if _, ok := origin.Underlying().(*types.Struct); !ok {
		fail("generic native type %s must be a struct", in.Type)
	}
	if in.Native == nil || in.Native.Kind() != reflect.Struct || in.Native.PkgPath() != s.nativePackage {
		fail("invalid native instance for %s", in.Type)
	}
	for name := range s.models {
		if modelBaseName(name) == in.Type {
			fail("generic model template %s cannot also have native Instances", in.Type)
		}
	}
	args := make([]types.Type, len(in.Args))
	names := make([]string, len(in.Args))
	for i, a := range in.Args {
		args[i] = s.instanceArgument(a)
		names[i] = s.modelArgumentName(args[i])
	}
	if in.Native.Name() != in.Type+"["+strings.Join(names, ",")+"]" {
		fail("native instance %s does not match %s[%s]", in.Native, in.Type, strings.Join(names, ","))
	}
	typ, err := types.Instantiate(nil, origin, args, true)
	if err != nil {
		fail("instantiate %s: %v", in.Type, err)
	}
	if s.instance(typ) != nil {
		fail("duplicate native instance %s", typ)
	}
	s.instances = append(s.instances, sourceInstance{typ.(*types.Named), in.Native})
	return typ.(*types.Named)
}

func (s *source) instanceArgument(t reflect.Type) types.Type {
	for _, in := range s.instanceInputs {
		if in.Native == t {
			return s.addInstance(in)
		}
	}
	for _, imp := range s.nativeImports {
		for _, in := range imp.schema.src.instances {
			if in.native == t {
				return in.typ
			}
		}
	}
	if t == reflect.TypeFor[error]() {
		return types.Universe.Lookup("error").Type()
	}
	if t == nil {
		fail("nil native type argument")
	}
	if t.PkgPath() == "time" && t.Name() == "Time" {
		standardSchemaImports.Lock()
		pkg, err := standardSchemaImports.importer.Import("time")
		standardSchemaImports.Unlock()
		if err != nil {
			fail("time type argument: %v", err)
		}
		return pkg.Scope().Lookup("Time").Type()
	}
	if t.PkgPath() != "" {
		if t.PkgPath() == s.nativePackage {
			if obj, ok := s.goPackage.Scope().Lookup(t.Name()).(*types.TypeName); ok {
				return obj.Type()
			}
		}
		for p, imp := range s.nativeImports {
			nativePath := imp.schema.src.nativePackage
			if nativePath == "" {
				nativePath = imp.path
			}
			if nativePath == t.PkgPath() {
				if obj, ok := p.Scope().Lookup(t.Name()).(*types.TypeName); ok {
					return obj.Type()
				}
			}
		}
		fail("unregistered native type argument %s", t)
	}
	switch t.Kind() {
	case reflect.Pointer:
		return types.NewPointer(s.instanceArgument(t.Elem()))
	case reflect.Slice:
		return types.NewSlice(s.instanceArgument(t.Elem()))
	case reflect.Array:
		return types.NewArray(s.instanceArgument(t.Elem()), int64(t.Len()))
	case reflect.Map:
		return types.NewMap(s.instanceArgument(t.Key()), s.instanceArgument(t.Elem()))
	case reflect.Interface:
		if t.NumMethod() == 0 {
			return types.Universe.Lookup("any").Type()
		}
	default:
		if obj, ok := types.Universe.Lookup(t.String()).(*types.TypeName); ok {
			return obj.Type()
		}
	}
	fail("unsupported native type argument %s", t)
	return nil
}

func (s *source) finishInstances() string {
	if len(s.instances) == 0 && len(s.genericFunctions) == 0 {
		return ""
	}
	imports := map[string]string{}
	qualifier := func(p *types.Package) string {
		if p == s.goPackage {
			return ""
		}
		if p.Path() == "time" {
			imports["time"] = "time"
			return "time"
		}
		imp, ok := s.nativeImports[p]
		if !ok {
			fail("unregistered generic source import %s", p.Path())
		}
		if imp.schema.pkg.Path == s.pkg.Path {
			return ""
		}
		for name, old := range imports {
			if old == imp.schema.pkg.Path {
				return name
			}
		}
		name := p.Name()
		base := name
		for i := 1; imports[name] != ""; i++ {
			name = fmt.Sprintf("%s_%d", base, i)
		}
		imports[name] = imp.schema.pkg.Path
		return name
	}
	var body strings.Builder
	seen := map[*types.Named]bool{}
	for _, in := range s.instances {
		s.node(in.typ)
		s.node(types.NewPointer(in.typ))
		for i := 0; i < in.typ.NumMethods(); i++ {
			m := in.typ.Method(i)
			if !m.Exported() {
				continue
			}
			s.ops = append(s.ops, s.op(m, s.node(m.Signature().Recv().Type())))
		}
		origin := in.typ.Origin()
		s.rejectRenamedGenericReferences(origin.Underlying())
		for i := 0; i < origin.TypeParams().Len(); i++ {
			s.rejectRenamedGenericReferences(origin.TypeParams().At(i).Constraint())
		}
		for i := 0; i < origin.NumMethods(); i++ {
			s.rejectRenamedGenericReferences(origin.Method(i).Type())
		}
		if seen[origin] {
			continue
		}
		seen[origin] = true
		params := make([]string, origin.TypeParams().Len())
		for i := range params {
			p := origin.TypeParams().At(i)
			params[i] = p.Obj().Name() + " " + types.TypeString(p.Constraint().Underlying(), qualifier)
		}
		fmt.Fprintf(&body, "type %s[%s] %s\n", origin.Obj().Name(), strings.Join(params, ","), types.TypeString(origin.Underlying(), qualifier))
		for i := 0; i < origin.NumMethods(); i++ {
			m := origin.Method(i)
			if !m.Exported() {
				continue
			}
			sig := m.Signature()
			fmt.Fprintf(&body, "func (_ %s) %s%s\n", types.TypeString(sig.Recv().Type(), qualifier), m.Name(), strings.TrimPrefix(types.TypeString(sig, qualifier), "func"))
		}
	}
	for _, fn := range s.genericFunctions {
		s.rejectRenamedGenericReferences(fn.signature)
		params := make([]string, fn.signature.TypeParams().Len())
		for i := range params {
			p := fn.signature.TypeParams().At(i)
			constraint := types.TypeString(p.Constraint().Underlying(), qualifier)
			if constraint == "interface{}" {
				constraint = "any"
			}
			params[i] = p.Obj().Name() + " " + constraint
		}
		signature := types.NewSignatureType(nil, nil, nil, fn.signature.Params(), fn.signature.Results(), fn.signature.Variadic())
		fmt.Fprintf(&body, "func %s[%s]%s\n", fn.name, strings.Join(params, ","), strings.TrimPrefix(types.TypeString(signature, qualifier), "func"))
	}
	var out strings.Builder
	name := path.Base(s.pkg.Path)
	if !token.IsIdentifier(name) {
		name = s.name
	}
	fmt.Fprintf(&out, "package %s\n", name)
	for _, name := range slices.Sorted(maps.Keys(imports)) {
		fmt.Fprintf(&out, "import %s %q\n", name, imports[name])
	}
	out.WriteString(body.String())
	s.pkg.Source = out.String()
	return s.pkg.Source
}

func (s *source) instanceTypeArgs(t *types.Named) []*values.Type {
	args := make([]*values.Type, t.TypeArgs().Len())
	for i := range args {
		args[i] = s.node(t.TypeArgs().At(i)).shape.Type
	}
	return args
}

func (s *source) hasGenericInstances() bool {
	if len(s.genericFunctions) != 0 {
		return true
	}
	for _, n := range s.nodes {
		if n.genericInstance && n.imported == nil {
			return true
		}
	}
	return false
}

// Generic declarations are emitted as Go source using native type spellings.
// Reject renamed references until that source emitter supports guest spellings.
func (s *source) rejectRenamedGenericReferences(root types.Type) {
	seen := map[types.Type]bool{}
	var visit func(types.Type)
	visit = func(t types.Type) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		switch t := t.(type) {
		case *types.Named:
			owner := s
			if t.Obj().Pkg() != s.goPackage {
				if imp, ok := s.nativeImports[t.Obj().Pkg()]; ok {
					owner = imp.schema.src
				} else {
					return
				}
			}
			name := t.Obj().Name()
			if owner.guestTypeName(name) != name {
				fail("generic source references renamed native type %s", t)
			}
			for i := 0; i < t.TypeArgs().Len(); i++ {
				visit(t.TypeArgs().At(i))
			}
		case *types.Alias:
			// Aliases keep their own guest spelling, but generic arguments may not.
			for i := 0; i < t.TypeArgs().Len(); i++ {
				visit(t.TypeArgs().At(i))
			}
		case *types.Pointer:
			visit(t.Elem())
		case *types.Slice:
			visit(t.Elem())
		case *types.Array:
			visit(t.Elem())
		case *types.Map:
			visit(t.Key())
			visit(t.Elem())
		case *types.Chan:
			visit(t.Elem())
		case *types.Struct:
			for i := 0; i < t.NumFields(); i++ {
				visit(t.Field(i).Type())
			}
		case *types.Signature:
			visit(t.Params())
			visit(t.Results())
			for i := 0; i < t.TypeParams().Len(); i++ {
				visit(t.TypeParams().At(i).Constraint())
			}
		case *types.Tuple:
			for i := 0; i < t.Len(); i++ {
				visit(t.At(i).Type())
			}
		case *types.Interface:
			for i := 0; i < t.NumExplicitMethods(); i++ {
				visit(t.ExplicitMethod(i).Type())
			}
			for i := 0; i < t.NumEmbeddeds(); i++ {
				visit(t.EmbeddedType(i))
			}
		case *types.TypeParam:
			visit(t.Constraint())
		case *types.Union:
			for i := 0; i < t.Len(); i++ {
				visit(t.Term(i).Type())
			}
		}
	}
	visit(root)
}
