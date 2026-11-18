package goxbasic

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRenderAndRecompile(t *testing.T) {
	env, err := NewEnv()
	if err != nil {
		t.Fatal(err)
	}
	user := &User{Name: "Ada Lovelace", Email: "ada@example.com", Admin: true, Joined: time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)}
	cart := &Cart{Coupon: "SPRING", Lines: []CartLine{
		{Product: TheStore.Products[0], Qty: 2},
		{Product: TheStore.Products[2], Qty: 1},
	}}
	render := func(wants ...string) {
		t.Helper()
		html, err := env.Render(context.Background(), TheStore, user, cart, "")
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

	render(
		`<title>Prairie Goods</title>`,                           // Layout argument
		`<style>.out { opacity: .5 }</style></head>`,             // Layout slot
		`<h1>Welcome to Prairie Goods</h1>`,                      // store passed implicitly to Header
		`<span class="avatar" title="ada@example.com">AL</span>`, // method on *User
		`member since Mar 2025 — 3 items in cart`,                // time.Time method, package function
		`<a href="/products/blk-2">Wool Blanket &lt;XL&gt;</a>`,  // escaped data
		`<li class="product out">`,                               // helper defined in the template
		`<td>$100.50</td>`,                                       // method on *Cart, Money.String
		`With coupon spring</td><td>$90.45</td>`,                 // pipeline, package function
		`Add $149.50 more for free shipping`,                     // constant, Money arithmetic
		`Enamel Mug</a> $18.00 <span class="tag">#kitchen</span> <span class="tag">#camp</span><small> seen 1×</small>`,
	)

	// Edit the template. The next render compiles the new source; the
	// application keeps its state, so the view counter goes on counting.
	original := Views["views/page.gox"]
	t.Cleanup(func() { Views["views/page.gox"] = original })
	Views["views/page.gox"] = strings.Replace(original, `"Welcome to "`, `"Shop at "`, 1)

	render(
		`<h1>Shop at Prairie Goods</h1>`,
		`<small> seen 2×</small>`,
	)

	// The host changes its own data; the template sees it on the next render.
	cart.Lines = append(cart.Lines, CartLine{Product: TheStore.Products[1], Qty: 2})
	render(
		`<td>$358.50</td>`,
		`<td colspan="3">Free shipping</td>`,
	)
}

// The templates are formatted, and the formatter adds a missing import of
// the application package because it is given the environment's packages.
func TestTemplateFormatting(t *testing.T) {
	env, err := NewEnv()
	if err != nil {
		t.Fatal(err)
	}
	if len(Views) != 4 {
		t.Fatalf("got %d templates, want 4", len(Views))
	}
	for name, source := range Views {
		formatted, err := env.Format(name)
		if err != nil {
			t.Fatal(err)
		}
		if formatted != source {
			t.Errorf("%s is not formatted; want:\n%s", name, formatted)
		}
	}

	const name = "views/cart.gox"
	original := Views[name]
	t.Cleanup(func() { Views[name] = original })
	Views[name] = strings.Replace(original, "\t\"app\"\n", "", 1)
	_, err = env.Render(context.Background(), TheStore, &User{}, &Cart{}, "")
	if err == nil || !strings.Contains(err.Error(), name+":") || !strings.Contains(err.Error(), `add import "app"`) {
		t.Errorf("want a compile error in %s naming the missing import, got: %v", name, err)
	}
	if formatted, err := env.Format(name); err != nil {
		t.Fatal(err)
	} else if formatted != original {
		t.Errorf("formatting did not restore the import:\n%s", formatted)
	}
}
