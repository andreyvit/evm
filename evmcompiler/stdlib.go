package evmcompiler

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	pathpkg "path"
	"slices"
	"strings"

	"golang.org/x/tools/go/ssa"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/values"
)

type standardPackage struct {
	declarationsOnly bool
	sources          []evm.SourceFile
	files            []*ast.File
	info             *types.Info
}

func (e *typeEnvironment) importStandard(path, source string) (*types.Package, error) {
	return e.importSource(path, []evm.SourceFile{{Name: pathpkg.Base(path) + ".go", Text: source}}, nil)
}

func (e *typeEnvironment) importSource(path string, sources []evm.SourceFile, pkg *types.Package) (*types.Package, error) {
	var files []*ast.File
	var decls []ast.Decl
	if err := validateSourceFiles(path, sources); err != nil {
		return nil, err
	}
	for _, source := range sources {
		file, err := parser.ParseFile(e.files, "evm/_stdlib/"+path+"/"+source.Name, source.Text, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
		decls = append(decls, file.Decls...)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("missing source files for %s", path)
	}

	declarationsOnly := true
	requireDeclarations := false
	if host := e.packages[path]; host != nil {
		requireDeclarations = len(host.Records)+len(host.Values) != 0
		for _, d := range host.Types {
			sharedPointer := false
			if d.Name == "" && d.SourceName == "" && len(d.TypeArgs) == 0 && d.Shape.Kind == values.PointerKind && d.Shape.Elem != nil {
				for _, shared := range host.SharedTypes {
					if d.Shape.Elem.Type == shared.Type {
						sharedPointer = true
						break
					}
				}
				for _, other := range host.Types {
					if other.SourceName != "" && len(other.TypeArgs) == 0 && other.Shape.Type == d.Shape.Elem.Type {
						sharedPointer = true
					}
				}
			}
			if d.SourceName != "" && len(d.TypeArgs) == 0 {
				sharedPointer = true
			}
			if d.Name == "" && d.SourceName == "" && d.Shape.Type == values.ErrorType {
				// How native functions of the package return the built-in
				// error type; it declares nothing that source could miss.
				sharedPointer = true
			}
			requireDeclarations = requireDeclarations || !sharedPointer
		}
	}
	for _, decl := range decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
			declarationsOnly = false
		}
	}
	if requireDeclarations && !declarationsOnly {
		return nil, fmt.Errorf("mixed native/source package %q requires declarations without function bodies", path)
	}
	var nativeGenerics []*ast.FuncDecl
	for _, decl := range decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			if pkg != nil && fn.Recv != nil && len(fn.Recv.List) != 0 {
				recv := fn.Recv.List[0].Type
			unwrap:
				for {
					switch r := recv.(type) {
					case *ast.StarExpr:
						recv = r.X
					case *ast.ParenExpr:
						recv = r.X
					case *ast.IndexExpr:
						recv = r.X
					case *ast.IndexListExpr:
						recv = r.X
					default:
						break unwrap
					}
				}
				if name, ok := recv.(*ast.Ident); ok && pkg.Scope().Lookup(name.Name) != nil {
					return nil, fmt.Errorf("source method receiver %s must be declared in source", name.Name)
				}
			}
		}
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body == nil && fn.Type.TypeParams.NumFields() != 0 {
			// Go requires generic bodies during checking; host declarations
			// are removed again before building SSA.
			nativeGenerics = append(nativeGenerics, fn)
			fn.Body = &ast.BlockStmt{List: []ast.Stmt{&ast.ExprStmt{X: &ast.CallExpr{Fun: ast.NewIdent("panic"), Args: []ast.Expr{&ast.BasicLit{Kind: token.STRING, Value: `"native declaration"`}}}}}}
		}
		if d, ok := decl.(*ast.GenDecl); ok && d.Tok == token.VAR {
			return nil, fmt.Errorf("virtual package %q cannot declare globals", path)
		}
		if d, ok := decl.(*ast.FuncDecl); ok && d.Recv == nil && d.Name.Name == "init" {
			return nil, fmt.Errorf("virtual package %q cannot declare init functions", path)
		}
	}
	info := &types.Info{Implicits: make(map[ast.Node]types.Object), FileVersions: make(map[*ast.File]string), Types: make(map[ast.Expr]types.TypeAndValue), Defs: make(map[*ast.Ident]types.Object), Uses: make(map[*ast.Ident]types.Object), Selections: make(map[*ast.SelectorExpr]*types.Selection), Scopes: make(map[ast.Node]*types.Scope), Instances: make(map[*ast.Ident]types.Instance)}
	config := types.Config{GoVersion: "go1.23", Sizes: types.SizesFor("gc", "amd64"), Importer: e}
	if pkg == nil {
		pkg = types.NewPackage(path, files[0].Name.Name)
	}
	err := types.NewChecker(&config, e.files, pkg, info).Files(files)
	for _, fn := range nativeGenerics {
		fn.Body = nil
	}
	if err != nil {
		return nil, err
	}
	e.imported[path] = pkg
	e.standard[path] = &standardPackage{declarationsOnly: declarationsOnly, sources: slices.Clone(sources), files: files, info: info}
	var functions map[string]*evm.ExternalFunction
	if host := e.packages[path]; host != nil {
		functions = make(map[string]*evm.ExternalFunction, len(host.Functions))
		for i := range host.Functions {
			f := &host.Functions[i]
			if f.On == nil && functions[f.LinkName()] == nil {
				functions[f.LinkName()] = f
			}
		}
	}
	for _, decl := range decls {
		f, ok := decl.(*ast.FuncDecl)
		if !ok || f.Body != nil {
			continue
		}
		obj := info.Defs[f.Name].(*types.Func)
		// Defer VM type/layout construction until this leaf is reachable.
		var native *evm.ExternalFunction
		if f.Recv == nil {
			native = functions[obj.Name()]
		}
		e.natives[obj] = nativeBinding{importKey{pkg: path, name: obj.Name()}, native}
	}
	return pkg, nil
}

func (c *typedCompiler) debugPrintImport(args []ssa.Value, newline bool, pos token.Pos) (evm.FunctionRef, evm.Signature) {
	var sig evm.Signature
	for _, v := range args {
		s, err := c.env.shape(v.Type())
		if err != nil {
			c.fail(pos, "%v", err)
			return evm.FunctionRef{}, sig
		}
		sig.Params = append(sig.Params, s.Type)
	}
	name := "print"
	if newline {
		name = "println"
	}
	for i := range c.module.Imports {
		imp := &c.module.Imports[i]
		if imp.Package == "evm/debug" && imp.Name == name && slices.Equal(imp.Signature.Params, sig.Params) {
			return evm.FunctionRef{Native: true, Index: uint32(i)}, sig
		}
	}
	idx := uint32(len(c.module.Imports))
	c.module.Imports = append(c.module.Imports, evm.Import{Package: "evm/debug", Name: name, Signature: sig})
	return evm.FunctionRef{Native: true, Index: idx}, sig
}
func (f *lowerer) printBuiltin(call *ssa.Call, newline bool) {
	ref, sig := f.c.debugPrintImport(call.Call.Args, newline, call.Pos())
	layout, err := evm.LayoutOf(sig.Params)
	if err != nil {
		f.c.fail(call.Pos(), "%v", err)
		return
	}
	f.outR = max(f.outR, layout.RCount)
	f.outP = max(f.outP, layout.PCount)
	for i, v := range call.Call.Args {
		f.outMove(layout.Slots[i], f.regs[v][0], false)
	}
	f.emit(evm.WithBC(evm.OpCallHost, 0, ref.Index))
}

// SourceImporter exposes EVM standard packages and supplied vetted packages
// when checking host declarations.
func SourceImporter(files *token.FileSet, packages ...evm.Package) types.Importer {
	e, err := newTypeEnvironment(packages)
	if err != nil {
		panic(err)
	}
	e.files = files
	return &sourceImporter{e}
}

type sourceImporter struct{ *typeEnvironment }

func (s *sourceImporter) Import(path string) (*types.Package, error) {
	if path == "github.com/andreyvit/evm" {
		if pkg := s.imported[path]; pkg != nil {
			return pkg, nil
		}
		// Host declarations may annotate retained function signatures. This
		// opaque handle is not an importable guest package or a native layout.
		pkg := types.NewPackage(path, "evm")
		handle := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "Callable", nil), types.NewStruct(nil, nil), nil)
		pkg.Scope().Insert(handle.Obj())
		for _, name := range []string{"FunctionOf", "ConcurrentFunctionOf"} {
			alias := types.NewAlias(types.NewTypeName(token.NoPos, pkg, name, nil), handle)
			alias.SetTypeParams([]*types.TypeParam{types.NewTypeParam(types.NewTypeName(token.NoPos, pkg, "Signature", nil), types.Universe.Lookup("any").Type())})
			pkg.Scope().Insert(alias.Obj())
		}
		pkg.MarkComplete()
		s.imported[path] = pkg
		return pkg, nil
	}
	if path == "github.com/andreyvit/evm/evmruntime/log" {
		source, _ := evm.StandardLibrarySource("evmruntime/log")
		return s.importStandard(path, source)
	}
	return s.typeEnvironment.Import(path)
}

func validateSourceFiles(path string, sources []evm.SourceFile) error {
	names := map[string]bool{}
	for _, source := range sources {
		if source.Name == ".go" || !strings.HasSuffix(source.Name, ".go") || strings.ContainsAny(source.Name, "/\\") {
			return fmt.Errorf("invalid source filename %q in %s: expected a Go file basename", source.Name, path)
		}
		if names[source.Name] {
			return fmt.Errorf("duplicate source filename %q in %s", source.Name, path)
		}
		names[source.Name] = true
	}
	return nil
}
