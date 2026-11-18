package gox

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Component calls and slots become ordinary, typed Go calls and closures.
func (c *compiler) components() (bool, error) {
	changed := false
	var expand func(*ast.BlockStmt) error
	expand = func(block *ast.BlockStmt) error {
		var result []ast.Stmt
		for i := 0; i < len(block.List); i++ {
			stmt := block.List[i]
			m, call, ok := c.statementMarkup(stmt)
			var target ast.Expr
			var sig *types.Signature
			component := false
			if ok && m.kind == markupOpen {
				target, sig, component = c.resolveComponent(m.name, stmt.Pos())
				if component {
					m.void = m.selfClosing
				}
			}
			if component {
				if _, assignment := stmt.(*ast.AssignStmt); assignment && !m.selfClosing {
					return c.error(stmt.Pos(), "assigned XML calls must be self-closing")
				}
				end := i
				var children []ast.Stmt
				if !m.void {
					depth := 1
					for end = i + 1; end < len(block.List); end++ {
						x, _, yes := c.statementMarkup(block.List[end])
						if yes && x.name == m.name {
							if x.kind == markupOpen && !x.selfClosing {
								depth++
							}
							if x.kind == markupClose {
								depth--
								if depth == 0 {
									break
								}
							}
						}
					}
					if end >= len(block.List) {
						return c.error(stmt.Pos(), "component must close in the same Go block")
					}
					children = block.List[i+1 : end]
				}
				args := call.Args[1:]
				props := map[string]ast.Expr{}
				var mapInputs []componentMapInput
				for _, a := range m.attrs {
					e, err := c.componentAttribute(a, args)
					if err != nil {
						return c.error(stmt.Pos(), err.Error())
					}
					if target, key, ok := strings.Cut(a.name, ":"); ok {
						var keys []ast.Expr
						static := key
						if a.keyArg != 0 {
							static = strings.TrimSuffix(static, ":")
						}
						if static != "" {
							for _, part := range strings.Split(static, ":") {
								if part == "" {
									return c.error(stmt.Pos(), "empty map input path segment; quote keys containing colons")
								}
								keys = append(keys, &ast.BasicLit{Kind: token.STRING, Value: quote(part)})
							}
						}
						if a.keyArg != 0 {
							keys = append(keys, unwrap(args[a.keyArg-1]))
						}
						if len(keys) == 0 {
							return c.error(stmt.Pos(), "empty map input name")
						}
						mapInputs = append(mapInputs, componentMapInput{target: target, keys: keys, value: e})
						continue
					}
					if _, exists := props[a.name]; exists {
						return c.error(stmt.Pos(), "duplicate component input "+a.name)
					}
					props[a.name] = e
				}

				if sig == nil {
					return c.error(stmt.Pos(), "unknown or untyped component "+m.name)
				}
				slots := map[string][]ast.Stmt{}
				var body []ast.Stmt
				for j := 0; j < len(children); j++ {
					sm, _, yes := c.statementMarkup(children[j])
					if !yes || sm.kind != markupOpen || !strings.HasPrefix(sm.name, "slot-") {
						body = append(body, children[j])
						continue
					}
					slot := strings.TrimPrefix(sm.name, "slot-")
					if _, exists := slots[slot]; exists {
						return c.error(children[j].Pos(), "duplicate slot "+slot)
					}
					if len(sm.attrs) != 0 {
						return c.error(children[j].Pos(), "slot parameters come from the declared function signature")
					}
					if sm.void {
						slots[slot] = nil
						continue
					}
					k := j + 1
					depth := 1
					for ; k < len(children); k++ {
						xm, _, yes := c.statementMarkup(children[k])
						if yes && xm.name == sm.name {
							if xm.kind == markupOpen && !xm.void {
								depth++
							}
							if xm.kind == markupClose {
								depth--
								if depth == 0 {
									break
								}
							}
						}
					}
					if k == len(children) {
						return c.error(children[j].Pos(), "unclosed slot "+slot)
					}
					slots[slot] = children[j+1 : k]
					j = k
				}
				if len(body) > 0 {
					if _, exists := slots["body"]; exists {
						return c.error(stmt.Pos(), "body supplied both explicitly and as children")
					}
					slots["body"] = body
				}
				for name := range slots {
					if _, exists := props[name]; exists {
						return c.error(stmt.Pos(), "duplicate component input "+name)
					}
				}
				if err := c.componentConventions(m.name, sig, props, mapInputs, stmt.Pos()); err != nil {
					return err
				}
				overrides, err := c.importedArgDirectives(m.name, sig)
				if err != nil {
					return err
				}
				for key := range props {
					found := false
					for i := 0; i < sig.Params().Len(); i++ {
						if sig.Params().At(i).Name() == key {
							found = true
						}
					}
					if !found {
						return c.error(stmt.Pos(), "unknown component input "+key)
					}
				}
				var actual []ast.Expr
				used := map[string]bool{}
				for p := 0; p < sig.Params().Len(); p++ {
					param := sig.Params().At(p)
					key := param.Name()
					used[key] = true
					// A supplied slot wins over the zero value that an optional
					// parameter received from the conventions above.
					_, slot := slots[key]
					if e, ok := props[key]; ok && !slot {
						actual = append(actual, e)
						continue
					}
					if statements, ok := slots[key]; ok {
						ft, ok := param.Type().Underlying().(*types.Signature)
						if !ok {
							return c.error(stmt.Pos(), "slot "+key+" is not a function")
						}
						expr, err := parser.ParseExpr(types.TypeString(ft, c.qualify))
						if err != nil {
							return err
						}
						typ := expr.(*ast.FuncType)
						clearPositions(typ)
						typ.Func = stmt.Pos()
						typ.Params.Opening = stmt.Pos()
						typ.Params.Closing = stmt.Pos()
						end := stmt.End()
						if len(statements) > 0 {
							end = statements[len(statements)-1].End()
						}
						fn := &ast.FuncLit{Type: typ, Body: &ast.BlockStmt{Lbrace: stmt.Pos(), Rbrace: end, List: statements}}
						if c.slotFuncs == nil {
							c.slotFuncs = map[*ast.FuncLit]bool{}
						}
						c.slotFuncs[fn] = true
						if err = expand(fn.Body); err != nil {
							return err
						}
						actual = append(actual, fn)
						continue
					}
					value := c.implicitValue(param, overrides[key], stmt.Pos())
					if value == nil {
						return c.error(stmt.Pos(), "missing required component input "+key)
					}
					actual = append(actual, value)
				}
				for key := range props {
					if !used[key] {
						return c.error(stmt.Pos(), "unknown component input "+key)
					}
				}
				for key := range slots {
					if !used[key] {
						return c.error(stmt.Pos(), "unknown component slot "+key)
					}
				}
				invocation := &ast.CallExpr{Fun: target, Args: actual, Lparen: call.Pos()}
				invocation = c.captureAmbientCall(invocation, sig, stmt.Pos())
				if c.componentCalls == nil {
					c.componentCalls = map[*ast.CallExpr]bool{}
				}
				c.componentCalls[invocation] = true
				if assignment, ok := stmt.(*ast.AssignStmt); ok {
					assignment.Rhs[0] = invocation
					result = append(result, assignment)
				} else {
					result = append(result, &ast.ExprStmt{X: invocation})
				}
				i = end
				changed = true
				continue
			}
			var err error
			ast.Inspect(stmt, func(n ast.Node) bool {
				if err != nil {
					return false
				}
				switch n := n.(type) {
				case *ast.IfStmt:
					if n.Init != nil {
						init := &ast.BlockStmt{List: []ast.Stmt{n.Init}}
						err = expand(init)
						n.Init = init.List[0]
					}
				case *ast.CaseClause:
					body := &ast.BlockStmt{List: n.Body}
					err = expand(body)
					n.Body = body.List
					return false
				case *ast.CommClause:
					body := &ast.BlockStmt{List: n.Body}
					err = expand(body)
					n.Body = body.List
					return false
				}
				if b, ok := n.(*ast.BlockStmt); ok {
					err = expand(b)
					return false
				}
				return true
			})
			if err != nil {
				return err
			}
			result = append(result, stmt)
		}
		block.List = result
		return nil
	}
	for _, d := range c.file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Body != nil {
			if err := expand(f.Body); err != nil {
				return false, err
			}
		}
	}
	return changed, nil
}

// Components resolve like Go expressions, including lexical function
// values and package/receiver selectors. Unknown capitalized names are errors.
func (c *compiler) resolveComponent(name string, pos token.Pos) (ast.Expr, *types.Signature, bool) {
	if name == "" || strings.HasPrefix(name, "slot-") {
		return nil, nil, false
	}
	expr, err := parser.ParseExpr(name)
	if err != nil {
		return nil, nil, false
	}
	switch expr.(type) {
	case *ast.Ident, *ast.SelectorExpr:
	default:
		return nil, nil, false
	}
	var sig *types.Signature
	if value, err := types.Eval(c.files, c.pkg, pos, name); err == nil && value.Type != nil {
		sig, _ = value.Type.Underlying().(*types.Signature)
	}
	r, _ := utf8.DecodeRuneInString(name)
	component := sig != nil || unicode.IsUpper(r) || strings.Contains(name, ".")
	ast.Inspect(expr, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			id.NamePos = pos
		}
		return true
	})
	return expr, sig, component
}
func (c *compiler) statementMarkup(stmt ast.Stmt) (markup, *ast.CallExpr, bool) {
	call, ok := statementCall(stmt)
	if !ok {
		return markup{}, nil, false
	}
	m, ok := c.marker(call)
	return m, call, ok
}
func (c *compiler) builderAt(pos token.Pos) string {
	for scope := c.scope(pos); scope != nil; scope = scope.Parent() {
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			if types.TypeString(obj.Type(), func(p *types.Package) string { return p.Name() }) == "*gox.Builder" {
				return name
			}
		}
	}
	return "_gox_b"
}
func (c *compiler) componentAttribute(a attribute, args []ast.Expr) (ast.Expr, error) {
	if a.boolean {
		return ast.NewIdent("true"), nil
	}
	if len(a.parts) == 1 && a.parts[0].arg > 0 {
		return unwrap(args[a.parts[0].arg-1]), nil
	}
	var expr ast.Expr
	for _, p := range a.parts {
		var next ast.Expr
		if p.arg == 0 {
			next = &ast.BasicLit{Kind: token.STRING, Value: quote(p.text)}
		} else {
			c.generatedImport("fmt", "fmt")
			next = &ast.CallExpr{Fun: sel("fmt", "Sprint"), Args: []ast.Expr{unwrap(args[p.arg-1])}}
		}
		if expr == nil {
			expr = next
		} else {
			expr = &ast.BinaryExpr{X: expr, Op: token.ADD, Y: next}
		}
	}
	if expr == nil {
		return nil, fmt.Errorf("empty attribute expression")
	}
	return expr, nil
}

// statementCall also recognizes a single XML call used as an assignment RHS.
func statementCall(n ast.Node) (*ast.CallExpr, bool) {
	var expr ast.Expr
	switch n := n.(type) {
	case *ast.ExprStmt:
		expr = n.X
	case *ast.AssignStmt:
		if len(n.Rhs) == 1 {
			expr = n.Rhs[0]
		}
	}
	call, ok := expr.(*ast.CallExpr)
	return call, ok
}
