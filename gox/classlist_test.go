package gox_test

import (
	"github.com/andreyvit/evm/gox"
	"slices"
	"testing"
)

func TestClassList(t *testing.T) {
	var classes gox.ClassList
	for _, group := range []string{"", "  ", " one ", "two three"} {
		classes.Add(group)
	}
	before := classes.String()
	classes.Add("four")
	if before != "one two three" || classes.String() != "one two three four" {
		t.Fatalf("before %q, after %q", before, classes.String())
	}
	got := render(t, `package plugin
import "github.com/andreyvit/evm/gox"
func Page(b *gox.Builder) {
 var classes gox.ClassList
 classes.Add(" first ")
 classes.Add("")
 classes.Add("second third")
 <div class={classes.String()}></div>
 classes.Add("fourth")
 <div class={classes.String()}></div>
}`)
	if got != `<div class="first second third"></div><div class="first second third fourth"></div>` {
		t.Fatal(got)
	}
}

func TestClassListClasses(t *testing.T) {
	tmpl, err := gox.Compile("classes.gox", `package plugin
import "github.com/andreyvit/evm/gox"
type Other struct{}
func (o *Other) Add(value string) {}
func Page(b *gox.Builder) {
 var other Other
 other.Add("not-a-class")
 var classes gox.ClassList
 classes.Add("button flex")
 <button class={classes.String()}></button>
}`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, class := range tmpl.Classes {
		names = append(names, class.Name)
	}
	if !slices.Equal(names, []string{"button", "flex"}) {
		t.Fatal(names)
	}
}
