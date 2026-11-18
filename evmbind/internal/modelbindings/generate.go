//go:build ignore

package main

import (
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/modeldata"
	"github.com/andreyvit/evm/evmbind/internal/modelhost"
	"os"
)

func main() {
	_, dep, err := modeldata.Declarations()
	if err != nil {
		panic(err)
	}
	b, err := evmbind.Generate("host", "github.com/andreyvit/evm/evmbind/internal/modelhost", "modelbindings", modelhost.Source, evmbind.Options{Imports: map[string]*evmbind.Schema{"github.com/andreyvit/evm/evmbind/internal/modeldata": dep}})
	if err != nil {
		panic(err)
	}
	if err = os.WriteFile("native_gen.go", b, 0644); err != nil {
		panic(err)
	}
}
