//go:build ignore

package main

import (
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/emptydata"
	"os"
)

func main() {
	b, e := evmbind.GenerateTypesWithOptions("sets", "github.com/andreyvit/evm/evmbind/internal/emptydata", "emptybindings", emptydata.Source, []string{"SetHolder"}, evmbind.Options{Methods: map[string][]string{"SetHolder": {"Has"}}})
	if e != nil {
		panic(e)
	}
	if e = os.WriteFile("native_gen.go", b, 0644); e != nil {
		panic(e)
	}
}
