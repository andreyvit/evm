//go:build ignore

package main

import (
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/omittedfixture"
	"os"
)

func main() {
	b, err := evmbind.GenerateWithOptions("page", "github.com/andreyvit/evm/evmbind/internal/omittedfixture", "omittedbindings", omittedfixture.Source, evmbind.Options{OmitFields: map[string][]string{"Page": {"Private"}}})
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile("native_gen.go", b, 0644); err != nil {
		panic(err)
	}
}
