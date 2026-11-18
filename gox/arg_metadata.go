package gox

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"strconv"
	"strings"
)

type argOverride struct {
	optional   *bool
	implicit   *bool
	legacyName string
	ambient    string
}

// InputName is the key used by legacy string-keyed argument adapters.
// XML component calls always use Name.
func (p Parameter) InputName() string {
	if p.LegacyName != "" {
		return p.LegacyName
	}
	return p.Name
}

func (c *compiler) collectArgDirectives(f *ast.FuncDecl) error {
	if f.Doc == nil {
		return nil
	}
	params := make(map[string]*ast.Ident)
	for _, field := range f.Type.Params.List {
		for _, name := range field.Names {
			params[name.Name] = name
		}
	}
	seen := make(map[string]bool)
	for _, comment := range f.Doc.List {
		if !strings.HasPrefix(comment.Text, "//gox:arg") {
			continue
		}
		text := strings.TrimPrefix(comment.Text, "//gox:arg")
		var scan scanner.Scanner
		var scanErr error
		scan.Init(token.NewFileSet().AddFile("directive", -1, len(text)), []byte(text), func(_ token.Position, msg string) { scanErr = fmt.Errorf("%s", msg) }, 0)
		var pending bool
		var savedTok token.Token
		var savedLit string
		next := func() (token.Pos, token.Token, string) {
			if pending {
				pending = false
				return token.NoPos, savedTok, savedLit
			}
			return scan.Scan()
		}
		_, tok, name := next()
		if tok != token.IDENT || params[name] == nil {
			return c.error(comment.Pos(), "gox:arg requires a declared parameter name")
		}
		if seen[name] {
			return c.error(comment.Pos(), "duplicate gox:arg for "+name)
		}
		seen[name] = true
		override := argOverride{}
		options := make(map[string]bool)
		for {
			_, tok, key := next()
			if tok == token.EOF {
				break
			}
			if tok == token.SEMICOLON {
				if key != "\n" {
					return c.error(comment.Pos(), "unexpected semicolon in gox:arg")
				}
				break
			}
			if tok != token.IDENT || options[key] {
				return c.error(comment.Pos(), "invalid or duplicate gox:arg option")
			}
			options[key] = true
			_, tok, literal := next()
			value := "true"
			if tok == token.ASSIGN {
				_, tok, value = next()
			} else if key == "optional" || key == "implicit" {
				pending = true
				savedTok = tok
				savedLit = literal
				tok = token.IDENT
			} else {
				return c.error(comment.Pos(), "gox:arg option requires =")
			}
			switch key {
			case "optional", "implicit":
				if tok != token.IDENT || (value != "true" && value != "false") {
					return c.error(comment.Pos(), "gox:arg "+key+" must be true or false")
				}
				optional := value == "true"
				if key == "optional" {
					override.optional = &optional
				} else {
					override.implicit = &optional
				}
			case "legacyName", "ambient":
				if tok != token.STRING {
					return c.error(comment.Pos(), "gox:arg "+key+" must be a quoted string")
				}
				decoded, err := strconv.Unquote(value)
				if err != nil || decoded == "" {
					return c.error(comment.Pos(), "gox:arg "+key+" must be nonempty")
				}
				if key == "ambient" {
					override.ambient = decoded
				} else {
					override.legacyName = decoded
				}
			default:
				return c.error(comment.Pos(), "unknown gox:arg option "+key)
			}
		}
		if name == findBuilder(f.Type) && (override.optional != nil || override.legacyName != "" || override.ambient != "") {
			return c.error(comment.Pos(), "gox:arg cannot override the builder")
		}
		if override.ambient != "" && (override.optional != nil || override.implicit != nil || override.legacyName != "") {
			return c.error(comment.Pos(), "ambient binding cannot combine with optional, implicit or legacyName")
		}
		if scanErr != nil {
			return c.error(comment.Pos(), scanErr.Error())
		}
		if len(options) == 0 {
			return c.error(comment.Pos(), "gox:arg requires an option")
		}
		if c.argOverrides == nil {
			c.argOverrides = make(map[token.Pos]argOverride)
		}
		c.argOverrides[params[name].Pos()] = override
	}
	keys := make(map[string]string)
	for name, id := range params {
		key := name
		if legacy := c.argOverrides[id.Pos()].legacyName; legacy != "" {
			key = legacy
		}
		if other, exists := keys[key]; exists {
			return c.error(id.Pos(), "duplicate legacy argument name for "+other+" and "+name)
		}
		keys[key] = name
	}
	return nil
}

func (c *compiler) parameterOptional(p *types.Var) bool {
	value, _ := optionalFields(p.Type())
	optional := value != ""
	if p.Pkg() == c.pkg {
		if override := c.argOverrides[p.Pos()].optional; override != nil {
			optional = *override
		}
	}
	return optional
}

func (c *compiler) captureParameters() {
	c.template.FunctionParameters = make(map[string][]Parameter)
	for _, file := range c.syntax {
		for _, decl := range file.Decls {
			f, ok := decl.(*ast.FuncDecl)
			if !ok || f.Recv != nil {
				continue
			}
			builder := findBuilder(f.Type)
			var params []Parameter
			for _, field := range f.Type.Params.List {
				for _, id := range field.Names {
					if id.Name == builder {
						continue
					}
					param := Parameter{Name: id.Name, Type: nodeString(field.Type), LegacyName: c.argOverrides[id.Pos()].legacyName, Ambient: c.argOverrides[id.Pos()].ambient}
					if variable, ok := c.info.Defs[id].(*types.Var); ok {
						param.Optional = c.parameterOptional(variable)
						param.Implicit = c.parameterImplicit(variable, argOverride{})
					}
					params = append(params, param)
				}
			}
			c.template.FunctionParameters[f.Name.Name] = params
		}
	}
	c.template.Parameters = append([]Parameter(nil), c.template.FunctionParameters[c.template.Entry]...)
}

// Imported declarations retain comments and names even when their bodies run natively.
func (c *compiler) importedArgDirectives(name string, sig *types.Signature) (map[string]argOverride, error) {
	if sig.Params().Len() == 0 || sig.Params().At(0).Pkg() == nil {
		return nil, nil
	}
	path := sig.Params().At(0).Pkg().Path()
	_, name, qualified := strings.Cut(name, ".")
	if !qualified {
		return nil, nil
	}
	cacheKey := path + "." + name
	if cached, ok := c.importedOverrides[cacheKey]; ok {
		return cached, nil
	}
	if c.importedOverrides == nil {
		c.importedOverrides = make(map[string]map[string]argOverride)
	}
	c.importedOverrides[cacheKey] = nil
	for _, pkg := range c.packages {
		if pkg.Path != path {
			continue
		}
		for _, fn := range pkg.Functions {
			if fn.Name != name || !strings.Contains(fn.Doc, "//gox:arg") {
				continue
			}
			params := make([]string, sig.Params().Len())
			for i := range params {
				typ := "any"
				if types.TypeString(sig.Params().At(i).Type(), func(p *types.Package) string { return p.Name() }) == "*gox.Builder" {
					typ = "*gox.Builder"
				}
				params[i] = sig.Params().At(i).Name() + " " + typ
			}
			files := token.NewFileSet()
			file, err := parser.ParseFile(files, path+"."+name, "package imported\n"+strings.TrimSpace(fn.Doc)+"\nfunc "+name+"("+strings.Join(params, ",")+")", parser.ParseComments)
			if err != nil {
				return nil, err
			}
			decl := file.Decls[0].(*ast.FuncDecl)
			metadata := &compiler{files: files}
			if err := metadata.collectArgDirectives(decl); err != nil {
				return nil, err
			}
			result := make(map[string]argOverride)
			for _, field := range decl.Type.Params.List {
				for _, id := range field.Names {
					result[id.Name] = metadata.argOverrides[id.Pos()]
				}
			}
			c.importedOverrides[cacheKey] = result
			return result, nil
		}
	}
	return nil, nil
}

func (c *compiler) validateAmbientDeclarations() error {
	for _, file := range c.syntax {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			obj, ok := c.info.Defs[fn.Name].(*types.Func)
			if !ok {
				continue
			}
			sig := obj.Type().(*types.Signature)
			count := 0
			var bound *types.Var
			for i := 0; i < sig.Params().Len(); i++ {
				p := sig.Params().At(i)
				tn := types.TypeString(p.Type(), func(p *types.Package) string { return p.Path() })
				if c.argOverrides[p.Pos()].ambient == "" && (tn == "*evmruntime.Ambient" || tn == "*github.com/andreyvit/evm/evmruntime.Ambient") {
					count++
				}
				if c.argOverrides[p.Pos()].ambient != "" {
					bound = p
				}
			}
			if bound != nil && count != 1 {
				return c.error(bound.Pos(), "ambient-bound arguments require a single Ambient parameter")
			}
		}
	}
	return nil
}
