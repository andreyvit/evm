package gox

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"strconv"
	"strings"
)

type componentMapInput struct {
	target string
	keys   []ast.Expr
	value  ast.Expr
}

func (c *compiler) componentConventions(componentName string, sig *types.Signature, props map[string]ast.Expr, inputs []componentMapInput, pos token.Pos) error {
	overrides, err := c.importedArgDirectives(componentName, sig)
	if err != nil {
		return err
	}
	typeName := func(t types.Type) string {
		return types.TypeString(t, func(p *types.Package) string {
			if p == c.pkg {
				return ""
			}
			return p.Name()
		})
	}
	exprText := func(e ast.Expr) string {
		var b bytes.Buffer
		_ = format.Node(&b, token.NewFileSet(), e)
		return b.String()
	}
	parse := func(s string) ast.Expr {
		e, err := parser.ParseExpr(s)
		if err != nil {
			panic(err)
		}
		clearPositions(e)
		return e
	}

	targets := make(map[string]*types.Var)
	var ambientParam *types.Var
	ambiguousAmbient := false
	for i := 0; i < sig.Params().Len(); i++ {
		p := sig.Params().At(i)
		targets[p.Name()] = p
		if ptr, ok := types.Unalias(p.Type()).(*types.Pointer); ok {
			if n, ok := types.Unalias(ptr.Elem()).(*types.Named); ok && n.Obj().Name() == "Ambient" && n.Obj().Pkg() != nil && (n.Obj().Pkg().Path() == "github.com/andreyvit/evm/evmruntime" || n.Obj().Pkg().Path() == "evmruntime") {
				if ambientParam != nil {
					ambiguousAmbient = true
				}
				ambientParam = p
			}
		}
	}

	for i := 0; i < sig.Params().Len(); i++ {
		p := sig.Params().At(i)
		override := overrides[p.Name()]
		if p.Pkg() == c.pkg {
			override = c.argOverrides[p.Pos()]
		}
		if override.ambient == "" {
			continue
		}
		if ambientParam == nil || ambiguousAmbient || p == ambientParam {
			return c.error(pos, "ambient-bound arguments require a single Ambient parameter")
		}
		if _, explicit := props[p.Name()]; explicit {
			return c.error(pos, "ambient argument "+p.Name()+" cannot be passed explicitly")
		}
	}
	type mapNode struct {
		key      ast.Expr
		value    ast.Expr
		children []*mapNode
	}
	grouped := make(map[string][]*mapNode)
	var order []string
	for _, input := range inputs {
		target := input.target
		if target == "ambient" {
			if ambiguousAmbient {
				return c.error(pos, "ambient: inputs require a single Ambient parameter")
			}
			if ambientParam == nil {
				return c.error(pos, "component has no parameter for ambient: inputs")
			}
			target = ambientParam.Name()
		} else {
			p := targets[target]
			if p == nil {
				return c.error(pos, "component has no parameter named "+target)
			}
			if _, ok := p.Type().Underlying().(*types.Map); !ok {
				return c.error(pos, "component parameter "+target+" is not a map")
			}
		}
		if _, ok := props[target]; ok {
			return c.error(pos, "cannot combine map entries with explicit "+target)
		}
		if _, ok := grouped[target]; !ok {
			order = append(order, target)
		}
		children := grouped[target]
		branch := &children
		for i, key := range input.keys {
			var node *mapNode
			if literal, ok := key.(*ast.BasicLit); ok {
				for _, child := range *branch {
					if other, ok := child.key.(*ast.BasicLit); ok && literal.Kind == other.Kind && literal.Value == other.Value {
						node = child
						break
					}
				}
			}
			if node == nil {
				node = &mapNode{key: key}
				*branch = append(*branch, node)
			}
			if node.value != nil {
				return c.error(pos, "duplicate or conflicting map input path for "+target)
			}
			if i == len(input.keys)-1 {
				if len(node.children) != 0 {
					return c.error(pos, "cannot combine a map value with nested entries for "+target)
				}
				node.value = input.value
			} else {
				branch = &node.children
			}
		}
		grouped[target] = children
	}
	var renderMap func(types.Type, []*mapNode) (string, error)
	renderMap = func(typ types.Type, nodes []*mapNode) (string, error) {
		if _, anyValue := typ.Underlying().(*types.Interface); anyValue {
			typ = types.NewMap(types.Typ[types.String], types.Universe.Lookup("any").Type())
		}
		mt, ok := typ.Underlying().(*types.Map)
		if !ok {
			return "", c.error(pos, "nested map input requires a map or interface value")
		}
		var entries []string
		for _, node := range nodes {
			var value string
			if node.value != nil {
				value = exprText(node.value)
			} else {
				var err error
				value, err = renderMap(mt.Elem(), node.children)
				if err != nil {
					return "", err
				}
			}
			entries = append(entries, exprText(node.key)+":"+value)
		}
		return typeName(typ) + "{" + strings.Join(entries, ",") + "}", nil
	}
	for _, target := range order {
		typ := targets[target].Type()
		ambient := ambientParam != nil && target == ambientParam.Name()
		mapType := typ
		if ambient {
			mapType = types.NewMap(types.Typ[types.String], types.Universe.Lookup("any").Type())
		}
		value, err := renderMap(mapType, grouped[target])
		if err != nil {
			return err
		}
		if ambient {
			v, err := types.Eval(c.files, c.pkg, pos, "ambient")
			if err != nil || !types.Identical(v.Type, typ) {
				return c.error(pos, "ambient: inputs require ambient *evmruntime.Ambient in caller scope")
			}
			value = "ambient.CloneWith(" + value + ")"
		}
		props[target] = parse(value)
	}

	for i := 0; i < sig.Params().Len(); i++ {
		p := sig.Params().At(i)
		override := overrides[p.Name()]
		if p.Pkg() == c.pkg {
			override = c.argOverrides[p.Pos()]
		}
		if override.ambient == "" {
			continue
		}
		ambient := props[ambientParam.Name()]
		if ambient == nil {
			ambient = c.implicitValue(ambientParam, overrides[ambientParam.Name()], pos)
		}
		if ambient == nil {
			return c.error(pos, "ambient-bound arguments require ambient in caller scope")
		}
		if _, simple := ambient.(*ast.Ident); !simple {
			c.sequence++
			local := fmt.Sprintf("_goxAmbient%d", c.sequence)
			if c.ambientCaptures == nil {
				c.ambientCaptures = make(map[token.Pos]ambientCapture)
			}
			c.ambientCaptures[pos] = ambientCapture{local, ambientParam.Type(), ambient}
			ambient = ast.NewIdent(local)
			props[ambientParam.Name()] = ambient
		}
		tn := typeName(p.Type())
		props[p.Name()] = parse("func(raw any) " + tn + " { if raw == nil { return *new(" + tn + ") }; return raw.(" + tn + ") }((" + exprText(ambient) + ").Get(" + strconv.Quote(override.ambient) + "))")
	}
	for i := 0; i < sig.Params().Len(); i++ {
		p := sig.Params().At(i)
		name := p.Name()
		override := overrides[name]
		if p.Pkg() == c.pkg {
			override = c.argOverrides[p.Pos()]
		}
		if override.ambient != "" {
			continue
		}
		t := p.Type()
		valueField, presentField := optionalFields(t)
		optional := c.parameterOptional(p)
		if override := overrides[name].optional; override != nil {
			optional = *override
		}
		if !optional && valueField == "" {
			continue
		}
		value, exists := props[name]
		if exists {
			if actual := c.info.TypeOf(value); actual != nil && types.AssignableTo(actual, t) {
				continue
			}
		}
		if !exists {
			if implicit := c.implicitValue(p, overrides[name], pos); implicit != nil {
				props[name] = implicit
				continue
			}
		}
		if !exists && !optional {
			continue
		}
		tn := typeName(t)
		if !exists {
			props[name] = parse("*new(" + tn + ")")
			continue
		}
		if valueField != "" {
			prefix := ""
			if ptr, ok := types.Unalias(t).(*types.Pointer); ok {
				prefix = "&"
				tn = typeName(ptr.Elem())
			}
			props[name] = parse(prefix + tn + "{" + valueField + ":" + exprText(value) + "," + presentField + ":true}")
		} else if ptr, ok := types.Unalias(t).(*types.Pointer); ok {
			elem := typeName(ptr.Elem())
			props[name] = parse(fmt.Sprintf("func(value %s) *%s { return &value }(%s)", elem, elem, exprText(value)))
		} else {
			props[name] = value
		}
	}
	return nil
}

func optionalFields(t types.Type) (string, string) {
	t = types.Unalias(t)
	if ptr, ok := t.(*types.Pointer); ok {
		t = types.Unalias(ptr.Elem())
	}
	n, ok := t.(*types.Named)
	if !ok {
		return "", ""
	}
	name := n.Obj().Name()
	match := false
	for _, prefix := range []string{"Optional", "Opt", "Maybe", "Null"} {
		if strings.HasPrefix(name, prefix) {
			match = true
			break
		}
	}
	if !match {
		return "", ""
	}
	s, ok := n.Underlying().(*types.Struct)
	if !ok {
		return "", ""
	}
	value, present := "", ""
	for i := 0; i < s.NumFields(); i++ {
		f := s.Field(i)
		if f.Name() == "Value" {
			value = f.Name()
		}
		switch f.Name() {
		case "Valid", "Present", "IsValid", "IsPresent":
			if types.Identical(f.Type(), types.Typ[types.Bool]) {
				if present != "" {
					return "", ""
				}
				present = f.Name()
			}
		}
	}
	if value == "" || present == "" {
		return "", ""
	}
	return value, present
}
