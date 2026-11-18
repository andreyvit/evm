//go:build ignore

package main

import (
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/interfaceconsumer"
	"github.com/andreyvit/evm/evmbind/internal/interfacedep"
	"os"
	"reflect"
)

func main() {
	dep, err := evmbind.NewTypes("domain", interfacedep.Source, nil, evmbind.Options{Types: []reflect.Type{reflect.TypeFor[interfacedep.Named](), reflect.TypeFor[interfacedep.NamedValue]()}, Methods: map[string][]string{"NamedValue": {"Label"}}})
	if err != nil {
		panic(err)
	}
	b, err := evmbind.GenerateWithImports("host", "github.com/andreyvit/evm/evmbind/internal/interfaceconsumer", "interfacebindings", interfaceconsumer.Source, map[string]*evmbind.Schema{"github.com/andreyvit/evm/evmbind/internal/interfacedep": dep})
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile("native_gen.go", b, 0644); err != nil {
		panic(err)
	}
}
