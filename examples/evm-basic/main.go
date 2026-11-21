// Command evm-basic runs a plugin written in Go inside a Go program.
//
// shop.go is the host application. _plugin/bonus.go is the plugin: it uses
// the host's types, methods, functions and constants as package "shop". This
// file binds the host package, compiles the plugin once, and calls it for
// each order.
//
//	go run ./examples/evm-basic
package main

import (
	"context"
	"embed"
	"fmt"
	"io"
	"os"
	"reflect"
	"time"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmstd"
	"github.com/andreyvit/evm/values"
)

// The host's own source tells the compiler what package "shop" declares.
//
//go:embed shop.go
var shopSource embed.FS

// The underscore keeps the go tool from building the plugin as host code.
//
//go:embed _plugin/bonus.go
var pluginSource string

func main() {
	run(os.Stdout)
}

func run(out io.Writer) {
	// Bind the host package. Every exported type, method, function and
	// constant in shop.go becomes visible. Go cannot look functions or types
	// up by name, so the options supply the functions, plus the types that
	// no function, method or field leads to.
	shopSchema := must(evmbind.New("shop", shopSource, evmbind.Options{
		Functions: map[string]any{"TierThreshold": TierThreshold},
		Types:     []reflect.Type{reflect.TypeFor[Customer](), reflect.TypeFor[Order]()},
	}))
	// What a plugin can import: the host package and the standard packages.
	packages := append(evmstd.Packages(), shopSchema.Bind(nil))

	// Compile and link once. A linked program is immutable and can be shared.
	module := must(evmcompiler.Compile("bonus.go", pluginSource, packages...))
	limits := evm.Limits{Steps: 1_000_000, Duration: time.Second}
	program := must(evm.LinkOptions{DefaultLimits: &limits}.Link(module, packages...))
	customerCodec := must(evmbind.Codec[*Customer](shopSchema))
	orderCodec := must(evmbind.Codec[*Order](shopSchema))
	onOrder := must(program.LookupFunction("OnOrder", &evm.Signature{Params: []*values.Type{customerCodec.Type(), orderCodec.Type(), values.TimeType}, Results: []*values.Type{values.StringType}}))

	ada := &Customer{Name: "Ada", Tier: "basic", Joined: time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)}
	now := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	for _, order := range []*Order{
		{ID: "A-1", Cents: 4200, Items: []Item{{SKU: "MUG-1", Qty: 2}}},
		{ID: "A-2", Cents: 9900, Items: []Item{{SKU: "GIFT-CARD", Qty: 1}}},
		{ID: "A-3", Cents: 1500, Items: []Item{{SKU: "LMP-3", Qty: 1}}},
	} {
		func() {
			vm := program.AcquireVM(context.Background())
			defer vm.Release()
			slots := [4]values.Value{customerCodec.Pack(ada), orderCodec.Pack(order), values.NativeTime(now)}
			ensure(vm.Run(onOrder, slots[:]))
			fmt.Fprintln(out, slots[3].AsString())
		}()
	}
	fmt.Fprintln(out, ada.Name, "has", ada.Points, "points after", ada.History)
}

func must[T any](value T, err error) T {
	ensure(err)
	return value
}

func ensure(err error) {
	if err != nil {
		panic(err)
	}
}
