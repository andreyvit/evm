// Command gox-basic renders a page with GoX templates that use an ordinary
// Go application directly: its types, methods, package functions and
// constants.
//
// app.go is the application. views/ holds the templates, one package in
// several files. render.go exposes the application to templates. This file
// compiles the templates once and renders the page for two requests.
//
//	go run ./examples/gox-basic
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/andreyvit/evm/gox"
)

func main() {
	run(os.Stdout)
}

func run(out io.Writer) {
	env := NewEnv()

	// Compile and link once. "Page" names the function Render runs. A linked
	// program is immutable and renders concurrently.
	tmpl := must(env.Options.CompileFiles("Page", Views, env.Packages...))
	page := must(tmpl.Link(env.Packages...))

	user := &User{Name: "Ada Lovelace", Email: "ada@example.com", Admin: true, Joined: time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)}
	cart := &Cart{Coupon: "SPRING", Lines: []CartLine{
		{Product: TheStore.Products[0], Qty: 2},
		{Product: TheStore.Products[2], Qty: 1},
	}}
	for _, query := range []string{"", "lamp"} {
		// Arguments are ordinary Go values, in the order of Page's
		// parameters after the builder. Templates work on the host's own
		// structs, not on copies.
		html := must(page.RenderNative(context.Background(), []any{TheStore, user, cart, query}, gox.RenderOptions{Strict: true}))
		fmt.Fprintln(out, html)
	}
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
