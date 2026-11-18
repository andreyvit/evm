package gox_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/gox"
)

type NativeShopper struct {
	Name   string
	Joined time.Time
	Tags   []string
	visits int
}

func (s *NativeShopper) Visit() int { s.visits++; return s.visits }

type NativeBasket struct {
	Items []*NativeItem
}

type NativeItem struct {
	Title string
	Cents int64
}

const nativeShopSource = `package shop

import "time"

type NativeShopper struct {
	Name   string
	Joined time.Time
	Tags   []string
	visits int
}

func (s *NativeShopper) Visit() int { s.visits++; return s.visits }

type NativeBasket struct {
	Items []*NativeItem
}

type NativeItem struct {
	Title string
	Cents int64
}
`

const nativePageSource = `package page

import (
	"shop"
	"strconv"

	"github.com/andreyvit/evm/gox"
)

func Page(b *gox.Builder, shopper *shop.NativeShopper, basket *shop.NativeBasket, query string, limit int) {
	<p title={query}>
		shopper.Name
		" "
		strconv.Itoa(shopper.Visit())
		" "
		shopper.Joined.Format("2006")
	</p>
	for i, item := range basket.Items {
		if (i < limit) {
			<li>item.Title</li>
		}
	}
}

func Tags(b *gox.Builder, tags []string) {
	for _, tag := range tags {
		<i>tag</i>
	}
}
`

func nativeShopProgram(t *testing.T) *gox.Program {
	t.Helper()
	schema, err := evmbind.New("shop", fstest.MapFS{"shop.go": {Data: []byte(nativeShopSource)}}, evmbind.Options{
		Types: []reflect.Type{reflect.TypeFor[NativeShopper](), reflect.TypeFor[NativeBasket]()},
	})
	if err != nil {
		t.Fatal(err)
	}
	pkg := schema.Bind(nil)
	tmpl, err := gox.Compile("page.gox", nativePageSource, pkg)
	if err != nil {
		t.Fatal(err)
	}
	program, err := tmpl.Link(pkg)
	if err != nil {
		t.Fatal(err)
	}
	return program
}

// Renderers accept the host's own Go values; no manual value packing.
func TestRenderNativeAcceptsGoValues(t *testing.T) {
	program := nativeShopProgram(t)
	shopper := &NativeShopper{Name: "Ada <A>", Tags: []string{"a", "b"}, Joined: time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)}
	basket := &NativeBasket{Items: []*NativeItem{{Title: "Mug"}, {Title: "Lamp"}, {Title: "Rug"}}}
	for visit, want := range []string{
		`<p title="q&amp;a">Ada &lt;A&gt; 1 2025</p><li>Mug</li><li>Lamp</li>`,
		`<p title="q&amp;a">Ada &lt;A&gt; 2 2025</p><li>Mug</li><li>Lamp</li>`,
	} {
		html, err := program.RenderNative(context.Background(), []any{shopper, basket, "q&a", 2}, gox.RenderOptions{Strict: true})
		if err != nil {
			t.Fatal(err)
		}
		if html != want {
			t.Errorf("render %d:\n got %s\nwant %s", visit+1, html, want)
		}
	}
	if shopper.visits != 2 {
		t.Errorf("the template did not share the native object: visits = %d", shopper.visits)
	}

	html, err := program.RenderFuncNative(context.Background(), "Tags", []any{shopper.Tags}, gox.RenderOptions{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := `<i>a</i><i>b</i>`; html != want {
		t.Errorf("got %s, want %s", html, want)
	}
}

func TestRenderNativeRejectsMismatchedValues(t *testing.T) {
	program := nativeShopProgram(t)
	shopper, basket := &NativeShopper{}, &NativeBasket{}
	for name, tc := range map[string]struct {
		renderer string
		args     []any
		want     string
	}{
		"swapped pointers": {"Page", []any{basket, shopper, "q", 1}, "argument 0: Go value of type *gox_test.NativeBasket does not match parameter type"},
		"wrong scalar":     {"Page", []any{shopper, basket, "q", int64(1)}, "argument 3: Go value of type int64 does not match parameter type"},
		"struct by value":  {"Page", []any{*shopper, basket, "q", 1}, "argument 0"},
		"too few":          {"Page", []any{shopper}, "got 1 arguments for 4 parameters"},
		"unknown renderer": {"Missing", nil, `renderer "Missing" is missing`},
	} {
		_, err := program.RenderFuncNative(context.Background(), tc.renderer, tc.args, gox.RenderOptions{})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want error containing %q, got %v", name, tc.want, err)
		}
	}
	// A failed conversion leaves the program usable.
	if _, err := program.RenderNative(context.Background(), []any{shopper, basket, "", 0}, gox.RenderOptions{}); err != nil {
		t.Fatal(err)
	}
}
