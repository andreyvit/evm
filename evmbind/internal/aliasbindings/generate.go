//go:build ignore

package main

import (
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/aliasconsumer"
	"github.com/andreyvit/evm/evmbind/internal/importfixture"
	"os"
	"reflect"
)

func main() {
	dep, err := evmbind.New("bh", importfixture.Source, evmbind.Options{Selective: true, Instances: []evmbind.Instance{{Type: "Box", Native: reflect.TypeFor[importfixture.Box[importfixture.Permission]](), Args: []reflect.Type{reflect.TypeFor[importfixture.Permission]()}}}, Types: []reflect.Type{reflect.TypeFor[importfixture.Child](), reflect.TypeFor[importfixture.Permission]()}})
	if err != nil {
		panic(err)
	}
	b, err := evmbind.Generate("bh", "github.com/andreyvit/evm/evmbind/internal/aliasconsumer", "aliasbindings", aliasconsumer.Source, evmbind.Options{Imports: map[string]*evmbind.Schema{"github.com/andreyvit/evm/evmbind/internal/importfixture": dep}})
	if err != nil {
		panic(err)
	}
	if err = os.WriteFile("native_gen.go", b, 0644); err != nil {
		panic(err)
	}
}
