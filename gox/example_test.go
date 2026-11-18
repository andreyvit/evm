package gox_test

import (
	"context"
	"embed"
	"fmt"
	"testing"

	"github.com/andreyvit/evm/gox"
	"github.com/andreyvit/evm/values"
)

func ExampleCompile() {
	tmpl, err := gox.Compile("greeting.gox", `package plugin

import "github.com/andreyvit/evm/gox"

func Greeting(b *gox.Builder, name string) {
    <p>
        "Hello, "
        name
        "!"
    </p>
}`)
	if err != nil {
		panic(err)
	}
	program, err := tmpl.Link()
	if err != nil {
		panic(err)
	}
	output, err := program.Render(context.Background(), []values.Value{
		values.String("Alice & Bob"),
	}, gox.RenderOptions{Strict: true})
	if err != nil {
		panic(err)
	}
	fmt.Println(output)
	// Output: <p>Hello, Alice &amp; Bob!</p>
}

//go:embed testdata/examples/*.gox
var examples embed.FS

func TestExampleComponents(t *testing.T) {
	sources := map[string]string{}
	for _, name := range []string{"page.gox", "button.gox"} {
		data, err := examples.ReadFile("testdata/examples/" + name)
		if err != nil {
			t.Fatal(err)
		}
		sources[name] = string(data)
	}
	tmpl, err := gox.CompileFiles("page.gox", sources)
	if err != nil {
		t.Fatal(err)
	}
	program, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	output, err := program.RenderNative(context.Background(), []any{"Customers", []string{"Alice & Bob"}}, gox.RenderOptions{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	want := `<main><h1>Customers (1)</h1><ul><li><button title="Customers" aria-label="Alice &amp; Bob" class="button px-4 py-2">Hello, Alice &amp; Bob!<span class="note">Customers</span></button></li></ul></main>`
	if output != want {
		t.Fatalf("got %q, want %q", output, want)
	}
}
