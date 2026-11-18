package evmcompiler

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/andreyvit/evm"
	"golang.org/x/tools/go/ast/astutil"
)

// DeclarationOptions controls annotations in printed package declarations.
type DeclarationOptions struct {
	// Annotations selects comment lines for a function from its raw declaration comments.
	Annotations func(path, name, doc string) []string
}

// PackageSource prints the importable package as Go declarations, using the
// same types as Compile. KVO property names retain their schema spelling.
func PackageSource(path string, packages ...evm.Package) (string, error) {
	return (DeclarationOptions{}).PackageSource(path, packages...)
}

func (options DeclarationOptions) PackageSource(path string, packages ...evm.Package) (string, error) {
	env, err := newTypeEnvironment(packages)
	if err != nil {
		return "", err
	}
	if _, standard := evm.StandardLibrarySource(path); env.packages[path] == nil && !standard {
		return "", fmt.Errorf("host package %q is not exposed", path)
	}
	env.recordFacade = true
	env.guest = types.NewPackage("plugin", "plugin")
	env.files = token.NewFileSet()
	pkg, err := env.Import(path)
	if err != nil {
		return "", err
	}
	imports := map[string]string{}
	used := map[string]bool{}
	for _, n := range pkg.Scope().Names() {
		used[n] = true
	}
	qual := func(p *types.Package) string {
		if p == nil || p == pkg || p == env.guest {
			return ""
		}
		if name := imports[p.Path()]; name != "" {
			return name
		}
		name := p.Name()
		for i := 2; used[name]; i++ {
			name = fmt.Sprintf("%s%d", p.Name(), i)
		}
		used[name] = true
		imports[p.Path()] = name
		return name
	}
	records := map[string]*evm.RecordType{}
	if decl := env.packages[path]; decl != nil {
		for _, r := range decl.Records {
			records[r.Name] = r
		}
	}
	docs := make(map[string]string)
	if decl := env.packages[path]; decl != nil {
		for _, source := range decl.Sources() {
			file, err := parser.ParseFile(token.NewFileSet(), source.Name, source.Text, parser.ParseComments)
			if err != nil {
				return "", err
			}
			for _, d := range file.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Doc != nil {
					for _, line := range fn.Doc.List {
						docs[fn.Name.Name] += line.Text + "\n"
					}
				}
			}
		}
		for _, fn := range decl.Functions {
			if fn.Doc != "" {
				docs[fn.Name] = fn.Doc
			}
		}
	}
	var body strings.Builder
	for _, name := range pkg.Scope().Names() {
		obj := pkg.Scope().Lookup(name)
		if !obj.Exported() || strings.Contains(name, "$") {
			continue
		}
		switch o := obj.(type) {
		case *types.TypeName:
			if alias, ok := o.Type().(*types.Alias); ok {
				if alias.TypeParams().Len() != 0 {
					return "", fmt.Errorf("cannot describe generic alias %s", name)
				}
				fmt.Fprintf(&body, "type %s = %s\n", name, types.TypeString(alias.Rhs(), qual))
				continue
			}
			t, ok := o.Type().(*types.Named)
			if !ok {
				return "", fmt.Errorf("cannot describe type %s", name)
			}
			declarationName := name
			if params := t.TypeParams(); params.Len() != 0 {
				var names []string
				for i := 0; i < params.Len(); i++ {
					param := params.At(i)
					names = append(names, param.Obj().Name()+" "+types.TypeString(param.Constraint(), qual))
				}
				declarationName += "[" + strings.Join(names, ", ") + "]"
			}
			if r := records[name]; r != nil {
				access := "read-only"
				if r.Mutable {
					access = "mutable"
				}
				fmt.Fprintf(&body, "// %s is a %s view of KVO model %s.\ntype %s struct {\n", name, access, r.Model.Name(), name)
				st := t.Underlying().(*types.Struct)
				for i := 0; i < st.NumFields(); i++ {
					f := st.Field(i)
					if _, ok := env.recordFields[f]; ok {
						fmt.Fprintf(&body, "%s %s\n", f.Name(), types.TypeString(f.Type(), qual))
					}
				}
				for _, p := range r.Model.AllProps() {
					if field, _, _ := types.LookupFieldOrMethod(t, true, env.guest, p.Name()); field == nil {
						fmt.Fprintf(&body, "// Unavailable property %s (%s).\n", strconv.Quote(p.Name()), strconv.Quote(p.AnyType().Name()))
					}
				}
				body.WriteString("}\n")
				for i := range env.packages[path].Functions {
					f := &env.packages[path].Functions[i]
					if f.On != r {
						continue
					}
					sig := pkg.Scope().Lookup(operationIdent(path, f)).Type()
					fmt.Fprintf(&body, "\n// Type operation: %s.%s.%s(...)\nfunc (%s) %s%s\n", pkg.Name(), name, f.Name, name, f.Name, strings.TrimPrefix(types.TypeString(sig, qual), "func"))
				}
			} else {
				switch u := t.Underlying().(type) {
				case *types.Interface:
					fmt.Fprintf(&body, "type %s interface {\n", declarationName)
					for i := 0; i < u.NumMethods(); i++ {
						m := u.Method(i)
						fmt.Fprintf(&body, "%s%s\n", m.Name(), strings.TrimPrefix(types.TypeString(m.Type(), qual), "func"))
					}
					body.WriteString("}\n")
				case *types.Struct:
					fmt.Fprintf(&body, "type %s struct {\n", declarationName)
					for i := 0; i < u.NumFields(); i++ {
						f := u.Field(i)
						if f.Exported() {
							fmt.Fprintf(&body, "%s %s\n", f.Name(), types.TypeString(f.Type(), qual))
						}
					}
					body.WriteString("}\n")
				default:
					fmt.Fprintf(&body, "type %s %s\n", declarationName, types.TypeString(t.Underlying(), qual))
				}
			}
			for i := 0; i < t.NumMethods(); i++ {
				m := t.Method(i)
				if m.Exported() {
					sig := m.Type().(*types.Signature)
					fmt.Fprintf(&body, "func (%s) %s%s\n", types.TypeString(sig.Recv().Type(), qual), m.Name(), strings.TrimPrefix(types.TypeString(sig, qual), "func"))
				}
			}
		case *types.Var:
			fmt.Fprintf(&body, "// Host-owned binding.\nvar %s %s\n", name, types.TypeString(o.Type(), qual))
		case *types.Const:
			typ := ""
			if b, ok := o.Type().(*types.Basic); !ok || b.Info()&types.IsUntyped == 0 {
				typ = " " + types.TypeString(o.Type(), qual)
			}
			literal := o.Val().ExactString()
			if o.Val().Kind() == constant.Float {
				// An exact rational must use floating division in a Go declaration.
				num, den, frac := strings.Cut(literal, "/")
				if frac {
					literal = num + ".0 / " + den + ".0"
				} else if !strings.ContainsAny(literal, ".pPeE") {
					literal += ".0"
				}
			}
			fmt.Fprintf(&body, "const %s%s = %s\n", name, typ, literal)
		case *types.Func:
			if options.Annotations != nil {
				for _, line := range options.Annotations(path, name, docs[name]) {
					fmt.Fprintln(&body, line)
				}
			}
			fmt.Fprintln(&body, types.ObjectString(o, qual))
		}
		body.WriteByte('\n')
	}
	var src strings.Builder
	fmt.Fprintf(&src, "package %s // %q\n\n", pkg.Name(), path)
	for _, p := range slices.Sorted(maps.Keys(imports)) {
		fmt.Fprintf(&src, "import %s %q\n", imports[p], p)
	}
	src.WriteString(body.String())
	files := token.NewFileSet()
	file, err := parser.ParseFile(files, "api.go", src.String(), parser.ParseComments)
	if err != nil {
		return "", fmt.Errorf("parse %s declarations: %w", path, err)
	}
	// Rewrite syntax nodes, preserving literal strings and struct tags.
	astutil.Apply(file, func(c *astutil.Cursor) bool {
		if t, ok := c.Node().(*ast.InterfaceType); ok && t.Methods.NumFields() == 0 {
			c.Replace(&ast.Ident{NamePos: t.Pos(), Name: "any"})
			return false
		}
		return true
	}, nil)
	var formatted strings.Builder
	err = format.Node(&formatted, files, file)
	if err != nil {
		return "", fmt.Errorf("render %s declarations: %w", path, err)
	}
	return formatted.String(), nil
}

// PackagePaths lists every import, including virtual standard packages.
func PackagePaths(packages ...evm.Package) []string {
	paths := evm.StandardLibraryPaths()
	for _, p := range packages {
		paths = append(paths, p.Path)
	}
	slices.Sort(paths)
	return slices.Compact(paths)
}

// PackagesSource prints all packages with their full import paths.
func PackagesSource(packages ...evm.Package) (string, error) {
	var out strings.Builder
	for _, path := range PackagePaths(packages...) {
		source, err := PackageSource(path, packages...)
		if err != nil {
			return "", err
		}
		if out.Len() > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(source)
	}
	return out.String(), nil
}
