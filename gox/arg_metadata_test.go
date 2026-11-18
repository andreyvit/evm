package gox_test

import (
	"context"
	"strings"
	"testing"

	"github.com/andreyvit/evm/gox"
)

func TestArgumentMetadata(t *testing.T) {
	source := `package plugin
//gox:arg label optional=true legacyName="String Arg Name"
//gox:arg enabled optional=true
func Card(b *gox.Builder, label string, enabled bool, flag gox.Optional[bool]) {
 <p>label; enabled; flag.Present; flag.Value</p>
}
func Page(b *gox.Builder) {
 label := "not inherited"
 _ = label
 <Card />
 <Card label="Shown" enabled={true} flag={false} />
}`
	for _, format := range []bool{false, true} {
		input := source
		if format {
			var err error
			input, err = gox.FormatSource("page.gox", source)
			if err != nil {
				t.Fatal(err)
			}
		}
		tmpl, err := gox.CompileSources(map[string]string{"page.gox": input})
		if err != nil {
			t.Fatal(err)
		}
		p, err := tmpl.Link()
		if err != nil {
			t.Fatal(err)
		}
		params := p.ParametersFor("Card")
		if len(params) != 3 || !params[0].Optional || params[0].LegacyName != "String Arg Name" || params[0].InputName() != "String Arg Name" || params[1].InputName() != "enabled" || !params[2].Optional {
			t.Fatalf("%+v", params)
		}
		got, err := p.RenderFunc(context.Background(), "Page", nil, gox.RenderOptions{})
		if err != nil || got != "<p>falsefalsefalse</p><p>Showntruetruefalse</p>" {
			t.Fatalf("%q: %v", got, err)
		}
	}
}

func TestArgumentMetadataErrors(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{`package plugin
//gox:arg b optional=true\nfunc C(b *gox.Builder) {}`, "builder"},
		{`package plugin
//gox:arg x optional=true; ignored=true\nfunc C(b *gox.Builder, x string) {}`, "semicolon"},
		{`package plugin
//gox:arg missing optional=true\nfunc C(b *gox.Builder, x string) {}`, "declared parameter"},
		{`package plugin
//gox:arg x optional=yes\nfunc C(b *gox.Builder, x string) {}`, "true or false"},
		{`package plugin
//gox:arg x legacyName=old\nfunc C(b *gox.Builder, x string) {}`, "quoted string"},
		{`package plugin
//gox:arg x unexpected=true\nfunc C(b *gox.Builder, x string) {}`, "unknown gox:arg"},
		{`package plugin
//gox:arg x optional=true optional=false\nfunc C(b *gox.Builder, x string) {}`, "duplicate"},
		{`package plugin
//gox:arg x optional=true\n//gox:arg x legacyName="old"\nfunc C(b *gox.Builder, x string) {}`, "duplicate"},
		{`package plugin
//gox:arg x legacyName="y"\nfunc C(b *gox.Builder, x,y string) {}`, "duplicate legacy"},
		{`package plugin
//gox:arg x optional=true legacyName="old_name"\nfunc C(b *gox.Builder, x string) {}\nfunc Page(b *gox.Builder) { <C old_name="no" /> }`, "unknown"},
		{`package plugin
//gox:arg x optional=false\nfunc C(b *gox.Builder, x gox.Optional[string]) {}\nfunc Page(b *gox.Builder) { <C /> }`, "x"},
		{`package plugin
func C(b *gox.Builder, xOpt string) {}\nfunc Page(b *gox.Builder) { <C x="no" /> }`, "unknown component input x"},
	} {
		_, err := gox.CompileSources(map[string]string{"page.gox": strings.ReplaceAll(tc.source, `\n`, "\n")})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("want %q: %v", tc.want, err)
		}
	}
}

func TestArgumentMetadataOnGoHelper(t *testing.T) {
	tmpl, err := gox.CompileSources(map[string]string{
		"helper.go": `package plugin
//gox:arg text optional=true legacyName="caption_text"
func Caption(text string) string { return "[" + text + "]" }`,
		"page.gox": `package plugin
func Page(b *gox.Builder) { <Caption />; <Caption text="x" /> }`,
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	if params := p.ParametersFor("Caption"); len(params) != 1 || !params[0].Optional || params[0].InputName() != "caption_text" {
		t.Fatalf("%+v", params)
	}
	got, err := p.RenderFunc(context.Background(), "Page", nil, gox.RenderOptions{})
	if err != nil || got != "[][x]" {
		t.Fatalf("%q: %v", got, err)
	}
}
