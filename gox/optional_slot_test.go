package gox_test

import (
	"context"
	"testing"

	"github.com/andreyvit/evm/gox"
)

// An optional slot renders when the caller supplies it and is nil otherwise.
func TestOptionalSlot(t *testing.T) {
	tmpl, err := gox.CompileSources(map[string]string{"layout.gox": `package views

import "github.com/andreyvit/evm/gox"

//gox:arg styles optional
func Layout(b *gox.Builder, styles func(), content func()) {
	<head>
		if styles != nil {
			styles()
		}
	</head>
	<main>content()</main>
}

func Styled(b *gox.Builder) {
	<Layout>
		<slot-styles>
			<style>.a{}</style>
		</slot-styles>
		<slot-content>
			<p>"styled"</p>
		</slot-content>
	</Layout>
}

func Empty(b *gox.Builder) {
	<Layout>
		<slot-styles></slot-styles>
		<slot-content>
			<p>"empty"</p>
		</slot-content>
	</Layout>
}

func Plain(b *gox.Builder) {
	<Layout>
		<slot-content>
			<p>"plain"</p>
		</slot-content>
	</Layout>
}
`})
	if err != nil {
		t.Fatal(err)
	}
	program, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"Styled": `<head><style>.a{}</style></head><main><p>styled</p></main>`,
		"Empty":  `<head></head><main><p>empty</p></main>`,
		"Plain":  `<head></head><main><p>plain</p></main>`,
	} {
		got, err := program.RenderFunc(context.Background(), name, nil, gox.RenderOptions{Strict: true})
		if err != nil {
			t.Fatal(name, err)
		}
		if got != want {
			t.Errorf("%s:\n got %s\nwant %s", name, got, want)
		}
	}
}
