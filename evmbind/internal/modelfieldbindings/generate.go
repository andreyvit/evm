//go:build ignore

package main

import (
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/modeldata"
	"github.com/andreyvit/evm/evmbind/internal/modelfielddata"
	"os"
)

func main() {
	_, dep, err := modeldata.Declarations()
	if err != nil {
		panic(err)
	}
	b, err := evmbind.GenerateTypesWithOptions("ctx", "github.com/andreyvit/evm/evmbind/internal/modelfielddata", "modelfieldbindings", modelfielddata.Source, nil, evmbind.Options{Methods: map[string][]string{"Context": {"Clone"}}, Imports: map[string]*evmbind.Schema{"github.com/andreyvit/evm/evmbind/internal/modeldata": dep}})
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile("native_gen.go", b, 0644); err != nil {
		panic(err)
	}
}
