//go:build ignore

package main

import (
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/packagefunctionfixture"
	"os"
)

func main() {
	b, err := evmbind.Generate("functions", "github.com/andreyvit/evm/evmbind/internal/packagefunctionfixture", "packagefunctionbindings", packagefunctionfixture.Source, packagefunctionfixture.Options())
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile("native_gen.go", b, 0644); err != nil {
		panic(err)
	}
}
