package gox_test

import (
	"github.com/andreyvit/evm/gox"
	"reflect"
	"testing"
)

func TestGoXFiles(t *testing.T) {
	tmpl, err := gox.CompileSources(map[string]string{
		"blocks/card.gox": `package plugin
func Card(b *gox.Builder) { <p>{"card"}</p> }; func Label() string { return "label" }`,
		"empty.gox": `package plugin
var number = 1`,
		"ordinary.go": `package plugin; func Ordinary() {}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	program, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	want := []gox.File{{Filename: "blocks/card.gox", Name: "card", Functions: []string{"Card", "Label"}}, {Filename: "empty.gox", Name: "empty"}}
	if got := program.Files(); !reflect.DeepEqual(got, want) {
		t.Fatalf("files: %+v", got)
	}
	for _, name := range []string{"Card", "Label"} {
		if got, ok := program.FunctionFile(name); !ok || !reflect.DeepEqual(got, want[0]) {
			t.Fatalf("%s: %+v, %v", name, got, ok)
		}
	}
	for _, name := range []string{"Ordinary", "Missing"} {
		if _, ok := program.FunctionFile(name); ok {
			t.Fatalf("unexpected file for %s", name)
		}
	}
	files := program.Files()
	files[0].Functions[0] = "mutated"
	file, ok := program.File("blocks/card.gox")
	if !ok || !reflect.DeepEqual(file, want[0]) {
		t.Fatalf("file: %+v, %v", file, ok)
	}
	file.Functions[0] = "mutated again"
	if got, _ := tmpl.File(file.Filename); !reflect.DeepEqual(got, want[0]) {
		t.Fatalf("metadata mutated: %+v", got)
	}
	if _, ok := program.File("card"); ok {
		t.Fatal("basename lookup must not match source key")
	}
}
