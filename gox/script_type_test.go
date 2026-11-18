package gox_test

import (
	"html/template"
	"strings"
	"testing"

	"github.com/andreyvit/evm/values"
)

func TestScriptTypeContextMatchesHTMLTemplate(t *testing.T) {
	for _, mime := range []string{"text/x-bubblehouse-comment", "text/plain", "application/json", "text/javascript", "module", ""} {
		t.Run(mime, func(t *testing.T) {
			source := `package plugin
func Page(b *gox.Builder, value string) {
<script type="` + mime + `">value="{=value}"</script>
}`
			reference := template.Must(template.New("reference").Parse(`<script type="` + mime + `">value="{{.}}"</script>`))
			for _, value := range []string{`market<"&`, `</script><script>alert(1)</script>`, "a\nb"} {
				var want strings.Builder
				if err := reference.Execute(&want, value); err != nil {
					t.Fatal(err)
				}
				got := render(t, source, values.String(value))
				if got != want.String() {
					t.Fatalf("%q: got %q, want %q", value, got, want.String())
				}
			}
		})
	}
}
