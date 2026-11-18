package gox

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
)

func (c *compiler) parameterImplicit(p *types.Var, override argOverride) bool {
	if p.Pkg() == c.pkg {
		override = c.argOverrides[p.Pos()]
	}
	if override.implicit != nil {
		return *override.implicit
	}
	name := types.TypeString(p.Type(), func(p *types.Package) string { return p.Path() })
	return name == "*github.com/andreyvit/evm/gox.Builder" || name == "*gox.Builder" || name == "*evmruntime.Ambient" || name == "*github.com/andreyvit/evm/evmruntime.Ambient" || slices.Contains(c.implicitTypes, name)
}

func (c *compiler) implicitValue(p *types.Var, override argOverride, pos token.Pos) ast.Expr {
	if !c.parameterImplicit(p, override) {
		return nil
	}
	if types.TypeString(p.Type(), func(p *types.Package) string { return p.Name() }) == "*gox.Builder" {
		if name := c.builderAt(pos); name != "" {
			return ast.NewIdent(name)
		}
		return nil
	}
	if scope := c.scope(pos); scope != nil {
		_, obj := scope.LookupParent(p.Name(), pos)
		if obj != nil && types.AssignableTo(obj.Type(), p.Type()) {
			return &ast.Ident{Name: p.Name(), NamePos: pos}
		}
	}
	return nil
}

func (c *compiler) resolvePlainArguments(call *ast.CallExpr, sig *types.Signature) (bool, error) {
	if c.resolvedCalls[call] || c.componentCalls[call] {
		return false, nil
	}
	name := nodeString(call.Fun)
	overrides, err := c.importedArgDirectives(name, sig)
	if err != nil {
		return false, err
	}
	hasAmbient := false
	for i := 0; i < sig.Params().Len(); i++ {
		p := sig.Params().At(i)
		if p.Pkg() == c.pkg {
			if override, ok := c.argOverrides[p.Pos()]; ok {
				if overrides == nil {
					overrides = make(map[string]argOverride)
				}
				overrides[p.Name()] = override
			}
		}
		hasAmbient = hasAmbient || overrides[p.Name()].ambient != ""
	}
	fixedCount := sig.Params().Len()
	if sig.Variadic() {
		fixedCount--
	}
	complete := len(call.Args) == fixedCount || sig.Variadic() && len(call.Args) >= fixedCount
	if complete && !hasAmbient {
		for i := 0; i < fixedCount; i++ {
			actual := c.info.TypeOf(call.Args[i])
			if actual == nil || !types.AssignableTo(actual, sig.Params().At(i).Type()) {
				complete = false
				break
			}
		}
		if complete {
			return false, nil
		}
	}

	for i := 0; i < sig.Params().Len(); i++ {
		if sig.Params().At(i).Name() == "" {
			return false, nil
		}
	}
	props := make(map[string]ast.Expr)
	index := 0
	fixed := sig.Params().Len()
	if sig.Variadic() {
		fixed--
	}
	for i := 0; i < fixed; i++ {
		p := sig.Params().At(i)
		if overrides[p.Name()].ambient != "" {
			continue
		}
		if index < len(call.Args) {
			actual := c.info.TypeOf(call.Args[index])
			if actual == nil {
				return false, nil
			}
			if types.AssignableTo(actual, p.Type()) || !c.parameterImplicit(p, overrides[p.Name()]) {
				props[p.Name()] = call.Args[index]
				index++
				continue
			}
		}
		if value := c.implicitValue(p, overrides[p.Name()], call.Pos()); value != nil {
			props[p.Name()] = value
		}
	}
	if !sig.Variadic() && index < len(call.Args) {
		if hasAmbient {
			return false, c.error(call.Pos(), "ambient arguments cannot be passed explicitly")
		}
		return false, nil
	}
	if err := c.componentConventions(name, sig, props, nil, call.Pos()); err != nil {
		return false, err
	}
	var args []ast.Expr
	for i := 0; i < fixed; i++ {
		p := sig.Params().At(i)
		value := props[p.Name()]
		if value == nil {
			return false, nil
		}
		args = append(args, value)
	}
	if sig.Variadic() {
		args = append(args, call.Args[index:]...)
	}
	before := nodeString(call)
	call.Args = args
	original := *call
	if captured := c.captureAmbientCall(&original, sig, call.Pos()); captured != &original {
		*call = *captured
	}
	if c.resolvedCalls == nil {
		c.resolvedCalls = make(map[*ast.CallExpr]bool)
	}
	c.resolvedCalls[call] = true
	changed := before != nodeString(call)
	return changed, nil
}
