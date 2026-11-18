package gox_test

import "testing"

func TestOptionalCellSemantics(t *testing.T) {
	got := render(t, `package plugin

import (
	"fmt"

	"github.com/andreyvit/evm/gox"
)

func Check(x gox.Optional[int]) bool { return x.Present && x.Value==1 }
func Page(b *gox.Builder) {
 var missing gox.Optional[int]
 zero:=gox.Optional[int]{Present:true}
 one:=gox.Optional[int]{Value:1,Present:true}
 absent:=gox.Optional[int]{Value:42}
 if missing.Present || !zero.Present || zero==missing || !Check(one) || absent.Value!=42 || absent.Present { panic("numbers") }
 p:=&absent.Value
 *p=7
 present:=&absent.Present
 *present=true
 copy:=absent
 *p=8
 if copy.Value!=7 || !copy.Present || absent.Value!=8 { panic("mutation") }
 empty:=gox.Optional[string]{Present:true}
 text:=gox.Optional[string]{Value:"hello",Present:true}
 hidden:=gox.Optional[string]{Value:"hidden"}
 if !empty.Present || empty.Value!="" || hidden.Present || hidden.Value!="hidden" { panic("strings") }
 var boxed any=text
 if boxed.(gox.Optional[string])!=text { panic("interface") }
 var z any=missing
 if z.(gox.Optional[int])!=missing { panic("zero interface") }
 m:=map[gox.Optional[int]]string{missing:"missing",zero:"zero",one:"one",absent:"eight"}
 if m[gox.Optional[int]{Value:1,Present:true}]!="one" || m[missing]!="missing" || m[zero]!="zero" { panic("map") }
 rich:=gox.Optional[gox.Content]{Value:gox.TrustedHTML(gox.HTML("<b>x</b>")),Present:true}
 hiddenRich:=gox.Optional[gox.Content]{Value:gox.Text("hidden")}
 if !rich.Present || !rich.Value.TrustedHTML || hiddenRich.Present || hiddenRich.Value.Content!="hidden" { panic("content") }
 nested:=gox.Optional[gox.Optional[int]]{Value:missing,Present:true}
 if !nested.Present || nested.Value.Present { panic("nested") }
 list:=gox.Optional[[]string]{Value:[]string{"a"},Present:true}
 if !list.Present || list.Value[0]!="a" { panic("slice") }
 if fmt.Sprint(text)!="{hello true}" { panic("format") }
 <p>text.Value</p>
}`)
	if got != "<p>hello</p>" {
		t.Fatal(got)
	}
}
