//go:build ignore

package main

import (
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/importconsumer"
	"github.com/andreyvit/evm/evmbind/internal/importfixture"
	"os"
	"reflect"
)

func main() {
	dep, err := evmbind.NewTypes("data", importfixture.Source, nil, evmbind.Options{Instances: []evmbind.Instance{{Type: "Box", Native: reflect.TypeFor[importfixture.Box[importfixture.Permission]](), Args: []reflect.Type{reflect.TypeFor[importfixture.Permission]()}}}, Types: []reflect.Type{reflect.TypeFor[importfixture.Permission](), reflect.TypeFor[importfixture.Child]()}})
	if err != nil {
		panic(err)
	}
	b, err := evmbind.GenerateWithImports("host", "github.com/andreyvit/evm/evmbind/internal/importconsumer", "importbindings", importconsumer.Source, map[string]*evmbind.Schema{"github.com/andreyvit/evm/evmbind/internal/importfixture": dep})
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile("native_gen.go", b, 0644); err != nil {
		panic(err)
	}
}
