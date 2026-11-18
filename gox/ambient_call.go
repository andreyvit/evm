package gox

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
)

type ambientCapture struct {
	name  string
	typ   types.Type
	value ast.Expr
}

func (c *compiler) captureAmbientCall(call *ast.CallExpr, sig *types.Signature, pos token.Pos) *ast.CallExpr {
	capture, ok := c.ambientCaptures[pos]
	if !ok {
		return call
	}
	delete(c.ambientCaptures, pos)
	typeName := func(t types.Type) string {
		return types.TypeString(t, func(p *types.Package) string {
			if p == c.pkg {
				return ""
			}
			return p.Name()
		})
	}
	typ, _ := parser.ParseExpr(typeName(capture.typ))
	clearPositions(typ)
	ft := &ast.FuncType{Params: &ast.FieldList{List: []*ast.Field{{Names: []*ast.Ident{ast.NewIdent(capture.name)}, Type: typ}}}}
	var statement ast.Stmt = &ast.ExprStmt{X: call}
	if sig.Results().Len() > 0 {
		ft.Results = &ast.FieldList{}
		for i := 0; i < sig.Results().Len(); i++ {
			t, _ := parser.ParseExpr(typeName(sig.Results().At(i).Type()))
			clearPositions(t)
			ft.Results.List = append(ft.Results.List, &ast.Field{Type: t})
		}
		statement = &ast.ReturnStmt{Results: []ast.Expr{call}}
	}
	if c.resolvedCalls == nil {
		c.resolvedCalls = make(map[*ast.CallExpr]bool)
	}
	c.resolvedCalls[call] = true
	return &ast.CallExpr{Fun: &ast.FuncLit{Type: ft, Body: &ast.BlockStmt{List: []ast.Stmt{statement}}}, Args: []ast.Expr{capture.value}, Lparen: pos}
}
