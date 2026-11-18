package gox_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/andreyvit/evm/gox"
)

// Templates import the evmstd standard packages without host setup, and the
// formatter knows their import paths.
func TestTemplatesImportStandardPackages(t *testing.T) {
	const source = `package views

import (
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/andreyvit/evm/gox"
)

func Page(b *gox.Builder, tags []string, joined time.Time, query string) {
	sorted := slices.Sorted(slices.Values(tags))
	<p title={strings.Join(sorted, ",")}>
		joined.Add(36 * time.Hour).Weekday().String()
		" "
		joined.Month().String()
		" "
		url.QueryEscape(query)
		if (utf8.RuneCountInString(query) < len(query)) {
			" non-ASCII"
		}
	</p>
}
`
	tmpl, err := gox.Compile("page.gox", source)
	if err != nil {
		t.Fatal(err)
	}
	program, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	html, err := program.RenderNative(context.Background(), []any{[]string{"b", "a"}, time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC), "café au lait"}, gox.RenderOptions{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := `<p title="a,b">Sunday March caf%C3%A9&#43;au&#43;lait non-ASCII</p>`; html != want {
		t.Errorf("got  %s\nwant %s", html, want)
	}

	formatted, err := gox.FormatSource("page.gox", source)
	if err != nil {
		t.Fatal(err)
	}
	if formatted != source {
		t.Errorf("source is not formatted:\n%s", formatted)
	}
	stripped := "package views\n\nfunc Page(b *gox.Builder, t time.Time, s []string) {\n\t<p>url.QueryEscape(strings.Join(slices.Sorted(slices.Values(s)), \",\"))</p>\n}\n"
	formatted, err = gox.FormatSource("page.gox", stripped)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"net/url"`, `"slices"`, `"strings"`, `"time"`, `"github.com/andreyvit/evm/gox"`} {
		if !strings.Contains(formatted, want) {
			t.Errorf("formatter did not add import %s:\n%s", want, formatted)
		}
	}
}
