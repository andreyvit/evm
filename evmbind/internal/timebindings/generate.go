//go:build ignore

package main

import (
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/timedata"
	"os"
)

func main() {
	b, err := evmbind.Generate("host/timecontext", "github.com/andreyvit/evm/evmbind/internal/timedata", "timebindings", timedata.Source, evmbind.Options{Selective: true, Methods: map[string][]string{"Context": {"Echo", "Pointer"}}})
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile("native_gen.go", b, 0644); err != nil {
		panic(err)
	}
}
