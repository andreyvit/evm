package gox_test

import (
	"context"
	"github.com/andreyvit/evm/gox"
	"strings"
	"testing"
)

func TestComponentParameterConventions(t *testing.T) {
	source := `package plugin

type OptionalBool struct { Value bool; Present bool }
func Leaf(b *gox.Builder, ambient *evmruntime.Ambient) { <em>{ambient.Get("color")}</em> }
//gox:arg enabled optional=true
//gox:arg count optional=true
func Item(b *gox.Builder, enabled bool, count *int, flag OptionalBool, extras map[string]any, ambient *evmruntime.Ambient) {
 <p>{enabled}
{count == nil}
{flag.Value}
{flag.Present}
{extras["label"]}
{ambient.Get("color")}</p>
 <Leaf />
}
func Page(b *gox.Builder) {
 var ambient *evmruntime.Ambient
 ambient = ambient.CloneWith(map[string]any{"color":"parent"})
 <Item enabled={true} count={3} flag={false} extras:label="X" ambient:color="child" />
 <Item extras={nil} />
 <p>{ambient.Get("color")}</p>
}`
	tmpl, err := gox.CompileSources(map[string]string{"page.gox": source})
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.RenderFunc(context.Background(), "Page", nil, gox.RenderOptions{})
	if err != nil || got != "<p>truefalsefalsetrueXchild</p><em>child</em><p>falsetruefalsefalseparent</p><em>parent</em><p>parent</p>" {
		t.Fatalf("%q: %v", got, err)
	}
}

func TestComponentParameterConventionErrors(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{`package plugin
func C(b *gox.Builder, extras map[string]any) {}\nfunc Page(b *gox.Builder) { <C extras={nil} extras:x={true} /> }`, "cannot combine"},
		{`package plugin
//gox:arg x optional=true\nfunc C(b *gox.Builder, x *int) {}\nfunc Page(b *gox.Builder) { <C x="wrong" /> }`, "cannot"},
		{`package plugin
func C(b *gox.Builder, x bool) {}\nfunc Page(b *gox.Builder) { <C x={true} x={false} /> }`, "duplicate"},
		{`package plugin
func C(b *gox.Builder) {}\nfunc Page(b *gox.Builder) { <C extras:x={true} /> }`, "no parameter"},
		{`package plugin
func C(b *gox.Builder, ambient *evmruntime.Ambient) {}\nfunc Page(b *gox.Builder) { <C ambient:x={true} /> }`, "caller scope"},
		{`package plugin
func C(b *gox.Builder, xOpt bool) {}\nfunc Page(b *gox.Builder) { <C /> }`, "xOpt"},
	} {
		_, err := gox.CompileSources(map[string]string{"page.gox": strings.ReplaceAll(tc.src, `\n`, "\n")})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("want %s: %v", tc.want, err)
		}
	}
}

func TestComponentOptionalShapes(t *testing.T) {
	for _, name := range []string{"Optional", "Opt", "Maybe", "Null", "OptionalBool", "OptBool", "MaybeBool", "NullBool"} {
		for _, field := range []string{"Valid", "Present", "IsValid", "IsPresent"} {
			t.Run(name+field, func(t *testing.T) {
				source := `package plugin
type TYPE struct { Value bool; FIELD bool }
func C(b *gox.Builder, flag TYPE, ptr *TYPE) {
 <p>{flag.Value}
 {flag.FIELD}
 {ptr == nil}</p>
}
func Page(b *gox.Builder) {
 <C flag={false} ptr={true} />
 <C />
}`
				source = strings.ReplaceAll(strings.ReplaceAll(source, "TYPE", name), "FIELD", field)
				tmpl, err := gox.CompileSources(map[string]string{"page.gox": source})
				if err != nil {
					t.Fatal(err)
				}
				p, err := tmpl.Link()
				if err != nil {
					t.Fatal(err)
				}
				got, err := p.RenderFunc(context.Background(), "Page", nil, gox.RenderOptions{})
				if err != nil || got != "<p>falsetruefalse</p><p>falsefalsetrue</p>" {
					t.Fatalf("%q: %v", got, err)
				}
			})
		}
	}
}

func TestComponentExtrasSelectNamedMaps(t *testing.T) {
	tmpl, err := gox.CompileSources(map[string]string{"page.gox": `package plugin

func C(b *gox.Builder, first map[string]any, last map[string]any) { <p>{first["x"]}
 {last["x"]}</p> }
func Page(b *gox.Builder) {
 first := map[string]any{"x":"provided"}
 <C first={first} last:x="last" />
 <C first:x="first" last:x="last" />
}`})
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.RenderFunc(context.Background(), "Page", nil, gox.RenderOptions{})
	if err != nil || got != "<p>providedlast</p><p>firstlast</p>" {
		t.Fatalf("%q: %v", got, err)
	}
}

func TestComponentGenericOptional(t *testing.T) {
	tmpl, err := gox.CompileSources(map[string]string{"page.gox": `package plugin

func C(b *gox.Builder, flag gox.Optional[bool]) { <p>{flag.Value}
 {flag.Present}</p> }
func Page(b *gox.Builder) { <C flag={false} />
 <C /> }
`})
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.RenderFunc(context.Background(), "Page", nil, gox.RenderOptions{})
	if err != nil || got != "<p>falsetrue</p><p>falsefalse</p>" {
		t.Fatalf("%q: %v", got, err)
	}
}

func TestComponentMapInputs_dynamic_keys_and_formatting(t *testing.T) {
	data, err := examples.ReadFile("testdata/examples/map-inputs.gox")
	if err != nil {
		t.Fatal(err)
	}
	source := strings.Replace(string(data), "otherExtras:{key}", `otherExtras:{ key + "" }`, 1)
	for range 2 {
		tmpl, err := gox.CompileSources(map[string]string{"page.gox": source})
		if err != nil {
			t.Fatal(err)
		}
		p, err := tmpl.Link()
		if err != nil {
			t.Fatal(err)
		}
		got, err := p.RenderFunc(context.Background(), "Page", nil, gox.RenderOptions{Strict: true})
		if err != nil || got != "<p>firstsecond</p><em>child</em><p>parent</p>" {
			t.Fatalf("%q: %v", got, err)
		}
		source, err = gox.FormatSource("page.gox", source)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestComponentMapInputs_dynamic_keys_evaluate_once(t *testing.T) {
	source := `package plugin
type Labels map[int]string
func Card(b *gox.Builder, labels Labels, flags map[string]bool, ambient *evmruntime.Ambient) { <p>labels[1]; labels[2]; flags["data-active"]; ambient.Get("color")</p> }
func Page(b *gox.Builder) {
 var ambient *evmruntime.Ambient
 calls := 0
 key := func() int { calls++; return calls }
 color := "color"
 <Card labels:{key()}="A" labels:{key()}="B" flags:data-active ambient:{color}="blue" />
 <p>calls</p>
}`
	tmpl, err := gox.CompileSources(map[string]string{"page.gox": source})
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.RenderFunc(context.Background(), "Page", nil, gox.RenderOptions{})
	if err != nil || got != "<p>ABtrueblue</p><p>2</p>" {
		t.Fatalf("%q: %v", got, err)
	}
}

func TestComponentMapInputErrors(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{`package plugin
func C(b *gox.Builder, extras map[string]string) {}\nfunc Page(b *gox.Builder) { <C extras:a:{"x"}="no" /> }`, "requires a map"},
		{`package plugin
func C(b *gox.Builder, extras map[string]any) {}\nfunc Page(b *gox.Builder) { <C extras:attrs="no" extras:attrs:type="email" /> }`, "conflicting"},
		{`package plugin
func C(b *gox.Builder, extras map[string]any) {}\nfunc Page(b *gox.Builder) { <C extras:attrs:type="email" extras:attrs="no" /> }`, "cannot combine"},
		{`package plugin
func C(b *gox.Builder, ambient *evmruntime.Ambient) {}\nfunc Page(b *gox.Builder) { <C ambient-x="no" /> }`, "unknown component input"},
		{`package plugin
func C(b *gox.Builder, extras string) {}\nfunc Page(b *gox.Builder) { <C extras:x="no" /> }`, "not a map"},
		{`package plugin
func C(b *gox.Builder, extras map[string]string) {}\nfunc Page(b *gox.Builder) { <C extras:x={true} /> }`, "cannot"},
		{`package plugin
func C(b *gox.Builder, extras map[string]string) {}\nfunc Page(b *gox.Builder) { <C extras:{1}="no" /> }`, "cannot"},
		{`package plugin
func C(b *gox.Builder, extras map[string]any) {}\nfunc Page(b *gox.Builder) { <C extras:="no" /> }`, "empty map"},
		{`package plugin
func C(b *gox.Builder, extras map[string]any) {}\nfunc Page(b *gox.Builder) { <C extras={nil} extras:{"x"}="no" /> }`, "cannot combine"},
		{`package plugin
func Page(b *gox.Builder) { <div attrs:{"x"}="no" /> }`, "require a component"},
		{`package plugin
func C(b *gox.Builder, extras map[string]any) {}\nfunc Page(b *gox.Builder) { <C extra-x="no" /> }`, "unknown component input"},
	} {
		_, err := gox.CompileSources(map[string]string{"page.gox": strings.ReplaceAll(tc.src, `\n`, "\n")})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("want %q: %v", tc.want, err)
		}
	}
}

func TestXMLCallAssignment(t *testing.T) {
	source := `package plugin
func Page(b *gox.Builder) {
 dimension := "points"
 tierContent := <Label key="tier" args:name="Gold" />
 content := <Label key="gap" args:{dimension}="12" args:tier={tierContent} />
 switch dimension {
 case "points": content = <Label key="again" args:tier={content} />
 }
 if final := <Label key="again" args:tier={content} />; final != "" {
  <p>final</p>
 }
}
//gox:arg args optional
func Label(b *gox.Builder, key string, args map[string]any) string {
 if key == "tier" { return args["name"].(string) }
 if key == "gap" { return args["points"].(string) + " to " + args["tier"].(string) }
 return args["tier"].(string)
}`
	formatted, err := gox.FormatSource("assignment.gox", source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(formatted, `tierContent := <Label`) {
		t.Fatal(formatted)
	}
	again, err := gox.FormatSource("assignment.gox", formatted)
	if err != nil || again != formatted {
		t.Fatalf("unstable format: %v\n%s", err, again)
	}
	for _, src := range []string{source, formatted} {
		tmpl, err := gox.Compile("assignment.gox", src)
		if err != nil {
			t.Fatal(err)
		}
		p, err := tmpl.Link()
		if err != nil {
			t.Fatal(err)
		}
		got, err := p.Render(context.Background(), nil, gox.RenderOptions{})
		if err != nil || got != "<p>12 to Gold</p>" {
			t.Fatalf("%q: %v", got, err)
		}
	}
}

func TestXMLAssignmentRejectsRenderingOnlyCalls(t *testing.T) {
	for _, source := range []string{
		`package plugin
func Page(b *gox.Builder) { value := <Label />; value }; func Label(b *gox.Builder) { <p>"label"</p> }`,
		`package plugin
func Page(b *gox.Builder) { value := <Label></Label>; value }; func Label() string { return "label" }`,
	} {
		if _, err := gox.Compile("assignment.gox", source); err == nil {
			t.Fatal("accepted non-value or non-self-closing assignment")
		}
	}
}

func TestNestedComponentMaps(t *testing.T) {
	source := `package plugin
func Page(b *gox.Builder) {
 hits := 0
 key := func() string { hits++; return "name" }
 <C
  extras:attrs:type="email"
  extras:attrs:{key()}="A&B"
  extras:{"tier::class"}="tier"
  typed:options:label="Label"
 />
 <span>hits</span>
}
func C(b *gox.Builder, extras map[string]any, typed map[string]map[string]string) {
 attrs := extras["attrs"].(map[string]any)
 <input type={attrs["type"]} name={attrs["name"]}>
 <p class={extras["tier::class"]}>typed["options"]["label"]</p>
}`
	formatted, err := gox.FormatSource("nested.gox", source)
	if err != nil {
		t.Fatal(err)
	}
	again, err := gox.FormatSource("nested.gox", formatted)
	if err != nil || again != formatted {
		t.Fatalf("unstable formatting: %v\n%s", err, again)
	}
	for _, src := range []string{source, formatted} {
		tmpl, err := gox.Compile("nested.gox", src)
		if err != nil {
			t.Fatal(err)
		}
		p, err := tmpl.Link()
		if err != nil {
			t.Fatal(err)
		}
		got, err := p.Render(context.Background(), nil, gox.RenderOptions{})
		want := `<input type="email" name="A&amp;B"><p class="tier">Label</p><span>1</span>`
		if err != nil || got != want {
			t.Fatalf("%q: %v", got, err)
		}
	}
}

func TestAssignedXMLCallsInValueHelpers(t *testing.T) {
	source := `package plugin
func Page(b *gox.Builder) { <p>text()</p> }
func text() string {
 value := <Value key="plain" />
 decorate := func() string { result := <Value key={value+" value"} />; return result }
 return decorate()
}
func Value(key string) string { return key }`
	tmpl, err := gox.Compile("values.gox", source)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Render(context.Background(), nil, gox.RenderOptions{})
	if err != nil || got != "<p>plain value</p>" {
		t.Fatalf("%q: %v", got, err)
	}
}
