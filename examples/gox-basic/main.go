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

	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/gox"
	"github.com/andreyvit/evm/values"
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
	storeCodec := must(evmbind.Codec[*Store](env.Schema))
	userCodec := must(evmbind.Codec[*User](env.Schema))
	cartCodec := must(evmbind.Codec[*Cart](env.Schema))

	user := &User{Name: "Ada Lovelace", Email: "ada@example.com", Admin: true, Joined: time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)}
	cart := &Cart{Coupon: "SPRING", Lines: []CartLine{
		{Product: TheStore.Products[0], Qty: 2},
		{Product: TheStore.Products[2], Qty: 1},
	}}
	args := [4]values.Value{storeCodec.Pack(TheStore), userCodec.Pack(user), cartCodec.Pack(cart)}
	for _, query := range []string{"", "lamp"} {
		args[3] = values.String(query)
		html := must(page.Render(context.Background(), args[:], gox.RenderOptions{Strict: true}))
		fmt.Fprintln(out, html)
	}
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
