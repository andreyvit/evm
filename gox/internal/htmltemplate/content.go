// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package htmltemplate

import (
	"fmt"
	"html/template"
	"reflect"
)

// Strings of content from a trusted source.
type (
	CSS      = template.CSS
	HTML     = template.HTML
	HTMLAttr = template.HTMLAttr
	JS       = template.JS
	JSStr    = template.JSStr
	URL      = template.URL
	Srcset   = template.Srcset
)

type contentType uint8

const (
	contentTypePlain contentType = iota
	contentTypeCSS
	contentTypeHTML
	contentTypeHTMLAttr
	contentTypeJS
	contentTypeJSStr
	contentTypeURL
	contentTypeSrcset
	// contentTypeUnsafe is used in attr.go for values that affect how
	// embedded content and network messages are formed, vetted,
	// or interpreted; or which credentials network messages carry.
	contentTypeUnsafe
)

// indirect returns the value, after dereferencing as many times
// as necessary to reach the base type (or nil).
func indirect(a any) any {
	if a == nil {
		return nil
	}
	if t := reflect.TypeOf(a); t.Kind() != reflect.Pointer {
		// Avoid creating a reflect.Value if it's not a pointer.
		return a
	}
	v := reflect.ValueOf(a)
	for v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	return v.Interface()
}

var (
	errorType       = reflect.TypeFor[error]()
	fmtStringerType = reflect.TypeFor[fmt.Stringer]()
)

// indirectToStringerOrError returns the value, after dereferencing as many times
// as necessary to reach the base type (or nil) or an implementation of fmt.Stringer
// or error.
func indirectToStringerOrError(a any) any {
	if a == nil {
		return nil
	}
	v := reflect.ValueOf(a)
	for !v.Type().Implements(fmtStringerType) && !v.Type().Implements(errorType) && v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	return v.Interface()
}

// stringify converts its arguments to a string and the type of the content.
// All pointers are dereferenced, as in the text/template package.
func stringify(args ...any) (string, contentType) {
	if len(args) == 1 {
		switch s := indirect(args[0]).(type) {
		case string:
			return s, contentTypePlain
		case CSS:
			return string(s), contentTypeCSS
		case HTML:
			return string(s), contentTypeHTML
		case HTMLAttr:
			return string(s), contentTypeHTMLAttr
		case JS:
			return string(s), contentTypeJS
		case JSStr:
			return string(s), contentTypeJSStr
		case URL:
			return string(s), contentTypeURL
		case Srcset:
			return string(s), contentTypeSrcset
		}
	}
	i := 0
	for _, arg := range args {
		// We skip untyped nil arguments for backward compatibility.
		// Without this they would be output as <nil>, escaped.
		// See issue 25875.
		if arg == nil {
			continue
		}

		args[i] = indirectToStringerOrError(arg)
		i++
	}
	return fmt.Sprint(args[:i]...), contentTypePlain
}
