//go:build !evmreflect

package evmbind_test

import (
	"bytes"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/modeldata"
	"github.com/andreyvit/evm/evmbind/internal/modelfieldbindings"
	"github.com/andreyvit/evm/evmbind/internal/modelfielddata"
	"os"
	"testing"
)

func TestGeneratedNativeModelPointerFields(t *testing.T) {
	_, dep, err := modeldata.Declarations()
	if err != nil {
		t.Fatal(err)
	}
	generated, err := evmbind.GenerateTypesWithOptions("ctx", "github.com/andreyvit/evm/evmbind/internal/modelfielddata", "modelfieldbindings", modelfielddata.Source, nil, evmbind.Options{Methods: map[string][]string{"Context": {"Clone"}}, Imports: map[string]*evmbind.Schema{"github.com/andreyvit/evm/evmbind/internal/modeldata": dep}})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile("internal/modelfieldbindings/native_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, generated) {
		t.Fatal("regenerate model field bindings")
	}
	testNativeModelPointerFields(t, &modelfieldbindings.Bindings)
}
