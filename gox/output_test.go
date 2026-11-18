package gox_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/gox"
)

type FormatLocale struct {
	Currency string
	Layout   string
}

type FormatMoney int64

type FormatOrder struct {
	Total  FormatMoney
	Placed time.Time
	Count  int
	Note   *FormatNote
}

type FormatNote struct{ Total FormatMoney }

const formatHostSource = `package shop

import "time"

type FormatLocale struct {
	Currency string
	Layout   string
}

type FormatMoney int64

type FormatOrder struct {
	Total  FormatMoney
	Placed time.Time
	Count  int
	Note   *FormatNote
}

type FormatNote struct{ Total FormatMoney }

func Money(m FormatMoney, locale *FormatLocale) string
func Day(t time.Time, locale *FormatLocale) string
func Count(n int) string
`

func formatHost(t *testing.T) evm.Package {
	t.Helper()
	schema, err := evmbind.New("shop", fstest.MapFS{"shop.go": {Data: []byte(formatHostSource)}}, evmbind.Options{
		Types: []reflect.Type{reflect.TypeFor[FormatOrder](), reflect.TypeFor[FormatLocale]()},
		Functions: map[string]any{
			"Money": func(m FormatMoney, locale *FormatLocale) string {
				return locale.Currency + " " + strings.TrimSuffix(strings.TrimSuffix(time.Duration(m).String(), "ns"), "µs")
			},
			"Day":   func(t time.Time, locale *FormatLocale) string { return t.Format(locale.Layout) },
			"Count": func(n int) string { return "#" + string(rune('0'+n)) },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return schema.Bind(nil)
}

var outputOptions = gox.Options{ImplicitTypes: []string{"*shop.FormatLocale"}}

const outputHeader = `package views

import (
	"shop"

	"github.com/andreyvit/evm/gox"
)

func YesNo(v bool) gox.HTML {
	if v {
		return "<b>yes</b>"
	}
	return "no"
}

func Tags(b *gox.Builder, tags []string) {
	for _, tag := range tags {
		<i>tag</i>
	}
}
`

// GoX picks no display form for a value. A template names the function that
// turns the value into text, as a call or a pipeline, and the function
// receives implicit arguments from the scope like any other GoX call.
func TestOutputCallsFunctionsByName(t *testing.T) {
	host := formatHost(t)
	tmpl, err := outputOptions.Compile("page.gox", outputHeader+`
func Page(b *gox.Builder, order *shop.FormatOrder, locale *shop.FormatLocale) {
	<p title={shop.Day(order.Placed)} data-count={order.Count} aria-label="{order.Count} of {order.Total | shop.Money}">
		order.Total | shop.Money
		" on "
		shop.Day(order.Placed)
		" x"
		order.Count | shop.Count
		" "
		YesNo(order.Count > 1)
		" "
		Tags([]string{"a", "<b>"})
		shop.Money(order.Note.Total)
	</p>
	<Row order={order} />
}

// The locale comes from this function's own implicit parameter.
func Row(b *gox.Builder, order *shop.FormatOrder, locale *shop.FormatLocale) {
	<td>order.Total | shop.Money</td>
}
`, host)
	if err != nil {
		t.Fatal(err)
	}
	program, err := tmpl.Link(host)
	if err != nil {
		t.Fatal(err)
	}
	order := &FormatOrder{Total: 7, Placed: time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC), Count: 2}
	html, err := program.RenderFuncNative(context.Background(), "Page", []any{order, &FormatLocale{Currency: "EUR", Layout: "2 Jan 2006"}}, gox.RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The nil Note skips its output.
	want := `<p title="1 Mar 2025" data-count="2" aria-label="2 of EUR 7">EUR 7 on 1 Mar 2025 x#2 <b>yes</b> <i>a</i><i>&lt;b&gt;</i></p><td>EUR 7</td>`
	if html != want {
		t.Errorf("got  %s\nwant %s", html, want)
	}
}

func TestOutputRejectsTypesByPosition(t *testing.T) {
	host := formatHost(t)
	for name, want := range map[string][2]string{
		"int in text":          {`<p>order.Count</p>`, "page.gox:23:5: cannot output a value of type int: text takes strings and trusted content; convert it"},
		"bool in text":         {`<p>(order.Count > 1)</p>`, "cannot output a value of type bool"},
		"named number":         {`<p>order.Total</p>`, "cannot output a value of type shop.FormatMoney: text takes strings and trusted content; convert it"},
		"time":                 {`<p>order.Placed</p>`, "cannot output a value of type time.Time"},
		"any":                  {`var v any = "x"; <p>v</p>`, "cannot output a value of type any"},
		"constant":             {`<p>42</p>`, "cannot output a value of type int"},
		"nil":                  {`<p>nil</p>`, "cannot output nil"},
		"title":                {`<title>order.Count</title>`, "cannot output a value of type int"},
		"named bool attribute": {`type Flag bool; <p hidden={Flag(true)}></p>`, "cannot use a value of type Flag as an attribute value"},
		"named in attribute":   {`<p data-total={order.Total}></p>`, "cannot use a value of type shop.FormatMoney as an attribute value: an attribute takes a string, a bool or an integer; convert it"},
		"float in attribute":   {`<p data-x={1.5}></p>`, "cannot use a value of type float64 as an attribute value"},
		"struct in attribute":  {`<p data-order={*order}></p>`, "cannot use a value of type shop.FormatOrder as an attribute value"},
		"slice in attribute":   {`<p data-tags={[]string{"a"}}></p>`, "cannot use a value of type []string as an attribute value"},
		"byte in attribute":    {`var n uint8 = 7; <p data-n={n}></p>`, "cannot use a value of type uint8 as an attribute value: an attribute takes a string, a bool or an integer; uint8 may be a character or a number, so convert it to a string or to int"},
		"rune in attribute":    {`<p data-n={'a'}></p>`, "cannot use a value of type rune as an attribute value"},
		"rune interpolated":    {`for _, r := range "ab" { <a href="/letters/{r}"></a> }`, "cannot interpolate a value of type rune into an attribute: an attribute takes strings and integers; rune may be a character or a number, so convert it to a string or to int"},
		"byte interpolated":    {`<a href="/letters/{"ab"[0]}"></a>`, "cannot interpolate a value of type byte into an attribute"},
		"bool interpolated":    {`<p class="a {order.Count > 1}"></p>`, "cannot interpolate a value of type bool into an attribute: an attribute takes strings and integers; convert it"},
		"float interpolated":   {`<p title="{1.5} stars"></p>`, "cannot interpolate a value of type float64 into an attribute: an attribute takes strings and integers; convert it"},
		"time interpolated":    {`<p title="on {order.Placed}"></p>`, "cannot interpolate a value of type time.Time into an attribute"},
		"named interpolated":   {`<a href="/pay/{order.Total}"></a>`, "cannot interpolate a value of type shop.FormatMoney into an attribute"},
		"bool in style":        {`<style>p { color: {=(order.Count > 1)} }</style>`, "cannot use a value of type bool in CSS: CSS takes strings, integers and floats; convert it"},
		"named in style attr":  {`<p style="width: {order.Total}px"></p>`, "cannot use a value of type shop.FormatMoney in CSS"},
		"missing implicit":     {`func() { locale := 1; _ = locale; <p>shop.Money(order.Total)</p> }()`, "not enough arguments in call to shop.Money"},
	} {
		source := outputHeader + "\nfunc Page(b *gox.Builder, order *shop.FormatOrder, locale *shop.FormatLocale) {\n\t" + want[0] + "\n}\n"
		_, err := outputOptions.Compile("page.gox", source, host)
		if err == nil || !strings.Contains(err.Error(), want[1]) {
			t.Errorf("%s: want error containing %q, got: %v", name, want[1], err)
		}
	}
}

// Each position takes the values it writes as they are: text takes strings, an attribute also integers and, as a whole value, bools, CSS
// integers and floats, and JavaScript any value.
func TestOutputAcceptsValuesByPosition(t *testing.T) {
	tmpl, err := gox.Compile("page.gox", `package views

import "github.com/andreyvit/evm/gox"

type Slug string

func Page(b *gox.Builder, name string, count int, on bool, ratio float64) {
	slug := Slug("a&b")
	var small int16 = 7
	var wide uint64 = 18446744073709551615
	<p hidden={!on} tabindex={count} data-n={small} data-id="item-{wide}" title="{count} of {int32(9)}" aria-busy={on} class="n-{name}" style="width: {count}px; opacity: {ratio}; flex: {1234567.25}">
		name
		slug
		gox.HTML("<i>ok</i>")
		gox.Text("<t>")
	</p>
	<a href="/items/{count}?page={small}" onclick="go({count}, {on}, {ratio})">"x"</a>
	<script>var count = {=count}, on = {=on}, list = {=[]int{1, 2}}, by = {=map[string]any{"k": ratio}};</script>
	<style>p { width: {=count}px; line-height: {=ratio}; margin: {=1234567.25}px; color: {=name} }</style>
}
`)
	if err != nil {
		t.Fatal(err)
	}
	program, err := tmpl.Link()
	if err != nil {
		t.Fatal(err)
	}
	html, err := program.RenderNative(context.Background(), []any{"<A>", 3, true, 0.5}, gox.RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Log(html)
	for _, want := range []string{
		`<p tabindex="3" data-n="7" data-id="item-18446744073709551615" title="3 of 9" aria-busy="true" class="n-&lt;A&gt;" style="width: 3px; opacity: 0.5; flex: 1234567.25">&lt;A&gt;a&amp;b<i>ok</i>&lt;t&gt;</p>`,
		`<a href="/items/3?page=7" onclick=`,
		`var count =  3 , on =  true , list = [1,2], by = {"k":0.5};`,
		`<style>p { width: 3px; line-height: 0.5; margin: 1234567.25px; color: `,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("output lacks %s", want)
		}
	}
}
