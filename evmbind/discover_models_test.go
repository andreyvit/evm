package evmbind_test

import (
	"reflect"
	"testing"

	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/modeldata"
	"github.com/andreyvit/evm/evmcompiler"
)

func TestDiscoverUsesImportedModelRows(t *testing.T) {
	models, _, err := modeldata.Declarations()
	if err != nil {
		t.Fatal(err)
	}
	dep, err := evmbind.New("model", modeldata.Source, evmbind.Options{Selective: true, Models: &models, Types: []reflect.Type{reflect.TypeFor[BoundModel]()}})
	if err != nil {
		t.Fatal(err)
	}
	_, packages, err := evmbind.Discover("api", evmbind.Discovery{Options: evmbind.Options{
		Models:    &models,
		Imports:   map[string]*evmbind.Schema{reflect.TypeFor[BoundModel]().PkgPath(): dep},
		Functions: map[string]any{"Row": func() *BoundModel { return nil }},
	}})
	if err != nil {
		t.Fatal(err)
	}
	packages = append(packages, models, dep.Bind(nil))
	if _, err := evmcompiler.Compile("test.go", `import "api";func Run() string { return api.Row().name }`, packages...); err != nil {
		t.Fatal(err)
	}
}
