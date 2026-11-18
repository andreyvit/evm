package gox_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/andreyvit/evm/gox"
)

// The example and benchmark templates are what readers copy, so they stay in
// the formatter's own layout.
func TestTestdataTemplatesAreFormatted(t *testing.T) {
	names, err := filepath.Glob("testdata/*/*.gox")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) < 8 {
		t.Fatalf("found only %d templates", len(names))
	}
	for _, name := range names {
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		formatted, err := gox.FormatSource(name, string(source))
		if err != nil {
			t.Errorf("%s: %v", name, err)
		} else if formatted != string(source) {
			t.Errorf("%s is not formatted; run: go run ./cmd/gox fmt -w gox/testdata", name)
		}
	}
}
