// Command genstd generates the declarations and bindings of the standard
// packages that evmstd binds natively, and the API report listing what each
// offered standard package exposes and what it leaves out, with the reason.
//
//	go run ./internal/genstd
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/constant"
	"go/format"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

// The offered standard packages, by how they reach scripts.
var (
	// native packages are bound to the real Go functions through evmbind.
	native = []string{
		"unicode", "unicode/utf8", "unicode/utf16", "bytes", "encoding/base64", "encoding/hex",
		"encoding/json", "html", "math/bits", "net/url", "path", "path/filepath", "regexp", "time",
	}
	// guest packages are script Go source in evmstd/_src, compiled with the script.
	guest = []string{"cmp", "iter", "maps", "slices", "sort"}
	// builtin packages are implemented inside the VM (_stdlib).
	builtin = []string{"errors", "fmt", "log", "math", "strconv", "strings"}
)

// denied lists declarations left out by decision. Keys are "path.Name" or
// "path.Type.Method"; a type entry also removes its methods.
var denied = map[string]string{
	"time.Sleep":        "blocks the VM outside its time budget",
	"time.After":        "timers and channels",
	"time.AfterFunc":    "timers and goroutines",
	"time.NewTimer":     "timers and channels",
	"time.NewTicker":    "timers and channels",
	"time.Tick":         "timers and channels",
	"time.Timer":        "timers and channels",
	"time.Ticker":       "timers and channels",
	"time.Local":        "reads the host's time zone setting",
	"time.LoadLocation": "reads the system time zone database",

	"path/filepath.Abs":          "reads the working directory",
	"path/filepath.EvalSymlinks": "reads the file system",
	"path/filepath.Glob":         "reads the file system",
	"path/filepath.Walk":         "reads the file system",
	"path/filepath.WalkDir":      "reads the file system",
	"path/filepath.WalkFunc":     "file system callback type",

	"encoding/json.Marshal":               "works by reflection on Go values and cannot see script values",
	"encoding/json.MarshalIndent":         "works by reflection on Go values and cannot see script values",
	"encoding/json.Unmarshal":             "works by reflection on Go values and cannot fill script values",
	"encoding/json.Decoder":               "streams over I/O and decodes by reflection",
	"encoding/json.Encoder":               "streams over I/O and encodes by reflection",
	"encoding/json.Delim":                 "only used with Decoder",
	"encoding/json.InvalidUTF8Error":      "only returned by Marshal",
	"encoding/json.InvalidUnmarshalError": "only returned by Unmarshal",
	"encoding/json.MarshalerError":        "only returned by Marshal",
	"encoding/json.UnmarshalFieldError":   "only returned by Unmarshal",
	"encoding/json.UnmarshalTypeError":    "only returned by Unmarshal",
	"encoding/json.UnsupportedTypeError":  "only returned by Marshal",
	"encoding/json.UnsupportedValueError": "only returned by Marshal",

	"bytes.Buffer.Grow":            "allocates host memory outside the VM budget",
	"bytes.Buffer.Available":       "exposes spare host capacity",
	"bytes.Buffer.AvailableBuffer": "exposes spare host capacity",
}

// reasons explains gaps in the built-in and script-source packages. A key is
// "path.Name", "path.Type" (covering its methods) or "path.Prefix*".
var reasons = map[string]string{
	"errors.As":             "needs reflection on the target pointer; use AsType",
	"errors.ErrUnsupported": "built-in packages have no package variables",

	"fmt.Fprint*":      "I/O: writes to an io.Writer",
	"fmt.Fscan*":       "I/O: reads from an io.Reader",
	"fmt.Scan*":        "I/O: reads standard input",
	"fmt.Sscan*":       "scanning is not implemented",
	"fmt.FormatString": "needs the State of a native formatter",

	"log.*": "the VM's log only prints; it has no flags, prefixes, writers or process exit",

	"strconv.ErrRange":      "built-in packages have no package variables; conversion errors are plain errors",
	"strconv.ErrSyntax":     "built-in packages have no package variables; conversion errors are plain errors",
	"strconv.NumError":      "conversion errors are plain errors",
	"strconv.FormatComplex": "complex numbers are not supported",
	"strconv.ParseComplex":  "complex numbers are not supported",
	"strconv.QuotedPrefix":  "not implemented",
	"strconv.UnquoteChar":   "not implemented",

	"strings.Reader":               "reader types are not implemented",
	"strings.NewReader":            "reader types are not implemented",
	"strings.Title":                "deprecated in Go",
	"strings.ToLowerSpecial":       "needs unicode.SpecialCase, which the built-in package cannot name",
	"strings.ToTitleSpecial":       "needs unicode.SpecialCase, which the built-in package cannot name",
	"strings.ToUpperSpecial":       "needs unicode.SpecialCase, which the built-in package cannot name",
	"strings.Replacer.WriteString": "I/O: writes to an io.Writer",

	"iter.Pull":  "needs coroutines",
	"iter.Pull2": "needs coroutines",

	"sort.Slice":         "needs reflection on the slice; use slices.SortFunc",
	"sort.SliceStable":   "needs reflection on the slice; use slices.SortStableFunc",
	"sort.SliceIsSorted": "needs reflection on the slice; use slices.IsSortedFunc",
}

func reasonFor(pkg, name, fallback string) string {
	if r := reasons[pkg+"."+name]; r != "" {
		return r
	}
	if typ, _, ok := strings.Cut(name, "."); ok {
		if r := reasons[pkg+"."+typ]; r != "" {
			return r
		}
	}
	for key, r := range reasons {
		if prefix, ok := strings.CutSuffix(key, "*"); ok && strings.HasPrefix(pkg+"."+name, prefix) {
			return r
		}
	}
	return fallback
}

// guarded functions are bound to a wrapper in evmstd instead of the standard
// function, to bound work the VM cannot meter.
var guarded = map[string]string{
	"bytes.Repeat": "repeatBytes",
}

// builtinTime lists what the VM's own time package already declares. evmstd
// leaves these out of its fragment; they are exposed all the same.
var builtinTime = map[string]bool{
	"Time.Format": true, "Time.IsZero": true, "Time.Before": true, "Time.After": true, "Time.Equal": true,
	"Time.Unix": true, "Time.UnixMilli": true, "Time.UnixMicro": true, "Time.UnixNano": true,
	"RFC3339": true, "RFC3339Nano": true,
}

type decl struct {
	pkg, name string // "Func", "Type", "Type.Method", "Const", "Var"
	kind      string // func, method, type, const, var
	text      string // Go declaration as printed in the report
	reason    string // empty when exposed
}

type generator struct {
	fset     *token.FileSet
	imp      types.Importer
	pkgs     map[string]*types.Package
	nativeOK map[string]bool // package path -> natively bound
	exposed  map[*types.TypeName]bool
	all      []decl
}

func main() {
	g := &generator{fset: token.NewFileSet(), pkgs: map[string]*types.Package{}, nativeOK: map[string]bool{}, exposed: map[*types.TypeName]bool{}}
	g.imp = importer.ForCompiler(g.fset, "source", nil)
	for _, p := range native {
		g.nativeOK[p] = true
	}
	for _, list := range [][]string{native, guest, builtin} {
		for _, p := range list {
			pkg, err := g.imp.Import(p)
			check(err)
			g.pkgs[p] = pkg
		}
	}
	// Type exposure first: signatures refer to types of later packages.
	for _, p := range native {
		g.exposeTypes(g.pkgs[p])
	}
	var bindings bytes.Buffer
	bindings.WriteString("// Code generated by go run ./internal/genstd; DO NOT EDIT.\n\npackage evmstd\n\nimport (\n\t\"reflect\"\n")
	for _, p := range native {
		fmt.Fprintf(&bindings, "\t%q\n", p)
	}
	bindings.WriteString("\n\t\"github.com/andreyvit/evm/evmbind\"\n)\n\n")
	bindings.WriteString("// nativePackages lists the natively bound standard packages, dependencies first.\nvar nativePackages = []nativePackage{\n")
	for _, p := range native {
		g.native(g.pkgs[p], &bindings)
	}
	bindings.WriteString("}\n\nvar _ = evmbind.Options{}\nvar _ = reflect.TypeOf\n")
	formatted, err := format.Source(bindings.Bytes())
	if err != nil {
		check(os.WriteFile("evmstd/bindings_gen.go", bindings.Bytes(), 0o644))
		check(err)
	}
	check(os.WriteFile("evmstd/bindings_gen.go", formatted, 0o644))
	for _, p := range guest {
		g.declared(g.pkgs[p], "evmstd/_src/"+flat(p)+".go", "not written in the script source of this package")
	}
	for _, p := range builtin {
		g.declared(g.pkgs[p], "_stdlib/"+p+".go", "not implemented by the VM's built-in package")
	}
	g.report()
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func flat(p string) string { return strings.ReplaceAll(p, "/", "_") }

// exposeTypes marks the named types of a native package that scripts can use.
func (g *generator) exposeTypes(pkg *types.Package) {
	scope := pkg.Scope()
	// Two passes: a type's kind decides exposure, independent of field types.
	for _, name := range scope.Names() {
		obj, ok := scope.Lookup(name).(*types.TypeName)
		if !ok || !obj.Exported() || g.typeDeclReason(obj) != "" {
			continue
		}
		g.exposed[obj] = true
	}
}

func (g *generator) typeDeclReason(obj *types.TypeName) string {
	if r := denied[obj.Pkg().Path()+"."+obj.Name()]; r != "" {
		return r
	}
	if obj.IsAlias() {
		return "type aliases are not bound"
	}
	named, ok := obj.Type().(*types.Named)
	if !ok {
		return "unsupported type declaration"
	}
	if named.TypeParams().Len() != 0 {
		return "generic types are not bound natively"
	}
	switch u := named.Underlying().(type) {
	case *types.Basic:
		if u.Info()&types.IsComplex != 0 || u.Kind() == types.Uintptr || u.Kind() == types.UnsafePointer {
			return "unsupported underlying type " + u.Name()
		}
	case *types.Struct:
	case *types.Map, *types.Slice:
		// Checked with their element types where they are used.
	case *types.Interface:
		return "interface types are not bound natively"
	case *types.Signature:
		return "named function types are not bound natively"
	default:
		return "unsupported underlying type " + types.TypeString(u, nil)
	}
	return ""
}

// typeReason reports why a type cannot cross between a script and a native
// function, or "".
func (g *generator) typeReason(t types.Type, callback bool) string {
	switch t := types.Unalias(t).(type) {
	case *types.Basic:
		if t.Info()&types.IsComplex != 0 {
			return "complex numbers are not supported"
		}
		if t.Kind() == types.Uintptr || t.Kind() == types.UnsafePointer {
			return "unsafe pointers are not supported"
		}
		return ""
	case *types.Pointer:
		if named, ok := types.Unalias(t.Elem()).(*types.Named); ok {
			return g.typeReason(named, callback)
		}
		return "pointers to unnamed types are not bound"
	case *types.Slice:
		return g.typeReason(t.Elem(), callback)
	case *types.Array:
		return "fixed-size arrays are not bound"
	case *types.Map:
		if r := g.typeReason(t.Key(), callback); r != "" {
			return r
		}
		return g.typeReason(t.Elem(), callback)
	case *types.Chan:
		return "channels are not supported"
	case *types.Signature:
		if callback {
			return "nested callbacks are not bound"
		}
		if t.Variadic() {
			return "variadic callbacks are not bound"
		}
		return g.signatureReason(t, true)
	case *types.Interface:
		if t.Empty() {
			return ""
		}
		return "interface values are not bound natively"
	case *types.TypeParam:
		return "generic functions are not bound natively"
	case *types.Named:
		obj := t.Obj()
		if obj.Pkg() == nil {
			if obj.Name() == "error" {
				return ""
			}
			return "unsupported builtin " + obj.Name()
		}
		if t.TypeArgs().Len() != 0 {
			return "generic types are not bound natively (" + obj.Pkg().Name() + "." + obj.Name() + ")"
		}
		if g.exposed[obj] {
			return ""
		}
		switch obj.Pkg().Path() {
		case "io", "io/fs", "os", "bufio", "net", "syscall":
			return "I/O: uses " + obj.Pkg().Path() + "." + obj.Name()
		}
		if g.nativeOK[obj.Pkg().Path()] {
			return "uses " + obj.Pkg().Name() + "." + obj.Name() + ", which is not exposed"
		}
		return "uses " + obj.Pkg().Path() + "." + obj.Name() + ", from a package that is not bound natively"
	}
	return "unsupported type " + types.TypeString(t, nil)
}

func (g *generator) signatureReason(sig *types.Signature, callback bool) string {
	if sig.TypeParams().Len() != 0 {
		return "generic functions are not bound natively"
	}
	for _, tuple := range []*types.Tuple{sig.Params(), sig.Results()} {
		for i := 0; i < tuple.Len(); i++ {
			if r := g.typeReason(tuple.At(i).Type(), callback); r != "" {
				return r
			}
		}
	}
	return ""
}

// native emits the declaration source and the bindings of one package.
func (g *generator) native(pkg *types.Package, bindings *bytes.Buffer) {
	imports := map[string]bool{}
	qualifier := func(p *types.Package) string {
		if p == pkg {
			return ""
		}
		imports[p.Path()] = true
		return p.Name()
	}
	add := func(d decl) bool {
		if r := denied[d.pkg+"."+d.name]; r != "" && d.reason == "" {
			d.reason = r
		}
		g.all = append(g.all, d)
		return d.reason == ""
	}
	var body bytes.Buffer
	var functions, values, typeRoots, omitFields []string
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		d := decl{pkg: pkg.Path(), name: name}
		switch o := obj.(type) {
		case *types.TypeName:
			d.kind, d.text, d.reason = "type", "type "+name, g.typeDeclReason(o)
			if !add(d) {
				continue
			}
			named := o.Type().(*types.Named)
			if pkg.Path() != "time" || name != "Time" {
				typeRoots = append(typeRoots, fmt.Sprintf("reflect.TypeFor[%s.%s]()", pkg.Name(), name))
			}
			if st, ok := named.Underlying().(*types.Struct); ok {
				fmt.Fprintf(&body, "type %s struct {\n", name)
				var omitted []string
				for i := 0; i < st.NumFields(); i++ {
					f := st.Field(i)
					switch {
					case !f.Exported():
						fmt.Fprintf(&body, "\t%s struct{}\n", f.Name())
					case f.Embedded() || g.typeReason(f.Type(), false) != "":
						reason := "embedded fields are not bound"
						if !f.Embedded() {
							reason = g.typeReason(f.Type(), false)
						}
						g.all = append(g.all, decl{pkg: pkg.Path(), name: name + "." + f.Name(), kind: "field", text: f.Name() + " " + types.TypeString(f.Type(), qualifierNoImport(pkg)), reason: reason})
						fmt.Fprintf(&body, "\t%s struct{}\n", f.Name())
						omitted = append(omitted, strconv.Quote(f.Name()))
					default:
						g.all = append(g.all, decl{pkg: pkg.Path(), name: name + "." + f.Name(), kind: "field", text: f.Name() + " " + types.TypeString(f.Type(), qualifierNoImport(pkg))})
						fmt.Fprintf(&body, "\t%s %s\n", f.Name(), types.TypeString(anonymous(f.Type()), qualifier))
					}
				}
				body.WriteString("}\n")
				if len(omitted) > 0 {
					omitFields = append(omitFields, fmt.Sprintf("%q: {%s}", name, strings.Join(omitted, ", ")))
				}
			} else {
				fmt.Fprintf(&body, "type %s %s\n", name, types.TypeString(named.Underlying(), qualifier))
			}
			for _, recv := range []types.Type{named, types.NewPointer(named)} {
				methods := types.NewMethodSet(recv)
				for i := 0; i < methods.Len(); i++ {
					sel := methods.At(i)
					m := sel.Obj().(*types.Func)
					sig := m.Signature()
					_, pointer := sig.Recv().Type().(*types.Pointer)
					if !m.Exported() || len(sel.Index()) != 1 || pointer != (recv != types.Type(named)) {
						continue
					}
					md := decl{pkg: pkg.Path(), name: name + "." + m.Name(), kind: "method"}
					receiver := name
					if pointer {
						receiver = "*" + name
					}
					md.text = fmt.Sprintf("func (%s) %s%s", receiver, m.Name(), signatureText(sig, qualifierNoImport(pkg)))
					md.reason = g.signatureReason(sig, false)
					if pkg.Path() == "time" && name == "Time" && pointer && md.reason == "" {
						md.reason = "pointer methods of the built-in Time value are not bound"
					}
					if !add(md) || pkg.Path() == "time" && builtinTime[md.name] {
						continue
					}
					fmt.Fprintf(&body, "func (%s) %s%s\n", receiver, m.Name(), signatureText(sig, qualifier))
				}
			}
		case *types.Func:
			sig := o.Signature()
			d.kind, d.text, d.reason = "func", "func "+name+signatureText(sig, qualifierNoImport(pkg)), g.signatureReason(sig, false)
			if !add(d) {
				continue
			}
			fmt.Fprintf(&body, "func %s%s\n", name, signatureText(sig, qualifier))
			target := pkg.Name() + "." + name
			if wrapper := guarded[pkg.Path()+"."+name]; wrapper != "" {
				target = wrapper
			}
			functions = append(functions, fmt.Sprintf("%q: %s", name, target))
		case *types.Var:
			d.kind, d.text, d.reason = "var", "var "+name+" "+types.TypeString(o.Type(), qualifierNoImport(pkg)), g.typeReason(o.Type(), false)
			if !add(d) {
				continue
			}
			fmt.Fprintf(&body, "var %s %s\n", name, types.TypeString(anonymous(o.Type()), qualifier))
			values = append(values, fmt.Sprintf("%q: %s.%s", name, pkg.Name(), name))
		case *types.Const:
			d.kind, d.text = "const", "const "+name
			typ := ""
			if basic, ok := o.Type().(*types.Basic); !ok || basic.Info()&types.IsUntyped == 0 {
				d.reason = g.typeReason(o.Type(), false)
				typ = " " + types.TypeString(o.Type(), qualifier)
				d.text += " " + types.TypeString(o.Type(), qualifierNoImport(pkg))
			}
			if !add(d) || pkg.Path() == "time" && builtinTime[name] {
				continue
			}
			fmt.Fprintf(&body, "const %s%s = %s\n", name, typ, constantLiteral(o))
		}
	}
	var source bytes.Buffer
	fmt.Fprintf(&source, "// Code generated by go run ./internal/genstd; DO NOT EDIT.\n\npackage %s\n\n", pkg.Name())
	var deps []string
	for p := range imports {
		deps = append(deps, p)
	}
	sort.Strings(deps)
	for _, p := range deps {
		fmt.Fprintf(&source, "import %q\n", p)
	}
	source.Write(body.Bytes())
	formatted, err := format.Source(source.Bytes())
	if err != nil {
		formatted = source.Bytes()
		fmt.Fprintln(os.Stderr, "format", pkg.Path(), err)
	}
	dir := "evmstd/_decl/" + flat(pkg.Path())
	check(os.MkdirAll(dir, 0o755))
	check(os.WriteFile(dir+"/decl.go", formatted, 0o644))

	fmt.Fprintf(bindings, "\t{\n\t\tPath: %q,\n\t\tDecl: %q,\n", pkg.Path(), flat(pkg.Path()))
	var quoted []string
	for _, p := range deps {
		if p != "time" || pkg.Path() == "time" {
			quoted = append(quoted, strconv.Quote(p))
		}
	}
	fmt.Fprintf(bindings, "\t\tImports: []string{%s},\n", strings.Join(quoted, ", "))
	fmt.Fprintf(bindings, "\t\tOptions: evmbind.Options{\n\t\t\tNativePackage: %q,\n", pkg.Path())
	list := func(head string, items []string) {
		fmt.Fprintf(bindings, "\t\t\t%s{\n", head)
		for _, item := range items {
			fmt.Fprintf(bindings, "\t\t\t\t%s,\n", item)
		}
		bindings.WriteString("\t\t\t},\n")
	}
	list("Functions: map[string]any", functions)
	list("Values: map[string]any", values)
	list("Types: []reflect.Type", typeRoots)
	list("OmitFields: map[string][]string", omitFields)
	bindings.WriteString("\t\t},\n\t},\n")
}

// anonymous returns t with parameter names removed from every function type
// nested in it. Two spellings of one callback type would otherwise bind as
// two types.
func anonymous(t types.Type) types.Type {
	switch t := t.(type) {
	case *types.Signature:
		strip := func(tuple *types.Tuple) *types.Tuple {
			vars := make([]*types.Var, tuple.Len())
			for i := range vars {
				vars[i] = types.NewVar(token.NoPos, nil, "", anonymous(tuple.At(i).Type()))
			}
			return types.NewTuple(vars...)
		}
		return types.NewSignatureType(nil, nil, nil, strip(t.Params()), strip(t.Results()), t.Variadic())
	case *types.Pointer:
		return types.NewPointer(anonymous(t.Elem()))
	case *types.Slice:
		return types.NewSlice(anonymous(t.Elem()))
	case *types.Map:
		return types.NewMap(anonymous(t.Key()), anonymous(t.Elem()))
	}
	return t
}

// signatureText prints a function's parameters and results, keeping the
// names of its own parameters and dropping those inside callback types.
func signatureText(sig *types.Signature, q types.Qualifier) string {
	keep := func(tuple *types.Tuple) *types.Tuple {
		vars := make([]*types.Var, tuple.Len())
		for i := range vars {
			vars[i] = types.NewVar(token.NoPos, nil, tuple.At(i).Name(), anonymous(tuple.At(i).Type()))
		}
		return types.NewTuple(vars...)
	}
	clean := types.NewSignatureType(nil, nil, nil, keep(sig.Params()), keep(sig.Results()), sig.Variadic())
	return strings.TrimPrefix(types.TypeString(clean, q), "func")
}

func qualifierNoImport(pkg *types.Package) types.Qualifier {
	return func(p *types.Package) string {
		if p == pkg {
			return ""
		}
		return p.Name()
	}
}

func constantLiteral(c *types.Const) string {
	v := c.Val()
	basic, _ := c.Type().Underlying().(*types.Basic)
	switch v.Kind() {
	case constant.String:
		return strconv.Quote(constant.StringVal(v))
	case constant.Bool:
		return strconv.FormatBool(constant.BoolVal(v))
	case constant.Int:
		if basic != nil && basic.Kind() == types.UntypedRune {
			n, _ := constant.Int64Val(v)
			return strconv.QuoteRune(rune(n))
		}
		return v.ExactString()
	case constant.Float:
		f, _ := constant.Float64Val(v)
		s := strconv.FormatFloat(f, 'g', -1, 64)
		if !strings.ContainsAny(s, ".eE") {
			s += ".0"
		}
		return s
	}
	return v.ExactString()
}

// declared inventories a package whose exposed API is a Go source file that
// people maintain: everything exported there is exposed, the rest is not.
func (g *generator) declared(pkg *types.Package, file, missing string) {
	have := map[string]bool{}
	if src, err := os.ReadFile(file); err == nil {
		f, err := parser.ParseFile(token.NewFileSet(), file, src, parser.SkipObjectResolution)
		check(err)
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				name := d.Name.Name
				if d.Recv != nil {
					name = receiverName(d.Recv.List[0].Type) + "." + name
				}
				have[name] = true
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch spec := spec.(type) {
					case *ast.TypeSpec:
						have[spec.Name.Name] = true
					case *ast.ValueSpec:
						for _, n := range spec.Names {
							have[n.Name] = true
						}
					}
				}
			}
		}
	}
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		d := decl{pkg: pkg.Path(), name: name, text: types.ObjectString(obj, qualifierNoImport(pkg))}
		if !have[name] {
			d.reason = reasonFor(pkg.Path(), name, missing)
		}
		switch o := obj.(type) {
		case *types.Func:
			d.kind = "func"
		case *types.Const:
			d.kind, d.text = "const", "const "+name
		case *types.Var:
			d.kind = "var"
		case *types.TypeName:
			d.kind, d.text = "type", "type "+name
			g.all = append(g.all, d)
			named, ok := o.Type().(*types.Named)
			if !ok {
				continue
			}
			if _, isInterface := named.Underlying().(*types.Interface); isInterface {
				continue
			}
			methods := types.NewMethodSet(types.NewPointer(named))
			for i := 0; i < methods.Len(); i++ {
				m := methods.At(i).Obj().(*types.Func)
				if !m.Exported() || len(methods.At(i).Index()) != 1 {
					continue
				}
				md := decl{pkg: pkg.Path(), name: name + "." + m.Name(), kind: "method", text: "func (" + name + ") " + m.Name() + strings.TrimPrefix(types.TypeString(m.Signature(), qualifierNoImport(pkg)), "func")}
				if !have[md.name] || d.reason != "" {
					md.reason = reasonFor(pkg.Path(), md.name, missing)
				}
				g.all = append(g.all, md)
			}
			continue
		}
		g.all = append(g.all, d)
	}
}

func receiverName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.StarExpr:
		return receiverName(x.X)
	case *ast.IndexExpr:
		return receiverName(x.X)
	case *ast.IndexListExpr:
		return receiverName(x.X)
	case *ast.Ident:
		return x.Name
	}
	return ""
}

// report writes evmstd/API.md: per package, what scripts can use, and what
// they cannot with the reason.
func (g *generator) report() {
	var b bytes.Buffer
	b.WriteString("# Standard packages for scripts\n\nGenerated by `go run ./internal/genstd` from the Go standard library this\nmodule builds with. Do not edit.\n\n")
	b.WriteString("| Package | How it is provided | Exposed | Not exposed |\n| --- | --- | ---: | ---: |\n")
	how := map[string]string{}
	var order []string
	for _, list := range []struct {
		how  string
		pkgs []string
	}{{"built into the VM", builtin}, {"script source", guest}, {"bound to the Go package", native}} {
		for _, p := range list.pkgs {
			how[p] = list.how
			order = append(order, p)
		}
	}
	sort.Strings(order)
	counts := map[string][2]int{}
	for _, d := range g.all {
		c := counts[d.pkg]
		if d.reason == "" {
			c[0]++
		} else {
			c[1]++
		}
		counts[d.pkg] = c
	}
	for _, p := range order {
		fmt.Fprintf(&b, "| `%s` | %s | %d | %d |\n", p, how[p], counts[p][0], counts[p][1])
	}
	for _, exposed := range []bool{true, false} {
		if exposed {
			b.WriteString("\n## Exposed\n")
		} else {
			b.WriteString("\n## Not exposed\n")
		}
		for _, p := range order {
			var lines []string
			for _, d := range g.all {
				if d.pkg != p || (d.reason == "") != exposed {
					continue
				}
				line := "- `" + d.text + "`"
				if d.kind == "field" {
					line = "- field `" + strings.SplitN(d.name, ".", 2)[0] + "." + d.text + "`"
				}
				if !exposed {
					line += ": " + d.reason
				}
				lines = append(lines, line)
			}
			if len(lines) == 0 {
				continue
			}
			fmt.Fprintf(&b, "\n### %s\n\n%s\n", p, strings.Join(lines, "\n"))
		}
	}
	check(os.WriteFile("evmstd/API.md", b.Bytes(), 0o644))
}

var _ = path.Base
