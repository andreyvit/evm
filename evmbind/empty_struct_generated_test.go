//go:build !evmreflect

package evmbind_test

import (
	"bytes"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/emptybindings"
	"github.com/andreyvit/evm/evmbind/internal/emptydata"
	"os"
	"testing"
)

func TestGeneratedEmptyStructCollections(t *testing.T) {
	generated, err := evmbind.Generate("sets", "github.com/andreyvit/evm/evmbind/internal/emptydata", "emptybindings", emptydata.Source, evmbind.Options{Only: []string{"SetHolder"}, Methods: map[string][]string{"SetHolder": {"Has"}}})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile("internal/emptybindings/native_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, generated) {
		t.Fatal("regenerate empty struct bindings")
	}
	testNativeEmptyStructCollections(t, &emptybindings.Bindings)
}
