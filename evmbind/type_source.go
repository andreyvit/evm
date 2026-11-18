package evmbind

import (
	"fmt"
	"go/ast"
	"go/token"
	"strconv"
)

// Keep native declaration syntax, including complete const blocks so iota and
// omitted expressions retain their meaning. Unrelated implementation imports
// and bodies never enter the script declaration checker.
func selectTypeSyntax(files []*ast.File, names []string) ([]*ast.File, map[string]bool, error) {
	return selectTypeSyntaxMethods(files, names, nil)
}
func selectTypeSyntaxMethods(files []*ast.File, names []string, methods map[string][]string) ([]*ast.File, map[string]bool, error) {
	return selectTypeSyntaxFunctions(files, names, methods, nil)
}

func selectTypeSyntaxFunctions(files []*ast.File, names []string, methods map[string][]string, functions []string) ([]*ast.File, map[string]bool, error) {
	return selectSyntax(files, names, methods, functions, false, nil)
}

// wholeSelection selects every exported type, method and function, less
// Options.Omit. A non-nil Options.Methods still chooses the methods.
func wholeSelection(files []*ast.File, opt Options) (names []string, methods map[string][]string, functions map[string]bool, omit map[string]bool, err error) {
	omit = map[string]bool{}
	for _, name := range opt.Omit {
		if omit[name] {
			return nil, nil, nil, nil, fmt.Errorf("duplicate omitted declaration %s", name)
		}
		omit[name] = true
	}
	matched := map[string]bool{}
	seen := func(name string) bool {
		if omit[name] {
			matched[name] = true
			return true
		}
		return false
	}
	names = []string{}
	exportedTypes := map[string]bool{}
	for _, file := range files {
		for _, decl := range file.Decls {
			g, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range g.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					if spec.Name.IsExported() && !seen(spec.Name.Name) {
						names = append(names, spec.Name.Name)
						exportedTypes[spec.Name.Name] = true
					}
				case *ast.ValueSpec:
					if g.Tok == token.CONST {
						for _, name := range spec.Names {
							seen(name.Name)
						}
					}
				}
			}
		}
	}
	functions = functionNames(opt)
	methods = opt.Methods
	if methods == nil {
		methods = map[string][]string{}
	}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() {
				continue
			}
			if fn.Recv == nil {
				if !seen(fn.Name.Name) {
					functions[fn.Name.Name] = true
				}
				continue
			}
			receiver := embeddedFieldName(fn.Recv.List[0].Type)
			if !seen(receiver+"."+fn.Name.Name) && opt.Methods == nil && exportedTypes[receiver] {
				methods[receiver] = append(methods[receiver], fn.Name.Name)
			}
		}
	}
	for _, name := range opt.Omit {
		if !matched[name] {
			return nil, nil, nil, nil, fmt.Errorf("omitted declaration %s is not an exported type, function, constant or method in source", name)
		}
		if functions[name] {
			return nil, nil, nil, nil, fmt.Errorf("omitted function %s is also bound in Options", name)
		}
	}
	return names, methods, functions, omit, nil
}

// selectSyntax reduces native source to the declarations a schema exposes and
// the local declarations they need. Function bodies, variables and unused
// imports are dropped. allConstants keeps every constant block with an
// exported name outside omit, not only the constants of exposed types.
func selectSyntax(files []*ast.File, names []string, methods map[string][]string, functions []string, allConstants bool, omit map[string]bool) ([]*ast.File, map[string]bool, error) {
	selectedMethods, err := selectMethods(files, methods)
	if err != nil {
		return nil, nil, err
	}
	selectedFunctions := map[*ast.FuncDecl]bool{}
	pending := map[string]bool{}
	for _, name := range functions {
		pending[name] = true
	}
	for _, file := range files {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && pending[fn.Name.Name] {
				if !fn.Name.IsExported() {
					return nil, nil, fmt.Errorf("selected function %s is not exported", fn.Name.Name)
				}
				selectedFunctions[fn] = true
				delete(pending, fn.Name.Name)
			}
		}
	}
	for name := range pending {
		return nil, nil, fmt.Errorf("selected function %s is not declared in source", name)
	}
	typesByName := map[string]*ast.TypeSpec{}
	constants := map[string]*ast.GenDecl{}
	var blocks []*ast.GenDecl
	for _, f := range files {
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok {
				continue
			}
			switch g.Tok {
			case token.TYPE:
				for _, sp := range g.Specs {
					t := sp.(*ast.TypeSpec)
					if typesByName[t.Name.Name] != nil {
						return nil, nil, fmt.Errorf("duplicate type %s", t.Name.Name)
					}
					typesByName[t.Name.Name] = t
				}
			case token.CONST:
				blocks = append(blocks, g)
				for _, sp := range g.Specs {
					for _, n := range sp.(*ast.ValueSpec).Names {
						constants[n.Name] = g
					}
				}
			}
		}
	}
	if names == nil {
		for name := range typesByName {
			if ast.IsExported(name) {
				names = append(names, name)
			}
		}
	}
	for _, name := range names {
		if typesByName[name] == nil {
			return nil, nil, fmt.Errorf("selected type %s is not declared in source", name)
		}
	}
	exposed := map[string]bool{}
	keptTypes := map[string]bool{}
	keptBlocks := map[*ast.GenDecl]bool{}
	var add func(string, bool)
	var addBlock func(*ast.GenDecl)
	addBlock = func(g *ast.GenDecl) {
		if keptBlocks[g] {
			return
		}
		keptBlocks[g] = true
		for _, sp := range g.Specs {
			v := sp.(*ast.ValueSpec)
			typeReferences(v.Type, func(n string) { add(n, false) }, nil)
			for _, x := range v.Values {
				typeReferences(x, func(n string) { add(n, false) }, nil)
			}
		}
	}
	add = func(name string, expose bool) {
		if t := typesByName[name]; t != nil {
			if keptTypes[name] && (!expose || exposed[name]) {
				return
			}
			keptTypes[name] = true
			if expose {
				exposed[name] = true
			}
			typeReferences(t.Type, func(n string) { add(n, expose) }, nil)
			if t.TypeParams != nil {
				for _, f := range t.TypeParams.List {
					typeReferences(f.Type, func(n string) { add(n, false) }, nil)
				}
			}
		} else if g := constants[name]; g != nil {
			addBlock(g)
		}
	}
	for _, name := range names {
		add(name, true)
	}
	for fn := range selectedFunctions {
		parameters := map[string]bool{}
		if fn.Type.TypeParams != nil {
			for _, field := range fn.Type.TypeParams.List {
				for _, name := range field.Names {
					parameters[name.Name] = true
				}
			}
			for _, field := range fn.Type.TypeParams.List {
				typeReferences(field.Type, func(n string) {
					if !parameters[n] {
						add(n, false)
					}
				}, nil)
			}
		}
		signature := *fn.Type
		signature.TypeParams = nil
		typeReferences(&signature, func(n string) {
			if !parameters[n] {
				add(n, true)
			}
		}, nil)
	}
	visitedMethods := map[*ast.FuncDecl]bool{}
	for {
		progress := false
		for fn := range selectedMethods {
			if visitedMethods[fn] || !exposed[embeddedFieldName(fn.Recv.List[0].Type)] {
				continue
			}
			typeReferences(fn.Type, func(n string) { add(n, true) }, nil)
			visitedMethods[fn] = true
			progress = true
		}
		if !progress {
			break
		}
	}
	for fn := range selectedMethods {
		if !visitedMethods[fn] {
			return nil, nil, fmt.Errorf("method receiver %s is not selected", embeddedFieldName(fn.Recv.List[0].Type))
		}
	}
	if allConstants {
		for _, g := range blocks {
			for _, sp := range g.Specs {
				for _, n := range sp.(*ast.ValueSpec).Names {
					if n.IsExported() && !omit[n.Name] {
						addBlock(g)
					}
				}
			}
		}
	}
	// Include aliases of selected enum constants, even across const blocks.
	for changed := true; changed; {
		changed = false
		for _, g := range blocks {
			if keptBlocks[g] {
				continue
			}
			wanted := false
			check := func(n string) {
				if exposed[n] || constants[n] != nil && keptBlocks[constants[n]] {
					wanted = true
				}
			}
			for _, sp := range g.Specs {
				v := sp.(*ast.ValueSpec)
				typeReferences(v.Type, check, nil)
				for _, x := range v.Values {
					typeReferences(x, check, nil)
				}
			}
			if wanted {
				addBlock(g)
				changed = true
			}
		}
	}
	var result []*ast.File
	for _, f := range files {
		out := &ast.File{Name: f.Name, Package: f.Package}
		usedImports := map[string]bool{}
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && (selectedMethods[fn] || selectedFunctions[fn]) {
				copy := *fn
				copy.Body = nil
				if fn.Type.TypeParams != nil && len(fn.Type.TypeParams.List) != 0 {
					// go/types requires a generic function body. The native
					// body is irrelevant and may depend on unexposed code.
					copy.Body = &ast.BlockStmt{List: []ast.Stmt{&ast.ExprStmt{X: &ast.CallExpr{Fun: ast.NewIdent("panic"), Args: []ast.Expr{&ast.BasicLit{Kind: token.INT, Value: "0"}}}}}}
				}
				out.Decls = append(out.Decls, &copy)
				typeReferences(fn.Type, nil, func(n string) { usedImports[n] = true })
				continue
			}
			g, ok := d.(*ast.GenDecl)
			if !ok {
				continue
			}
			switch g.Tok {
			case token.TYPE:
				for _, sp := range g.Specs {
					t := sp.(*ast.TypeSpec)
					if keptTypes[t.Name.Name] {
						out.Decls = append(out.Decls, &ast.GenDecl{TokPos: g.TokPos, Tok: token.TYPE, Specs: []ast.Spec{t}})
						typeReferences(t.Type, nil, func(n string) { usedImports[n] = true })
					}
				}
			case token.CONST:
				if keptBlocks[g] {
					out.Decls = append(out.Decls, g)
					for _, sp := range g.Specs {
						v := sp.(*ast.ValueSpec)
						typeReferences(v.Type, nil, func(n string) { usedImports[n] = true })
						for _, x := range v.Values {
							typeReferences(x, nil, func(n string) { usedImports[n] = true })
						}
					}
				}
			}
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			name := path
			for i := len(name) - 1; i >= 0; i-- {
				if name[i] == '/' {
					name = name[i+1:]
					break
				}
			}
			if imp.Name != nil {
				name = imp.Name.Name
			}
			if usedImports[name] {
				out.Imports = append(out.Imports, imp)
			}
		}
		if len(out.Imports) > 0 {
			specs := make([]ast.Spec, len(out.Imports))
			for i, imp := range out.Imports {
				specs[i] = imp
			}
			out.Decls = append([]ast.Decl{&ast.GenDecl{Tok: token.IMPORT, Specs: specs}}, out.Decls...)
		}
		result = append(result, out)
	}
	return result, exposed, nil
}

// Field and selector names are not local type references. Walking them as
// identifiers would accidentally export an unrelated type with the same name.
func typeReferences(x ast.Expr, local, imported func(string)) {
	if x == nil {
		return
	}
	ast.Inspect(x, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			if id, ok := n.X.(*ast.Ident); ok && imported != nil {
				imported(id.Name)
			}
			return false
		case *ast.Field:
			typeReferences(n.Type, local, imported)
			return false
		case *ast.Ident:
			if local != nil {
				local(n.Name)
			}
		}
		return true
	})
}

func selectMethods(files []*ast.File, selected map[string][]string) (map[*ast.FuncDecl]bool, error) {
	pending := map[string]bool{}
	for typ, names := range selected {
		for _, name := range names {
			key := typ + "." + name
			if pending[key] {
				return nil, fmt.Errorf("duplicate selected method %s", key)
			}
			pending[key] = true
		}
	}
	result := map[*ast.FuncDecl]bool{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil {
				continue
			}
			key := embeddedFieldName(fn.Recv.List[0].Type) + "." + fn.Name.Name
			if pending[key] {
				if !fn.Name.IsExported() {
					return nil, fmt.Errorf("selected method %s is not exported", key)
				}
				result[fn] = true
				delete(pending, key)
			}
		}
	}
	// Interface declarations contain their methods directly, rather than as
	// receiver declarations. Prune before dependency and import discovery.
	for _, file := range files {
		for _, decl := range file.Decls {
			g, ok := decl.(*ast.GenDecl)
			if !ok || g.Tok != token.TYPE {
				continue
			}
			for _, spec := range g.Specs {
				t := spec.(*ast.TypeSpec)
				in, ok := t.Type.(*ast.InterfaceType)
				if _, configured := selected[t.Name.Name]; !ok || !configured {
					continue
				}
				var fields []*ast.Field
				seen := map[string]bool{}
				for _, field := range in.Methods.List {
					name := embeddedFieldName(field.Type)
					if len(field.Names) != 0 {
						name = field.Names[0].Name
					}
					key := t.Name.Name + "." + name
					if seen[name] {
						return nil, fmt.Errorf("ambiguous interface member %s", key)
					}
					if pending[key] {
						seen[name] = true
						if !ast.IsExported(name) {
							return nil, fmt.Errorf("selected method %s is not exported", key)
						}
						fields = append(fields, field)
						delete(pending, key)
					}
				}
				in.Methods.List = fields
			}
		}
	}
	for key := range pending {
		return nil, fmt.Errorf("selected method %s is not declared in source", key)
	}
	return result, nil
}
