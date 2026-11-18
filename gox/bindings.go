package gox

import (
	"embed"
	"reflect"
	"sync"

	"github.com/andreyvit/evm/evmruntime"

	"github.com/andreyvit/evm/evmbind"
	ht "github.com/andreyvit/evm/gox/internal/htmltemplate"
	"github.com/andreyvit/evm/values"
)

//go:embed builder.go template.go content.go classlist.go
var bindingSource embed.FS

var trustedSchema = sync.OnceValue(func() *evmbind.Schema {
	names := []string{"HTML", "HTMLAttr", "CSS", "JS", "JSStr", "URL", "Srcset"}
	trusted, err := evmbind.NewTypes("github.com/andreyvit/evm/gox", ht.BindingSource, names, evmbind.Options{NativePackage: "html/template", Types: []reflect.Type{
		reflect.TypeFor[HTML](), reflect.TypeFor[HTMLAttr](), reflect.TypeFor[CSS](), reflect.TypeFor[JS](), reflect.TypeFor[JSStr](), reflect.TypeFor[URL](), reflect.TypeFor[Srcset](),
	}})
	if err != nil {
		panic(err)
	}
	return trusted
})

var nativeSchema = sync.OnceValue(func() *evmbind.Schema {
	names := []string{"HTML", "HTMLAttr", "CSS", "JS", "JSStr", "URL", "Srcset"}
	schema, err := evmbind.NewTypes("github.com/andreyvit/evm/gox", bindingSource, append([]string{"Builder", "Content", "ClassList"}, names...), evmbind.Options{
		NativePackage: "github.com/andreyvit/evm/gox",
		CellStructs:   map[string]*values.CellStruct{"Content": contentCell.CellStruct},
		Types:         []reflect.Type{reflect.TypeFor[Builder](), reflect.TypeFor[Content](), reflect.TypeFor[ClassList]()},
		Functions:     map[string]any{"Text": Text, "TrustedHTML": TrustedHTML},
		Fields:        map[string][]string{"Builder": {}, "ClassList": {}},
		Methods:       map[string][]string{"ClassList": {"Add", "String"}},
		Imports:       map[string]*evmbind.Schema{"html/template": trustedSchema()},
	})
	if err != nil {
		panic(err)
	}
	return schema
})

// Schema shares GoX's native builder and trusted-content types with host bindings.
// Builder fields remain private; the runtime package supplies its render methods.
func Schema() *evmbind.Schema { return nativeSchema() }

// TrustedContentSchema shares GoX's supported html/template content types with
// host bindings: HTML, HTMLAttr, CSS, JS, JSStr, URL and Srcset.
func TrustedContentSchema() *evmbind.Schema { return trustedSchema() }

var ambientSchema = sync.OnceValue(func() *evmbind.Schema {
	schema, err := evmbind.NewTypes("github.com/andreyvit/evm/evmruntime", evmruntime.BindingSource, []string{"Ambient"}, evmbind.Options{
		NativePackage: "github.com/andreyvit/evm/evmruntime",
		Methods:       map[string][]string{"Ambient": {"Get", "CloneWith", "Values"}},
		Types:         []reflect.Type{reflect.TypeFor[evmruntime.Ambient]()},
		Fields:        map[string][]string{"Ambient": {}},
	})
	if err != nil {
		panic(err)
	}
	return schema
})

// AmbientSchema shares native inherited context with host rendering adapters.
func AmbientSchema() *evmbind.Schema { return ambientSchema() }
