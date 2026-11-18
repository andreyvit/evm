package gox

import (
	_ "embed"
	"fmt"
	"reflect"
	"slices"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmstd"
	ht "github.com/andreyvit/evm/gox/internal/htmltemplate"
	"github.com/andreyvit/evm/values"
)

//go:embed optional.go
var optionalSource string

var builderPointer = Schema().NativeType(reflect.TypeFor[*Builder]())
var trustedTypes = map[string]*values.Type{}

func init() {
	for _, name := range []string{"HTML", "HTMLAttr", "CSS", "JS", "JSStr", "URL", "Srcset"} {
		trustedTypes[name] = Schema().Type(name)
	}
}

// withBuiltinPackages adds what every template can import: the GoX runtime
// and the evmstd standard packages. A host package with the same path as a
// standard package replaces it.
func withBuiltinPackages(packages []evm.Package) []evm.Package {
	out := append(slices.Clone(packages), runtimePackage())
	supplied := make(map[string]bool, len(out))
	for _, p := range out {
		supplied[p.Path] = true
	}
	for _, p := range evmstd.Packages() {
		if !supplied[p.Path] {
			out = append(out, p)
		}
	}
	return out
}

func runtimePackage() evm.Package {
	p := Schema().Bind(nil)
	p.Source = optionalSource
	p.StructValues = map[string]func(string, []*values.Shape) *values.CellStruct{
		"Optional": func(name string, args []*values.Shape) *values.CellStruct {
			if len(args) != 1 {
				return nil
			}
			return values.CompactOptional(name, args[0])
		},
	}
	p.Types = append(slices.Clone(p.Types), trustedSchema().Package().Types...)
	add := func(name string, params []*values.Type, fn func(*Builder, *evm.Call) error) {
		p.Functions = append(p.Functions, evm.ExternalFunction{Name: name, Signature: evm.Signature{Params: append([]*values.Type{builderPointer}, params...)}, Call: func(c *evm.Call) error {
			b, err := builderForCall(c, c.Argument(0))
			if err != nil {
				return err
			}
			return fn(b, c)
		}})
	}
	add("Step", []*values.Type{values.IntType}, func(b *Builder, c *evm.Call) error { return b.step(int(c.Argument(1).AsInt())) })
	add("Value", []*values.Type{values.IntType, values.AnyType}, func(b *Builder, c *evm.Call) error {
		v, err := nativeValue(c, 2)
		if err != nil {
			return err
		}
		return b.value(int(c.Argument(1).AsInt()), v)
	})
	add("String", []*values.Type{values.IntType, values.StringType}, func(b *Builder, c *evm.Call) error {
		return b.stringValue(int(c.Argument(1).AsInt()), c.Argument(2).AsString())
	})
	add("Int", []*values.Type{values.IntType, values.IntType}, func(b *Builder, c *evm.Call) error { return b.value(int(c.Argument(1).AsInt()), c.Argument(2).AsInt()) })
	add("Bool", []*values.Type{values.IntType, values.Boolean}, func(b *Builder, c *evm.Call) error { return b.value(int(c.Argument(1).AsInt()), c.Argument(2).R != 0) })
	add("Class", []*values.Type{values.StringType}, func(b *Builder, c *evm.Call) error { return b.Class(c.Argument(1).AsString()) })
	add("Attr", []*values.Type{values.StringType, values.AnyType}, func(b *Builder, c *evm.Call) error {
		v, err := nativeValue(c, 2)
		if err != nil {
			return err
		}
		return b.Attr(c.Argument(1).AsString(), v)
	})
	add("AttrOrOmit", []*values.Type{values.StringType, values.AnyType}, func(b *Builder, c *evm.Call) error {
		v, err := nativeValue(c, 2)
		if err != nil {
			return err
		}
		return b.AttrOrOmit(c.Argument(1).AsString(), v)
	})
	add("Data", []*values.Type{values.StringType, values.StringType}, func(b *Builder, c *evm.Call) error { return b.Data(c.Argument(1).AsString(), c.Argument(2).AsString()) })
	add("BuilderText", []*values.Type{values.StringType}, func(b *Builder, c *evm.Call) error { return b.Text(c.Argument(1).AsString()) })
	for _, name := range []string{"Class", "Attr", "AttrOrOmit", "Data", "BuilderText"} {
		for _, f := range p.Functions {
			if f.Name == name {
				params := append([]*values.Type(nil), f.Signature.Params[1:]...)
				methodName := name
				if name == "BuilderText" {
					methodName = "Text"
				}
				method := evm.ExternalMethod{Name: methodName, Receiver: builderPointer, Signature: evm.Signature{Params: params}}
				method.Call = func(c *evm.Call) error {
					b, err := builderForCall(c, c.Argument(0))
					if err != nil {
						return err
					}
					switch name {
					case "Class":
						return b.Class(c.Argument(1).AsString())
					case "Data":
						return b.Data(c.Argument(1).AsString(), c.Argument(2).AsString())
					case "BuilderText":
						return b.Text(c.Argument(1).AsString())
					default:
						v, err := nativeValue(c, 2)
						if err != nil {
							return err
						}
						if name == "AttrOrOmit" {
							return b.AttrOrOmit(c.Argument(1).AsString(), v)
						}
						return b.Attr(c.Argument(1).AsString(), v)
					}
				}
				p.Methods = append(p.Methods, method)
			}
		}
	}
	p.Methods = append(p.Methods, evm.ExternalMethod{Name: "HTML", Receiver: builderPointer, Signature: evm.Signature{Params: []*values.Type{trustedTypes["HTML"]}}, Call: func(c *evm.Call) error {
		b, err := builderForCall(c, c.Argument(0))
		if err != nil {
			return err
		}
		return b.HTML(HTML(c.Argument(1).AsString()))
	}})
	p.Methods = append(p.Methods,
		evm.ExternalMethod{Name: "Open", Receiver: builderPointer, Signature: evm.Signature{Params: []*values.Type{values.StringType}}, Call: func(c *evm.Call) error {
			b, err := builderForCall(c, c.Argument(0))
			if err != nil {
				return err
			}
			return b.Open(c.Argument(1).AsString())
		}},
		evm.ExternalMethod{Name: "Close", Receiver: builderPointer, Call: func(c *evm.Call) error {
			b, err := builderForCall(c, c.Argument(0))
			if err != nil {
				return err
			}
			return b.Close()
		}},
	)
	// Small side-effecting calls read arguments directly from guest registers.
	// Keep the normal callbacks for indirect and deferred invocations.
	direct := func(fn func(*Builder, values.Value) error) evm.DirectFunc {
		return func(c *evm.Call, x, y values.Value) (values.Value, error) {
			b, err := builderForCall(c, x)
			if err != nil {
				return values.Value{}, err
			}
			return values.Value{}, fn(b, y)
		}
	}
	callbacks := map[string]evm.DirectFunc{
		"Step":  direct(func(b *Builder, v values.Value) error { return b.step(int(v.AsInt())) }),
		"Class": direct(func(b *Builder, v values.Value) error { return b.Class(v.AsString()) }),
		"Text":  direct(func(b *Builder, v values.Value) error { return b.Text(v.AsString()) }),
		"HTML":  direct(func(b *Builder, v values.Value) error { return b.HTML(HTML(v.AsString())) }),
		"Open":  direct(func(b *Builder, v values.Value) error { return b.Open(v.AsString()) }),
		"Close": direct(func(b *Builder, _ values.Value) error { return b.Close() }),
	}
	for i := range p.Functions {
		f := &p.Functions[i]
		if f.Name == "Text" || f.Name == "TrustedHTML" {
			trusted := f.Name == "TrustedHTML"
			fn := func(_ *evm.Call, x, _ values.Value) (values.Value, error) {
				return contentCell.pack(Content{x.AsString(), trusted}), nil
			}
			f.Call = func(c *evm.Call) error { v, err := fn(c, c.Argument(0), values.Value{}); c.SetResult(0, v); return err }
			f.Direct = evm.DirectBinding{Op: evm.HostCallOpcode(false, []values.Repr{values.Pair}, values.Pair), Call: fn}
			continue
		}
		if fn := callbacks[f.Name]; fn != nil && f.Name != "Text" {
			f.Direct = evm.DirectBinding{Op: evm.HostCallOpcode(false, []values.Repr{values.Pointer, f.Signature.Params[1].Repr()}, 0), Call: fn}
		}
	}
	for i := range p.Methods {
		m := &p.Methods[i]
		if fn := callbacks[m.Name]; fn != nil {
			var args []values.Repr
			for _, t := range m.Signature.Params {
				args = append(args, t.Repr())
			}
			m.Direct = evm.DirectBinding{Op: evm.HostCallOpcode(true, args, 0), Call: fn}
		}
	}
	return p
}
func nativeValue(c *evm.Call, index int) (any, error) {
	v := c.Argument(index)
	shape, raw, err := c.Program().UnpackInterface(v)
	if err != nil || shape == nil {
		return nil, err
	}
	typ := shape.Type
	if typ == Schema().NativeType(reflect.TypeFor[Content]()) {
		text := contentCell.text(raw)
		if raw.Representation() == contentCell.trusted {
			return ht.HTML(text), nil
		}
		return text, nil
	}
	switch typ {
	case values.StringType:
		return raw.AsString(), nil
	case values.Boolean:
		return raw.R != 0, nil
	case values.IntType, values.Int64:
		return raw.AsInt(), nil
	case values.UintType, values.Uint64:
		return raw.R, nil
	case values.Float64:
		return raw.AsFloat(), nil
	}
	for name, t := range trustedTypes {
		if typ == t {
			s := raw.AsString()
			switch name {
			case "HTML":
				return ht.HTML(s), nil
			case "HTMLAttr":
				return ht.HTMLAttr(s), nil
			case "CSS":
				return ht.CSS(s), nil
			case "JS":
				return ht.JS(s), nil
			case "JSStr":
				return ht.JSStr(s), nil
			case "URL":
				return ht.URL(s), nil
			case "Srcset":
				return ht.Srcset(s), nil
			}
		}
	}
	return c.JSONValue(v, 16<<20)
}

func builderForCall(c *evm.Call, v values.Value) (*Builder, error) {
	b := (*Builder)(v.P)
	if b == nil {
		return nil, fmt.Errorf("nil rendering builder")
	}
	if b.program != c.Program() {
		return nil, fmt.Errorf("builder belongs to another rendering program")
	}
	return b, nil
}
