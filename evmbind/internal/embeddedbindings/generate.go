//go:build ignore

package main

import (
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/embeddedfixture"
	"os"
)

func main() {
	b, err := evmbind.Generate("page", "github.com/andreyvit/evm/evmbind/internal/embeddedfixture", "embeddedbindings", embeddedfixture.Source, evmbind.Options{})
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile("native_gen.go", b, 0644); err != nil {
		panic(err)
	}
}
