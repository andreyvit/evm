package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/andreyvit/evm/gox"
)

// render compiles the current Views, links them and renders the page.
func render(env *Env, args ...any) (string, error) {
	tmpl, err := env.Options.CompileFiles("Page", Views, env.Packages...)
	if err != nil {
		return "", err
	}
	page, err := tmpl.Link(env.Packages...)
	if err != nil {
		return "", err
	}
	return page.RenderNative(context.Background(), args, gox.RenderOptions{Strict: true})
}

// freshStore copies TheStore, so that a test starts with unviewed products.
func freshStore() *Store {
	store := *TheStore
	store.Products = nil
	for _, p := range TheStore.Products {
		store.Products = append(store.Products, &Product{SKU: p.SKU, Name: p.Name, Price: p.Price, Stock: p.Stock, Tags: p.Tags})
	}
	return &store
}

func TestRun(t *testing.T) {
	var out strings.Builder
	run(&out)
	pages := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(pages) != 2 {
		t.Fatalf("got %d pages, want 2:\n%s", len(pages), out.String())
	}
	for _, want := range []string{`<input name="q" value="" placeholder="Search">`, `Enamel Mug</a>`, `Brass Lamp</a>`} {
		if !strings.Contains(pages[0], want) {
			t.Errorf("first page lacks %q", want)
		}
	}
	if !strings.Contains(pages[1], `<input name="q" value="lamp" placeholder="Search">`) || !strings.Contains(pages[1], `Brass Lamp</a>`) || strings.Contains(pages[1], `Enamel Mug</a>`) {
		t.Errorf("second page does not show the search for lamp:\n%s", pages[1])
	}
}

func TestRenderAndRecompile(t *testing.T) {
	env := NewEnv()
	store := freshStore()
	user := &User{Name: "Ada Lovelace", Email: "ada@example.com", Admin: true, Joined: time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)}
	cart := &Cart{Coupon: "SPRING", Lines: []CartLine{
		{Product: store.Products[0], Qty: 2},
		{Product: store.Products[2], Qty: 1},
	}}
	check := func(wants ...string) {
		t.Helper()
		html, err := render(env, store, user, cart, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(html, want) {
				t.Errorf("output lacks %q", want)
			}
		}
		if t.Failed() {
			t.Fatal(html)
		}
	}

	check(
		`<title>Prairie Goods</title>`,                           // Layout argument
		`<style>.out { opacity: .5 }</style></head>`,             // Layout slot
		`<h1>Welcome to Prairie Goods</h1>`,                      // store passed implicitly to Header
		`<span class="avatar" title="ada@example.com">AL</span>`, // method on *User
		`member since Mar 1, 2025 — 3 items in cart`,             // FormatDate, package function
		`<a href="/products/blk-2">Wool Blanket &lt;XL&gt;</a>`,  // escaped data
		`<li class="product out">`,                               // helper defined in the template
		`<td>$100.50</td>`,                                       // method on *Cart, FormatMoney
		`With coupon spring</td><td>$90.45</td>`,                 // pipelines, the second with a placeholder
		`<td>2</td>`,                                             // strconv.Itoa
		`Add $149.50 more for free shipping`,                     // constant, Money arithmetic
		`Enamel Mug</a> $18.00 <span class="tag">#kitchen</span> <span class="tag">#camp</span><small> seen 1×</small>`,
	)

	// Edit the template. The next render compiles the new source; the
	// application keeps its state, so the view counter goes on counting.
	original := Views["views/page.gox"]
	t.Cleanup(func() { Views["views/page.gox"] = original })
	Views["views/page.gox"] = strings.Replace(original, `"Welcome to "`, `"Shop at "`, 1)

	check(
		`<h1>Shop at Prairie Goods</h1>`,
		`<small> seen 2×</small>`,
	)

	// The host changes its own data; the template sees it on the next render.
	cart.Lines = append(cart.Lines, CartLine{Product: store.Products[1], Qty: 2})
	check(
		`<td>$358.50</td>`,
		`<td colspan="3">Free shipping</td>`,
	)
}

// The templates are formatted, and the formatter adds a missing import of
// the application package because it is given the environment's packages.
func TestTemplateFormatting(t *testing.T) {
	env := NewEnv()
	if len(Views) != 4 {
		t.Fatalf("got %d templates, want 4", len(Views))
	}
	format := func(name string) string {
		t.Helper()
		formatted, err := env.Options.FormatSource(name, Views[name], env.Packages...)
		if err != nil {
			t.Fatal(err)
		}
		return formatted
	}
	for name, source := range Views {
		if formatted := format(name); formatted != source {
			t.Errorf("%s is not formatted; want:\n%s", name, formatted)
		}
	}

	// A file that names a package without importing it does not compile.
	const name = "views/cart.gox"
	original := Views[name]
	t.Cleanup(func() { Views[name] = original })
	Views[name] = strings.Replace(original, "\t\"app\"\n", "", 1)
	_, err := render(env, TheStore, &User{}, &Cart{}, "")
	if err == nil || !strings.Contains(err.Error(), name+":") || !strings.Contains(err.Error(), `add import "app"`) {
		t.Errorf("want a compile error in %s naming the missing import, got: %v", name, err)
	}
	if formatted := format(name); formatted != original {
		t.Errorf("formatting did not restore the import:\n%s", formatted)
	}
}
