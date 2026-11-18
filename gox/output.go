package gox

import (
	"fmt"
	"go/ast"
	"go/types"
)

// typeName spells a type for a message: packages by import path, the
// template's own types unqualified, the empty interface as any.
func (c *compiler) typeName(t types.Type) string {
	if iface, ok := types.Unalias(t).(*types.Interface); ok && iface.Empty() {
		return "any"
	}
	return types.TypeString(t, func(p *types.Package) string {
		if p == c.pkg {
			return ""
		}
		return p.Path()
	})
}

// printable reports the types every position writes as they are: strings,
// including named strings such as the trusted content types, and gox.Content.
func printable(t types.Type) bool {
	if basic, ok := t.Underlying().(*types.Basic); ok && basic.Info()&types.IsString != 0 {
		return true
	}
	named, ok := types.Unalias(t).(*types.Named)
	return ok && named.Obj().Name() == "Content" && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "github.com/andreyvit/evm/gox"
}

// outputPosition is where a dynamic value lands. It decides which types the
// value may have. GoX never chooses a display form for a value: text is for
// people, so it takes strings only; attributes and CSS are for the browser,
// so they also take the numbers a browser reads.
type outputPosition uint8

const (
	// outputText is HTML text or a plain-text template: for a reader.
	outputText outputPosition = iota
	// outputAttribute is a whole attribute value, name={value}.
	outputAttribute
	// outputAttributePart is a value interpolated into a quoted attribute,
	// name="a {value} b".
	outputAttributePart
	// outputCSS is a style body or a style attribute.
	outputCSS
)

// builtinKind reports whether t is one of Go's own types of the given kinds.
// A named type such as Money says nothing about how to write it.
func builtinKind(t types.Type, info types.BasicInfo) bool {
	basic, ok := types.Unalias(t).(*types.Basic)
	return ok && basic.Info()&info != 0
}

// character reports the builtin integer types that usually hold a character
// or an address instead of a count or an identifier: byte, uint8, rune and
// uintptr. Go keeps rune apart from int32 by name only.
func character(t types.Type) bool {
	basic, ok := types.Unalias(t).(*types.Basic)
	return ok && (basic.Kind() == types.Uint8 || basic.Kind() == types.Uintptr || basic.Name() == "rune")
}

// attributeInteger reports the integers an attribute writes as a number.
func attributeInteger(t types.Type) bool {
	return builtinKind(t, types.IsInteger) && !character(t)
}

// direct reports whether a value of type t is written as it is at position p.
func direct(t types.Type, p outputPosition) bool {
	switch p {
	case outputAttribute:
		// colspan={n} and hidden={flag}
		return printable(t) || builtinKind(t, types.IsBoolean) || attributeInteger(t)
	case outputAttributePart:
		// href="/items/{id}"
		return printable(t) || attributeInteger(t)
	case outputCSS:
		return printable(t) || builtinKind(t, types.IsInteger|types.IsFloat)
	}
	return printable(t)
}

// checkOutput reports a value whose type position p does not take.
func (c *compiler) checkOutput(expr ast.Expr, t types.Type, p outputPosition) error {
	if t == nil {
		return nil // an earlier error is reported by the checker
	}
	if basic, ok := t.(*types.Basic); ok && basic.Kind() == types.UntypedNil {
		return c.error(expr.Pos(), "cannot output nil")
	}
	if basic, ok := t.(*types.Basic); ok && basic.Info()&types.IsUntyped != 0 {
		t = types.Default(t)
	}
	if direct(t, p) {
		return nil
	}
	name := c.typeName(t)
	var message string
	switch p {
	case outputAttribute:
		message = "cannot use a value of type %s as an attribute value: an attribute takes a string, a bool or an integer"
	case outputAttributePart:
		message = "cannot interpolate a value of type %s into an attribute: an attribute takes strings and integers"
	case outputCSS:
		message = "cannot use a value of type %s in CSS: CSS takes strings, integers and floats"
	default:
		message = "cannot output a value of type %s: text takes strings and trusted content"
	}
	message = fmt.Sprintf(message, name)
	if (p == outputAttribute || p == outputAttributePart) && character(t) {
		return c.error(expr.Pos(), message+"; "+name+" may be a character or a number, so convert it to a string or to int")
	}
	return c.error(expr.Pos(), message+"; convert it")
}
