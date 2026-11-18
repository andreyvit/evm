package gox_test

import (
	"strings"
	"testing"

	"github.com/andreyvit/evm/gox"
)

// Type errors report the filename exactly as the host supplied it.
func TestTypeErrorKeepsSuppliedFilename(t *testing.T) {
	source := `package views

import "github.com/andreyvit/evm/gox"

func Page(b *gox.Builder, name string) {
	<p>name.Missing()</p>
}
`
	for _, filename := range []string{"page.gox", "views/page.gox", "./views/page.gox", "/srv/views/page.gox", "views/shared/../page.gox"} {
		_, err := gox.Compile(filename, source)
		if err == nil {
			t.Fatalf("%s: compiled a call to a missing method", filename)
		}
		if want := filename + ":6:"; !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s: error %q does not start with %q", filename, err, want)
		}
	}
}
