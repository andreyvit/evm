package main

import (
	"os"
	"reflect"

	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/methodfixture"
	"github.com/andreyvit/evm/kvo"
)

func main() {
	b, err := evmbind.GenerateMethods("methodbindings", methodfixture.Methods())
	if err != nil {
		panic(err)
	}
	if err = os.WriteFile("../methodbindings/bindings_gen.go", b, 0644); err != nil {
		panic(err)
	}
	b = kvo.Bind(reflect.TypeFor[methodfixture.Row](), methodfixture.Model).Generate("methodfixture", "Static")
	if err = os.WriteFile("native_gen.go", b, 0644); err != nil {
		panic(err)
	}
}
