# EVM

**Safe and fast embedded Go.** Compile Go source at run time and run it inside
a Go application, on the application's own data, within limits the application
sets.

EVM is a register-based VM with an optimizing Go compiler. Scripts call host
functions and methods directly: through precompiled bindings that avoid
reflection, or through reflection with no build step. Memory and garbage
collection are shared with the host, so a script reads and writes the host's
own structs, slices and maps, and nothing is serialized at the boundary.

The repository holds three parts:

- **EVM** runs a subset of Go: a compiler, a VM, bindings to host code and a
  standard library.
- **[GoX](gox/README.md)** is typed HTML templates: HTML inside Go functions,
  compiled through EVM.
- **[KVO](kvo/README.md)** is typed data records with efficient binary
  encodings: one model, read and edited as a Go struct or as an encoded row,
  from Go, scripts and templates.

A plugin is ordinary Go. It uses the host's types and calls the host's
methods:

<!-- excerpt: examples/evm-basic/_plugin/bonus.go -->
```go
func OnOrder(c *shop.Customer, o *shop.Order, now time.Time) string {
	ordersSeen++

	points := o.Cents / 100 * shop.PointsPerDollar
	if slices.ContainsFunc(o.Items, isGift) {
		points *= 2
	}
	// ...
	c.AddPoints(points, "order "+o.ID)
	// ...
}
```

The host compiles it against the packages it chooses to expose, links it, and
calls it with its own values:

<!-- excerpt: examples/evm-basic/main.go -->
```go
module := must(evmcompiler.Compile("bonus.go", pluginSource, packages...))
program := must(evm.Link(module, packages...))
// ...
vm := evm.New(program)
budget := evm.Budget{Steps: 1_000_000, Duration: time.Second}
// ...
ensure(vm.RunEntryNative(context.Background(), "OnOrder", []any{ada, order, now}, result[:], budget))
```

[Basic usage](#basic-usage) walks through this example, a page of templates
and a record model. All three follow one pattern:

1. Write the application in plain Go.
2. Expose its packages. `evmbind.New` reads a package's own source and
   exposes what it exports.
3. Compile plugin or template source against those packages. Type errors
   are surfaced here.
4. Link. The result is an immutable program.
5. Run the program with ordinary Go values, as often as needed.

Requires Go 1.27+.

```sh
go get github.com/andreyvit/evm
```

## Why

- **Change behavior without a deploy.** Reward rules, reports, pages and emails
  change more often than the host binary. Compile them at run time.
- **Plugins are Go.** They use the host's language and the host's types. A
  plugin calls `c.AddPoints(...)` on the host's own `*Customer`. There is no
  second data model and no serialization at the boundary.
- **Checked before it runs.** The compiler checks a plugin against the exposed
  API. A plugin that names a missing field or passes a wrong type does not
  compile.
- **The host decides what a plugin can reach.** A plugin imports only the
  packages the host supplies. It has no goroutines and no `unsafe`, and no
  file, network or process access unless the host exposes it.
- **A runaway plugin stops.** Each call has limits on instructions,
  allocations, time, call depth and output.
- **Fast enough for the request path.** Scripts compile to typed register
  bytecode that computes on unboxed machine words. Small host calls run straight from
  registers through generated bindings, and a warm call allocates only what
  the script allocates. On five benchmark scripts EVM ran 6–15× as fast as
  Yaegi and 3–12× as fast as GopherLua, and between 1.4× faster and 1.9×
  slower than C Lua 5.5. LuaJIT with tracing on stayed 1.4–13× ahead. GoX
  rendered six application templates in 27–40% less time than cached
  `html/template`. The [measurements](#performance) are dated November 2026,
  with hardware, inputs and commands.
- **An agent can write the plugin.** Give it the printed package
  declarations. The host compiles and checks what it wrote, then loads it into
  the running application: a new reward rule, a report, a changed workflow.
  The host still chooses the API and the limits. Review, activation and
  permissions stay application policy.
- **Go as a query language.** A host can offer a call that runs a one-off
  script over its data: scan, join, aggregate or update with ordinary Go, the
  application's models and its helpers. A new question needs no new endpoint.

EVM limits what a plugin reaches and how long it runs. It is not a process
isolation boundary; see [trust](#trust).

## Features

EVM:

- Sequential Go: structs, slices, maps, strings, closures, interfaces,
  generics, range functions, `defer`/`panic`/`recover`, package variables,
  `init`, packages of several files.
- No goroutines, channels, `unsafe` or arbitrary native imports. Unsupported
  constructs fail compilation.
- Host packages bound from their own source: types, fields, methods,
  functions, constants and variables.
- Shared data: host pointers keep their identity; slices and maps keep their
  backing storage.
- Reflection bindings with no build step, or generated bindings that call Go
  directly.
- Standard packages: `fmt`, `strings`, `strconv`, `math`, `errors`, `log`,
  `time`, `slices`, `maps`, `sort`, `regexp`, `net/url` and
  [more](evmstd/API.md).
- Limits per session and per call. Errors carry source positions.
- Execution coverage, disassembly, printed package declarations.

GoX:

- HTML inside Go functions. Components are functions; attributes are typed
  arguments; slots are typed callbacks.
- Escaping by context: HTML text, attributes, URLs, JavaScript, CSS.
- Implicit arguments, optional arguments, pipelines.
- HTML, plain text and CSS output.
- Concurrent renders from one linked program.
- `gox fmt`, `gox check` and [editor support](_editors).

KVO:

- Models of typed properties with stable numeric codes.
- One record API over Go structs, Fast2 and Compact2 rows, MessagePack rows and
  decoded memory.
- Typed scalars, lists, maps and child records, read without decoding the row.
- Editors that write to a struct or build a new row.
- The same records as typed values in scripts and templates.

## Basic usage

### EVM: run a plugin

The plugin is ordinary Go. It imports the host application as package `shop`.

<!-- excerpt: examples/evm-basic/_plugin/bonus.go -->
```go
package plugin

import (
	"fmt"
	"shop"
	"slices"
	"strings"
	"time"
)

// Package variables live as long as the VM session that runs the plugin.
var ordersSeen int

// OnOrder awards points for an order and returns a line for the log.
func OnOrder(c *shop.Customer, o *shop.Order, now time.Time) string {
	ordersSeen++

	points := o.Cents / 100 * shop.PointsPerDollar
	if slices.ContainsFunc(o.Items, isGift) {
		points *= 2
	}
	if now.Sub(c.Joined) >= 365*24*time.Hour {
		points += 50 // loyalty bonus after a year
	}
	c.AddPoints(points, "order "+o.ID)

	for _, tier := range []string{"gold", "silver"} {
		if c.Points >= shop.TierThreshold(tier) {
			c.Tier = tier
			break
		}
	}
	return fmt.Sprintf("#%d %s: +%d points, %d total, %s", ordersSeen, o.ID, points, c.Points, c.Tier)
}

func isGift(item shop.Item) bool { return strings.HasPrefix(item.SKU, "GIFT-") }
```

The host application is plain Go with no knowledge of EVM:

<!-- excerpt: examples/evm-basic/shop.go -->
```go
// PointsPerDollar is the base earning rate.
const PointsPerDollar = 10

type Customer struct {
	Name    string
	Tier    string
	Points  int64
	Joined  time.Time
	History []string
}

// AddPoints credits the customer and records why.
func (c *Customer) AddPoints(points int64, reason string) {
	c.Points += points
	c.History = append(c.History, reason)
}
// ...
// TierThreshold returns the points a customer needs to hold a tier.
func TierThreshold(tier string) int64 {
```

The host embeds both sources, binds its own package, compiles and links the
plugin once, then calls it for each order:

<!-- excerpt: examples/evm-basic/main.go -->
```go
// The host's own source tells the compiler what package "shop" declares.
//
//go:embed shop.go
var shopSource embed.FS

// The underscore keeps the go tool from building the plugin as host code.
//
//go:embed _plugin/bonus.go
var pluginSource string
// ...
// Bind the host package. Every exported type, method, function and
// constant in shop.go becomes visible. Go cannot look functions or types
// up by name, so the options supply the functions, plus the types that
// no function, method or field leads to.
shop := must(evmbind.New("shop", shopSource, evmbind.Options{
	Functions: map[string]any{"TierThreshold": TierThreshold},
	Types:     []reflect.Type{reflect.TypeFor[Customer](), reflect.TypeFor[Order]()},
}))
// What a plugin can import: the host package and the standard packages.
packages := append(evmstd.Packages(), shop.Bind(nil))

// Compile and link once. A linked program is immutable and can be shared.
module := must(evmcompiler.Compile("bonus.go", pluginSource, packages...))
program := must(evm.Link(module, packages...))

// A VM is one session: it owns the plugin's package variables. Each call
// gets a budget, so a runaway plugin stops with an error.
vm := evm.New(program)
budget := evm.Budget{Steps: 1_000_000, Duration: time.Second}

ada := &Customer{Name: "Ada", Tier: "basic", Joined: time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)}
now := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
for _, order := range []*Order{
	{ID: "A-1", Cents: 4200, Items: []Item{{SKU: "MUG-1", Qty: 2}}},
	{ID: "A-2", Cents: 9900, Items: []Item{{SKU: "GIFT-CARD", Qty: 1}}},
	{ID: "A-3", Cents: 1500, Items: []Item{{SKU: "LMP-3", Qty: 1}}},
} {
	// Arguments and results are ordinary Go values. The plugin works on
	// the host's own *Customer, not on a copy.
	var result [1]any
	ensure(vm.RunEntryNative(context.Background(), "OnOrder", []any{ada, order, now}, result[:], budget))
	fmt.Fprintln(out, result[0])
}
fmt.Fprintln(out, ada.Name, "has", ada.Points, "points after", ada.History)
```

`go run ./examples/evm-basic` prints:

<!-- excerpt: examples/evm-basic/main_test.go -->
```text
#1 A-1: +470 points, 470 total, basic
#2 A-2: +2030 points, 2500 total, gold
#3 A-3: +200 points, 2700 total, gold
Ada has 2700 points after [order A-1 order A-2 order A-3]
```

Notes:

- `must` and `ensure` panic on an error. They are defined at the bottom of
  [`main.go`](examples/evm-basic/main.go).
- `evmbind.New` reads `shop.go` itself: declarations, parameter names and doc
  comments. It does not run the source. There is no second description of the
  API to keep in step.
- `shop.Bind(nil)` is the package with its implementations. `Compile` uses the
  declarations; `Link` uses the implementations.
- Compile errors are `evmcompiler.Diagnostics` with file, line and column. A
  plugin that imports a package the host did not supply does not compile.
- A program is immutable; many VMs can share it. A VM is one session and
  serves one goroutine at a time. `ordersSeen` counts across calls because the
  loop reuses the VM. `vm.Reset()` clears it.
- `RunEntryNative` checks each argument against the Go type of its parameter.
  It makes no numeric conversions: pass `int64(5)` for an `int64`.
- The plugin changed `ada` through `c.AddPoints` and `c.Tier = tier`. The
  host reads the result from its own struct.
- A call that exceeds its budget returns an error. The session then refuses
  calls until `vm.Reset()`.
- To activate a new version, compile and link the new source and create VMs
  from the new program. Existing sessions keep the old one.

### GoX: render a template

A template is a Go function that takes a `*gox.Builder` and holds markup. This
is the page of [examples/gox-basic](examples/gox-basic):

<!-- excerpt: examples/gox-basic/views/page.gox -->
```gox
package views

import (
	"app"

	"github.com/andreyvit/evm/gox"
)

func Page(b *gox.Builder, store *app.Store, user *app.User, cart *app.Cart, q string) {
	<Layout title={store.Name}>
		<slot-styles>
			<style>.out { opacity: .5 }</style>
		</slot-styles>
		<slot-content>
			<Header user={user} cart={cart} />
			<form><input name="q" value={q} placeholder="Search"></form>
			<ProductList q={q} />
			<CartSummary cart={cart} />
		</slot-content>
	</Layout>
}

// Callers do not pass store. *app.Store is an implicit type, so Header and
// ProductList receive the store of whichever component calls them.
func Header(b *gox.Builder, store *app.Store, user *app.User, cart *app.Cart) {
	<header>
		<h1>
			"Welcome to "
			store.Name
		</h1>
		<span class="avatar" title={user.Email}>user.Initials()</span>
		" "
		user.Name
		if user.Admin {
			" (admin)"
		}
		", member since "
		user.Joined | app.FormatDate
		" — "
		app.Pluralize(cart.Count(), "item", "items")
		" in cart"
	</header>
}
```

The layout takes its content as slots:

<!-- excerpt: examples/gox-basic/views/layout.gox -->
```gox
//gox:arg styles optional
func Layout(b *gox.Builder, title string, styles func(), content func()) {
	<html>
		<head>
			<title>title</title>
			<style>.tag { font-size: 12px }</style>
			if styles != nil {
				styles()
			}
		</head>
		<body>content()</body>
	</html>
}
```

A product row mixes markup, Go control flow and a helper:

<!-- excerpt: examples/gox-basic/views/products.gox -->
```gox
func ProductRow(b *gox.Builder, store *app.Store, p *app.Product) {
	<li class="product {stockClass(p)}">
		<a href={p.URL()}>p.Name</a>
		" "
		p.Price | app.FormatMoney
		if !p.InStock() {
			<em>" sold out"</em>
		}
		for _, tag := range p.Tags {
			" "
			<span class="tag">
				"#"
				tag
			</span>
		}
		<small>
			" seen "
			p.Viewed() | strconv.Itoa
			"×"
		</small>
	</li>
}

func stockClass(p *app.Product) string {
	if p.InStock() {
		return "in"
	}
	return "out"
}
```

The host exposes its application package to templates, as it does for a
plugin, and adds GoX options:

<!-- excerpt: examples/gox-basic/render.go -->
```go
// Go cannot look functions or types up by name, so the options supply
// the functions, plus the types that no function, method or field
// leads to.
app := must(evmbind.New("app", appSource, evmbind.Options{
	Functions: map[string]any{
		"Pluralize":   Pluralize,
		"Discount":    Discount,
		"FormatMoney": FormatMoney,
		"FormatDate":  FormatDate,
	},
	Types: []reflect.Type{
		reflect.TypeFor[Store](),
		reflect.TypeFor[User](),
		reflect.TypeFor[Cart](),
	},
}))
return &Env{
	Packages: []evm.Package{app.Bind(nil)},
	Options: gox.Options{
		// A component that declares a *app.Store parameter receives
		// its caller's store without being passed it.
		// FormatMoney takes the store the same way, so a template
		// writes price | app.FormatMoney.
		ImplicitTypes: []string{"*app.Store"},
	},
}
```

Then it compiles and links once, and renders for each request:

<!-- excerpt: examples/gox-basic/main.go -->
```go
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
```

`go run ./examples/gox-basic` prints the page twice, once per query.

Notes:

- Inside an element, a standalone expression prints. Literal text is a quoted
  string. `if` and `for` are Go.
- A component is a function. `<Header user={user} cart={cart} />` calls
  `Header`; the attributes are its arguments, checked by type.
- A slot is a `func()` parameter. `<slot-content>` supplies `content`, and
  `Layout` decides where to call it. `//gox:arg styles optional` lets a caller
  leave `styles` out.
- Nobody passes `store` to `Header`: `*app.Store` is in `ImplicitTypes`, so a
  component takes it from its caller's scope.
- Text positions take strings and trusted content, so the template says how
  each value reads: `p.Price | app.FormatMoney` calls
  `app.FormatMoney(p.Price, store)`,
  with the store filled in as an implicit argument. A bare `p.Price` does not
  compile.
- Values are escaped for where they land. The product named
  `Wool Blanket <XL>` renders as `Wool Blanket &lt;XL&gt;`.
- Each file imports what it names: `app` here, `strconv` or `strings` elsewhere.
  `gox fmt` adds missing imports.
- `Strict: true` turns a nil field in an output expression into an error.
  Without it, GoX reports the problem and leaves that output out.
- Templates recompile at run time. Replace a file's text in `Views`, compile
  and link again, and render with the new program; the
  [test](examples/gox-basic/render_test.go) does this.

### KVO: read and edit a record

KVO keeps three things apart: what a record means, how it is stored, and who
reads it.

- A **schema** holds properties and models.
- A **property** has a stable numeric code, a name such as
  `customer:points_earned` and a type. Declare it once and get a typed
  reference: `kvo.NewSignedProp[int64](...)`.
- A **model** lists the properties of one record type.
- A **record**, `kvo.Record`, is a 16-byte handle to one value of a model. It
  views the data where it is; it holds no decoded copy.
- A **binding** ties a model to a Go struct type and makes records.

A record's data has one of these representations:

| Representation | Record from | Reads and edits |
| --- | --- | --- |
| Go struct | `binding.Record(reflect.ValueOf(ptr))` | Read and write the struct's fields |
| Fast2 or Compact2 row | `binding.Encoded(row2.Open(bytes))` | Read fields from the bytes; an edit builds a new row |
| MessagePack row | `binding.MessagePack(bytes)` | Scan the encoded map by `msgpack` names; an edit rewrites the changed path |
| Decoded memory | `record.Fastest()` on a row whose model has no Go struct | Read indexed, owned values |

Fast2 and Compact2 are the current row formats. Fast2 favors access speed,
Compact2 size, and a Fast2 row can hold Compact2 children. MessagePack and
Fast1 are supported legacy formats; `kvo/codec/fast1` has its own API.

The same `Get`, `Edit` and script field access work on every representation.
Code that takes a `kvo.Record` does not know which one it has.

This repository has no database. A database such as
[edb](https://github.com/andreyvit/edb) stores the rows and hands out
records. A model also serves values that are never stored: a script takes a
Go struct as a typed record the same way.

#### The example

The model, with a Go struct for it:

<!-- excerpt: examples/kvo-basic/model.go -->
```go
// Customer is the Go struct form of a customer. A kvo tag binds a field to a
// property by name; a msgpack tag names its key in MessagePack rows.
type Customer struct {
	Name     string    `kvo:"name" msgpack:"name"`
	Tags     []string  `kvo:"tags" msgpack:"tags"`
	Earned   int64     `kvo:"points_earned" msgpack:"points_earned"`
	Redeemed int64     `kvo:"points_redeemed" msgpack:"points_redeemed"`
	Joined   time.Time `kvo:"joined" msgpack:"joined"`
}

// Declare each property once, with a stable code and a type. A model lists
// the properties of one record type. Build all of this at startup.
var (
	Schema       = kvo.NewSchema()
	CustomerType = kvo.NewEntityType(Schema, "customer", reflect.TypeFor[Customer]())

	Customer_Name = kvo.NewStringProp[string](Schema, 1, "customer:name", kvo.TMachineString)
	Customer_Tags = kvo.NewListProp(Schema, 2, "customer:tags",
		kvo.ListOf(kvo.TMachineString), values.StringCodec[string]())
	Customer_Earned   = kvo.NewSignedProp[int64](Schema, 3, "customer:points_earned", kvo.TInt64)
	Customer_Redeemed = kvo.NewSignedProp[int64](Schema, 4, "customer:points_redeemed", kvo.TInt64)
	Customer_Joined   = kvo.NewTimeProp(Schema, 5, "customer:joined")

	CustomerModel = kvo.NewModel(Schema, CustomerType, func(b *kvo.ModelBuilder) {
		b.Prop(Customer_Name)
		b.Prop(Customer_Tags)
		b.Prop(Customer_Earned)
		b.Prop(Customer_Redeemed)
		b.Prop(Customer_Joined)
	})

	// Customers makes records of the model from structs and from rows.
	Customers = kvo.Bind(reflect.TypeFor[Customer](), CustomerModel)
)

// PointBalance takes a record in any representation. Scripts call it as a
// method: customer.PointBalance().
func PointBalance(c kvo.Record) int64 {
	return c.Get(Customer_Earned) - c.Get(Customer_Redeemed)
}
```

Go code makes three records, one per representation, then reads and edits
them the same way:

<!-- excerpt: examples/kvo-basic/main.go -->
```go
// One customer per representation. A database would hand out the two
// encoded rows; here the binding encodes them from structs.
ada := &Customer{Name: "Ada", Tags: []string{"vip"}, Earned: 1250, Redeemed: 250, Joined: date(2024, 1, 15)}
bob := &Customer{Name: "Bob", Tags: []string{"newsletter"}, Earned: 300, Redeemed: 100, Joined: date(2025, 2, 1)}
cyd := &Customer{Name: "Cyd", Earned: 80, Joined: date(2025, 2, 20)}
bobRow := Customers.Encode(nil, reflect.ValueOf(bob).Elem(), row2.Fast2)
cydRow := msgpack.Encode(nil, reflect.ValueOf(cyd))

// A record is a 16-byte handle that views the struct or the row's bytes
// in place. Opening a row decodes nothing.
records := []kvo.Record{
	Customers.Record(reflect.ValueOf(ada)),
	Customers.Encoded(row2.Open(bobRow)),
	Customers.MessagePack(cydRow),
}

// Go reads typed properties the same way from every representation.
for _, c := range records {
	name := c.Get(Customer_Name)     // string
	tags := c.Get(Customer_Tags)     // kvo.List[string]
	joined := c.Get(Customer_Joined) // time.Time
	fmt.Fprintf(out, "%s: %d points, %d tags, joined %s\n", name, PointBalance(c), tags.Len(), joined.Format(time.DateOnly))
}

// Go edits through an editor. Editing a struct writes to the struct.
// Editing a row builds a new row and leaves the old bytes alone.
for i, c := range records {
	edit := c.Edit()
	edit.Set(Customer_Earned, edit.Get(Customer_Earned)+100)
	edit.List(Customer_Tags).AddUnique("welcomed")
	records[i] = edit.Record()
}
```

A script sees the schema as package `m`. A model is a type, a property is a
field, and an editor is the `Edit` type:

<!-- excerpt: examples/kvo-basic/_script/rules.go -->
```go
package rules

import (
	"fmt"
	"m"
	"time"
)

// Describe reads a customer. Package m comes from the KVO schema: each model
// is a type and each property is a field. The same code runs on a Go struct
// and on an encoded row.
func Describe(c *m.Customer, now time.Time) string {
	status := "member"
	for _, tag := range c.tags {
		if tag == "vip" {
			status = "VIP"
		}
	}
	days := int(now.Sub(c.joined).Hours() / 24)
	return fmt.Sprintf("%s: %s for %d days, %d points", c.name, status, days, c.PointBalance())
}

// Redeem edits a customer. The host decides what an edit means: here it
// writes to the Go struct or builds a new row.
func Redeem(c *m.CustomerEdit, points int64) bool {
	if points <= 0 || points > c.points_earned-c.points_redeemed {
		return false
	}
	c.points_redeemed += points
	return true
}
```

The host builds package `m` from the schema, compiles the script once and
runs it on the same three records:

<!-- excerpt: examples/kvo-basic/main.go -->
```go
// Scripts see the schema as package "m": type m.Customer with a field
// per property. PointBalance becomes a method of it.
models := must(evm.ModelPackage("m", Schema))
ensure(evmbind.RegisterMethods(&models, []evmbind.Method{
	evmbind.RecordMethod(CustomerModel, "PointBalance", PointBalance, evmbind.BorrowOnly),
}, evmbind.MethodOptions{}))
packages := append(evmstd.Packages(), models)

// Compile and link once.
module := must(evmcompiler.Compile("rules.go", rulesSource, packages...))
program := must(evm.Link(module, packages...))
vm := evm.New(program)
budget := evm.Budget{Steps: 100_000, Duration: time.Second}

// The script reads the same records. Nothing is converted or copied.
now := date(2025, 3, 1)
for _, c := range records {
	var result [1]any
	ensure(vm.RunEntryNative(context.Background(), "Describe", []any{c, now}, result[:], budget))
	fmt.Fprintln(out, result[0])
}

// The script edits through the host's editors. They live in a record
// scope, which a host opens for the length of a transaction.
ctx, scope := evm.WithRecords(context.Background())
defer scope.Close()
for i, c := range records {
	edit := c.Edit()
	var result [1]any
	ensure(vm.RunEntryNative(ctx, "Redeem", []any{scope.EditRecord(CustomerModel, edit), int64(250)}, result[:], budget))
	records[i] = edit.Record()
	fmt.Fprintf(out, "%s redeems 250: %v, balance %d\n", c.Get(Customer_Name), result[0], PointBalance(records[i]))
}
fmt.Fprintf(out, "Ada's struct: %d redeemed\n", ada.Redeemed)
```

`go run ./examples/kvo-basic` prints:

<!-- excerpt: examples/kvo-basic/main_test.go -->
```text
Ada: 1000 points, 1 tags, joined 2024-01-15
Bob: 200 points, 1 tags, joined 2025-02-01
Cyd: 80 points, 0 tags, joined 2025-02-20
Ada's struct: 1350 earned, tags [vip welcomed]
Bob's old row: 300 earned; new row: 400 earned, tags [newsletter welcomed]
Ada: VIP for 411 days, 1100 points
Bob: member for 28 days, 300 points
Cyd: member for 9 days, 180 points
Ada redeems 250: true, balance 850
Bob redeems 250: true, balance 50
Cyd redeems 250: false, balance 180
Ada's struct: 500 redeemed
```

Notes:

- `c.Get(Customer_Earned)` returns an `int64`, not a boxed value. The
  property reference carries the Go type.
- A property's name is `customer:points_earned`; its local name in the model
  is `points_earned`. The struct tag and the script field use the local name.
- Editing Ada changed the struct `ada`. Editing Bob left `bobRow` alone:
  `edit.Record()` is a new row.
- `PointBalance` is one Go function. Go calls it with a record; the script
  calls it as `c.PointBalance()`.
- The script never names a representation. `Describe` ran on a struct, a
  Fast2 row and a MessagePack row.
- A read-only record needs no scope. An editor does: `evm.WithRecords` opens
  one, and `scope.EditRecord` registers the editor for the script.
- Records do not save themselves, and a record holds its database key only
  when the key is one of its properties. The host owns lookup and saving.

## Advanced usage

One line per capability. The linked documents hold the contracts.

### EVM

Details: [host contracts](_docs/embedding.md), [`evmbind`](evmbind/README.md),
[hand-written bindings](_docs/bindings.md), [`values`](values/README.md),
[standard packages](evmstd/API.md).

#### Compile

- `evmcompiler.Compile(filename, source, packages...)` compiles one file. The
  package clause is optional.
- `evmcompiler.CompileFiles(map[string]string{...}, packages...)` compiles one
  package of several files.
- A compile error is an `evmcompiler.Diagnostics`; each `Diagnostic` has a
  `Position` and a `Message`.
- Source is limited to `evmcompiler.MaxSourceSize`, 1 MiB for all files of
  one compilation.
- Nesting is limited, because compile cost grows faster than the source with
  it: types 32 levels (`MaxTypeNesting`), one expression 500
  (`MaxExprNesting`; `a+b+c` is two), statements inside a function 100
  (`MaxStmtNesting`; an else-if chain counts each `if`), and function literals
  32 (`MaxFuncNesting`). A constant string expression may reach 1 MiB
  (`MaxConstStringSize`), estimated from literal lengths and `+` before the
  type checker folds it. Past a limit, compilation returns a diagnostic at the
  first level too deep.
- Branching is limited too, for a package as a whole (`MaxBranchWeight`):
  some costs of a function grow with the square of its branches, and with its
  branches times its local variables. One function may hold about 4,000
  `if` statements in a row, a `switch` of 2,300 cases, or a `switch` of 450
  cases that each assign a variable of their own; functions add up. The
  diagnostic is at the function that goes past the limit. Splitting a
  function into several lowers the total; a package that is past the limit
  as a whole has to be split.
- Scripts are Go 1.23: generics, range over integers and functions.
- `evmcompiler.Options{NoOptimization: true}.Compile(...)` keeps the code
  unoptimized for debugging.
- `evmcompiler.NewEnvironment(packages...)` type-checks the packages once.
  Its `Compile` and `CompileFiles` then check only the script, so a host that
  compiles many scripts against the same large package set should build one
  `Environment` and keep it. It never changes and is safe for concurrent use.
  It fails if any package cannot be imported; the package-list functions fail
  only a script that imports such a package. Both produce the same code.
- `module.Disassemble()` prints bytecode. `evmasm.Assemble(source)` builds a
  module from assembly, for tests and diagnostics.
- `evmcompiler.Analyze`, `AnalyzeFiles` and `SourceImporter` type-check with
  `go/types` against host packages, for tools. An `Environment` has `Analyze`
  and `AnalyzeFiles` too; parse into a file set from its `NewFileSet`.
  `AnalyzeFiles` applies the limits; `evmcompiler.CheckLimits` applies them
  alone and reports `token.Pos` positions.
- `evmcompiler.PackageSource(path, packages...)`, `PackagesSource` and
  `PackagePaths` print the declarations a script sees: documentation for a
  person or an agent writing a plugin.
- `evmcompiler.DeclarationOptions{Annotations: fn}.PackageSource(...)` chooses
  which comment lines to print for each function.

#### Link and run

- `evm.Link(module, packages...)` returns an immutable `*evm.Program`.
  `evm.LinkWithHostData(module, data, packages...)` attaches host data, read
  with `program.HostData()`.
- `evm.NewLinkEnvironment(packages...)` validates and binds the packages
  once. Its `Link` and `LinkWithHostData` then do only what the module needs,
  so a host that links many modules against the same large package set should
  build one `LinkEnvironment` and keep it. It never changes and is safe for
  concurrent use. Both ways produce the same program.
- `env.NewLinkEnvironment(packages...)` on an `evmcompiler.Environment` also
  shares the linked storage shapes of the host types among the programs of
  the modules that environment compiles. Pass the bound packages; a program
  then keeps only the tables of its own module.
- `evm.New(program)` creates a session. Initialization runs once, before the
  first call. Package variables persist until `vm.Reset()`.
- `vm.RunEntryNative(ctx, "Entry", []any{...}, results, budget)` passes and
  returns Go values.
- `vm.Run(ctx, "Entry", args, results)` and `vm.RunWithBudget(...)` use
  `values.Value`: `values.Int(5)`, `result.AsInt()`.
- `program.EntrySignature("Entry")` returns the parameter and result types.
- `vm.RunNative(ctx, func(c *evm.Call) error { ... }, budget)` runs host code
  inside the session. There, `c.NativeArguments(types, args)` converts Go
  values, `c.RunEntry("Entry", args, results)` calls an entry and
  `c.NativeAny(value)` converts an interface result.
- A VM serves one goroutine at a time and is not reentrant; a second call
  returns `evm.ErrReentry`. Use one VM per goroutine.
- Interface values, function values and type IDs belong to one program. Pack
  them again for another program.

#### Host packages from source

- `evmbind.New("shop", source, evmbind.Options{...})` exposes everything the
  source exports. `schema.Package()` holds the declarations;
  `schema.Bind(host)` adds the implementations.
- `Options.Functions: map[string]any{"Lookup": Lookup}` binds package
  functions. `Options.Host`, a `reflect.Type`, binds them to methods of a
  host object instead; `schema.Bind(host)` supplies the object. Such a method
  takes a leading `*evm.Call`.
- `Options.Types` adds Go types that no function, method or field leads to.
- `Options.Values: map[string]any{"Config": Config}` exposes package
  variables. Scripts read them; they cannot assign them.
- `Options.Omit: []string{"Secret", "User.Token"}` leaves out a type,
  function, constant or method.
- `Options.Only: []string{"Order"}` and `Options.Selective` expose only what
  the options name, to pick declarations out of a large package.
- `Options.Methods`, `Options.Fields` and `Options.OmitFields` select methods
  and fields per type. `Options.TypeNames` renames types for scripts.
- `Options.Imports: map[string]*evmbind.Schema{...}` shares types between
  schemas. `evmstd.Imports()` supplies `time.Duration`, `*url.URL` and the
  other standard types.
- `Options.Instances` binds instances of a generic struct, such as
  `Box[int]`. `Options.GenericFunctions` binds compiled instances of a
  generic function.
- `Options.Implementations: []evmbind.Implementation{evmbind.Pointer[Tx]()}`
  admits a Go type into script interfaces. `schema.Interface(program, ptr)`
  packs one as an argument; `schema.NativeInterface(program, value)` unpacks.
- `schema.NativePointer(ptr)` makes a `values.Value` argument from a struct
  pointer, for `vm.Run`.
- `schema.Type("Customer")` and `schema.NativeType(goType)` return the VM
  types for hand-written signatures.
- `evmbind.Discover("app", evmbind.Discovery{...})` binds functions and the
  types they reach by reflection, without source. It has no parameter names or
  comments; `Discovery.Constants` supplies constants. It also returns the
  packages of the other types it found, to compile and link with.
- `evm.MergePackages(packages...)` joins packages that contribute to one
  import path. Compile and Link take each path once.

#### Generated native bindings

Without generated code, a host call goes through reflection. Generated
bindings are Go wrappers, compiled into the host, that call its functions,
methods and fields directly.

- `go run github.com/andreyvit/evm/evmbind/cmd/evmbind -dir ../api -path demo
  -native example.org/project/api -package apibindings -o bindings_gen.go`
  generates wrappers for a whole package.
- `evmbind.Generate(path, nativeImport, outputPackage, source, options)` does
  the same from a program and takes the options of `evmbind.New`.
- `Options.Static: &apibindings.Bindings` supplies them to `evmbind.New`.
- `Options.Mode: evmbind.Automatic`, the default, uses the generated wrappers
  when their fingerprint matches the source and falls back to reflection.
  `evmbind.StaticOnly` rejects missing or stale wrappers: `New` panics.
- `schema.UsesStatic()` reports which was chosen.
- The build tag `evmreflect` leaves the generated code out of a build.
- A small call gets a direct-call instruction: a function with up to two
  arguments, or a pointer method with up to one, with at most one result,
  over words, strings and pointers.
- Generated field accessors serve scripts and KVO alike:
  `schema.NativeFields(goType)`, `binding.WithNativeSchema(schema)`.
- `evmbind.GenerateMethods(pkg, methods)` generates adapters for
  [record methods](#records-in-scripts-and-templates).
- Regenerate after the bound declarations change.

#### Hand-written packages

- `evm.Package{Path: "db", Functions: ..., Methods: ..., Types: ...,
  Constants: ..., Values: ..., Aliases: ...}` declares a package by hand.
- `evm.ExternalFunction{Name, Signature, Call}`: `Call` is a
  `func(*evm.Call) error` that reads `c.Argument(0)` and writes
  `c.SetResult(0, value)`.
- `Direct: evm.DirectUnary(func(n uint64) uint64 { ... })` and
  `evm.DirectBinding` bind small calls without an argument window.
- `evm.ExternalMethod{Receiver, Name, Signature, Call}` adds a method to a
  declared type.
- `evm.ExternalValue{Name, Type, Read}` exposes a host-owned package value.
- `evm.TypeDecl{Name, Shape}` declares a type from a `values.Shape`:
  `values.NewStructShape`, `values.NewPointerShape`.
- `evm.TypeDecl.NativeCodec` converts a native field whose Go form differs
  from its VM type.
- `evm.TypeAlias{Name, Target}` gives a type a second name.
- `Package.EntryTypes` lists native types that may enter through interface
  arguments of `Run`.
- A `*evm.Call` and its argument window are valid only during the call. A host
  function honors `c.Context` cancellation itself.

#### Values across the boundary

- Scalars, strings and named scalar types convert directly.
- A struct passes by value. A pointer keeps its Go address: the script and
  the host read and write one object.
- Slices and maps of bound types keep their Go backing storage.
- `time.Time` is one type on both sides. A Go value keeps its zone and
  monotonic reading; `values.Time(t)`, for `vm.Run`, keeps the instant in
  UTC.
- `any` carries bound Go values with their types. A value of a script-only
  type reaches Go as `*evm.GuestValue` and returns to its program unchanged.
- `error` crosses in both directions with its identity. A script-defined
  error needs a live VM to call `Error()`; see callback scopes below.
- Variadic functions and methods work with Go semantics.

#### Functions and callbacks

- A host parameter of type `*evm.FunctionOf[func(int) int]` accepts a script
  function, closure or bound method; Go sees a `*evm.FunctionHandle`.
- `*evm.NamedFunctionOf[func(int) any]` accepts only a public function without
  captures; Go sees a `*evm.NamedFunction`.
- `program.FunctionValue(value)` retains a script callable as a handle.
  `vm.RunFunction(ctx, handle, args, results)`, `vm.RunFunctionWithBudget` and
  `vm.RunFunctionNative` call it later.
- `program.NamedFunctionValue(value)` and `program.EntryFunction(named)`
  resolve a public function.
- Inside a host call, `c.RunCallback(fn)`, `c.RunCallbackArgs(fn, args,
  results)` and `c.RunInterfaceMethod(...)` call back into the script.
- `evm.NewNativeFunctionType(signature)` with `c.NativeFunction(typ, fn)`
  turns a Go closure into a script function value: an iterator or a yield
  function. `program.NativeFunction(typ, fn)` makes one to pass into `Run`.
- A Go function field of a bound struct holds a script function. To call it
  while the VM is idle, do so inside `vm.RunNative`, or open
  `vm.OpenCallbackScope(ctx, budget)` and close it with `scope.Close()`.
- `c.CallbackError(err)` and `c.PanicCallback(err)` keep a script panic
  recoverable when it crosses a Go callback.

#### Limits and budgets

- `vm.Limits = evm.Limits{Steps, Allocations, AllocationBytes, OutputBytes,
  Depth, RSlots, PSlots}` sets session limits. Zero fields take defaults:
  a million steps per call, depth 1,024, a million allocations and 64 MiB per
  session.
- `evm.Budget{Steps, Allocations, AllocationBytes, Duration}` limits one call,
  including initialization.
- `Budget.HardDuration`, `HardAllocations` and `HardAllocationBytes` are
  ceilings. A host function raises the soft limits up to them with
  `c.IncreaseCPUBudget(ms)` and `c.IncreaseMemBudget(bytes, allocs)`; a
  script does the same with `evmruntime.IncreaseCPUBudget` and
  `IncreaseMemBudget`. Set a hard ceiling only to let the limit stretch.
- `Budget.UnlimitedSteps` removes the instruction limit for a call.
- Cancelling `ctx` stops a running call.
- `c.NarrowBudget(steps, duration)` and `c.NarrowCPUBudget(soft, hard)` limit
  a stretch of work inside a call until `scope.End(c)`.
- `c.ChargeAllocation(bytes)` charges host allocations to the call.
- The errors are `evm.ErrSteps`, `ErrDepth`, `ErrRegisters`, `ErrAllocations`,
  `ErrAllocation` and `ErrOutput`; time limits return the context's error.
- Allocation counts are cumulative until `Reset`. They are not live heap
  size: a long-lived session that allocates reaches its limit eventually, so
  reset it between units of work or raise the limit.
- Host functions run outside the meters; only what they charge counts.

#### Errors and panics

- A failed run returns `*evm.ExecutionError{Function, PC, Position, Err}`.
  `errors.Is(err, evm.ErrNil)` sees through it.
- Language errors: `evm.ErrNil`, `ErrBounds`, `ErrNilMap`, `ErrDivideByZero`,
  `ErrNegativeShift`, `ErrTypeAssertion`, `ErrNilFunction`.
- An uncaught `panic(v)` returns an `ExecutionError` that wraps
  `*evm.PanicError`; `errors.As` finds it. It holds the value and its type.
- A script recovers its panics and language errors with `defer` and
  `recover()`.
- Budget, cancellation, invalid-value and native callback errors are fatal:
  script defers do not run.
- After a failed run the session returns `evm.ErrInvalidState` until
  `vm.Reset()`. That includes a `RunEntryNative` argument of the wrong Go
  type; an unknown entry name or a wrong argument count fails before the run
  and leaves the session usable. Results are written only on success. The VM does not roll back
  host side effects.
- A Go panic in a function bound through `evmbind` becomes a fatal error
  with the script's position. In a hand-written `Call` it propagates to the
  caller of `Run` after VM cleanup.
- `c.SetError(i, err)`, `c.ErrorValue(err)` and `c.NativeError(value)` move
  errors across the boundary by hand.

#### Standard packages and output

- Every script can import `errors`, `fmt`, `log`, `math`, `strconv`,
  `strings`, `evmruntime` and `evmruntime/log`. The VM implements them, and a
  basic `time.Time` with formatting and comparison.
- `evmstd.Packages()` adds `bytes`, `cmp`, `encoding/base64`, `encoding/hex`,
  `encoding/json` (validation and reformatting only), `html`, `iter`, `maps`,
  `math/bits`, `net/url`, `path`, `path/filepath`, `regexp`, `slices`, `sort`,
  `time`, `unicode`, `unicode/utf8` and `unicode/utf16`. Pass it to Compile
  and Link. GoX includes it.
- [`evmstd/API.md`](evmstd/API.md) lists every exposed declaration, and every
  one left out with the reason. Nothing reads files, the network or the
  environment. `time.Now` is exposed, and its values carry the host's time
  zone; `time.Local` and `time.LoadLocation` are left out.
- `cmp`, `iter`, `maps`, `slices` and `sort` are script source and are
  metered. The others call Go and run outside the meters.
- A host package with path `time` extends the built-in `time` package.
  Compile and Link merge the two.
- `evm.StandardLibraryPaths()` and `evmstd.Paths()` list the import paths.
- `vm.Output = func(s string) error { ... }` receives `print`, `fmt.Print` and
  `log` output. Nil uses Go's logger. `Limits.OutputBytes` defaults to 1 MiB.

#### JSON

- `program.JSONValue(ctx, value, limit)` and `c.JSONValue(value, limit)`
  convert a script `any` to a Go tree; numbers are `json.Number`.
- `program.EncodeJSON(ctx, value, limit)`, `c.EncodeJSON(value, limit)` and
  `c.EncodeJSONObject(value, limit)` write JSON bytes.
- `record.ToJSON()` in a script returns a detached `map[string]any` of a KVO
  record.

#### Ownership of borrowed data

- The compiler copies a borrowed value, such as a record of an open
  transaction, when a script keeps it past its scope.
- `ExternalFunction.Ownership = &evm.OwnershipEffects{NoRetain: true,
  NoInvalidate: true, Borrowed: ..., Ends: ..., Callbacks: ...}` tells it what
  a host function borrows, retains and invalidates. Without it, the compiler
  assumes the worst.
- `evmbind.BorrowOnly`, `NoRetain` and `NoInvalidate` say the same for a
  record method.
- `values.NewOwnedType` registers another borrowable host type.
- `function.OwnershipSites` lists the copies the compiler inserted.

#### Shared source

- `evm.Package{Path: "ids", Source: source}` supplies Go source that compiles
  as script code and that scripts import.
- `Package.SharedTypes: []evm.SharedType{{Name: "ID", Native:
  reflect.TypeFor[ID](), Type: idType}}` gives a host scalar and its
  source-defined methods one identity in host signatures, KVO fields and
  scripts.
- A declaration without a body binds to an `ExternalFunction` or
  `ExternalMethod` of the same name.
- `ExternalFunction.Instantiate` binds a generic declaration for each set of
  type arguments.
- `evm.TypeDecl{SourceName: "Index", TypeArgs: ...}` maps a source-declared
  type, or one instance of a generic type, to a native shape.

#### Coverage

- `coverage := evm.NewCoverage(program)`, then `vm.SetCoverage(coverage)`.
  `SetCoverage(nil)` turns it off.
- `coverage.Lines()` reports by source line: a line is complete when all its
  instructions ran and each conditional jump went both ways.
- `coverage.Instructions()` gives counts and jump outcomes per instruction.
- Use one collector per concurrent run, then `coverage.Merge(other)`.

#### Performance

- Compile and link once. Keep compilation, linking and binding construction
  out of the loop that runs the program.
- Reuse a VM and its argument and result slices. Once the VM's storage has
  grown, a call allocates only what the script allocates.
- Generated bindings remove reflection from calls and field access. They do
  not remove the cost of a host function or of decoding a row.
- Measure the whole operation. A field read that allocates nothing does not
  make the request around it free.
- Reports: [EVM](_docs/benchmarks.md), [bindings](_docs/bindings.md),
  [record methods](_docs/bench-record-methods.md),
  [list traversal](_docs/list-cells.md), [GoX](gox/_docs/benchmarks.md),
  [typed KVO access](kvo/_docs/bench-typed-access.md),
  [KVO access strategies](kvo/_docs/bench-record-access.md).

#### Trust

- Bytecode and bindings are trusted inputs. Accept script source and compile
  it; do not accept uploaded bytecode.
- Link validates operands and layouts. It cannot prove arbitrary pointers
  safe.
- EVM is not a process isolation boundary.

### GoX

Details: the [GoX guide](gox/README.md).

#### Markup

- Markup appears anywhere in a function that takes a `*gox.Builder`. Tags
  separate statements.
- In an element, `"text"` and `expr` print; assignments and control flow do
  not. `_ = f()` discards a result.
- `<>...</>` is a fragment: output without an element.
- Parenthesize a `<` comparison: `if (a < b) { ... }`.
- Attributes: `class="card"`, `href="/customers/{id}"`, `value={expr}`.
- A false boolean attribute is left out: `disabled={off}`. Other
  attributes, such as `aria-*`, print `false`.
- In `<script>` and `<style>`, `{=expr}` inserts Go data, escaped for
  JavaScript or CSS.
- Field selectors also accept JSON names and snake_case: `p.app_ids` for
  `AppIDs`.
- Indentation and newlines in source print nothing. Put spaces in strings.
- `return`, `break` and `continue` close the elements they leave. Each
  branch and loop body holds complete elements.

#### Output and escaping

- Attributes are for the browser, text is for people. GoX never picks a
  display form for a value.
- Text positions take strings, trusted content and `gox.Content`. Another
  type is a compile error: convert it, or call a function that returns a
  string.
- `value | f` calls `f(value)`; `value | f(a)` calls `f(a, value)`; `_` marks
  another position: `value | f(_, a)`. Implicit arguments still apply.
- An attribute takes strings and integers, whole or interpolated:
  `href="/items/{id}"`. A whole value, `name={value}`, can also be a `bool`.
  There, `byte` and `rune` are refused as ambiguous.
- CSS positions take strings, integers and floats. JavaScript positions take
  JSON-compatible values: scalars, strings, times, slices, arrays and maps.
- Escaping follows the context: HTML text, attribute, URL, srcset,
  JavaScript, CSS. The rules are those of Go's `html/template`.
- `gox.HTML`, `gox.CSS`, `gox.JS`, `gox.JSStr`, `gox.URL`, `gox.Srcset` and
  `gox.HTMLAttr` mark content the application trusts.
- `gox.Text(s)` and `gox.TrustedHTML(h)` make a `gox.Content`, for an input
  that takes either text or markup.
- A nil pointer in an output expression is reported through
  `RenderOptions.Report` and that output is left out. `Strict: true` makes it
  an error.

#### Components

- A function is a component: `<Button label="Save" />`. A package-qualified
  function is `<ui.Button>`. A function value in scope is `<chosen />`.
- A local closure is a local component: `item := func(label string) { ... }`.
- A `func()` parameter is a slot. `<slot-footer>...</slot-footer>` fills
  `footer`; children outside a slot tag fill `body`.
- A slot can take arguments: `row func(item Item)` makes `item` visible
  inside `<slot-row>`.
- Implicit arguments: `//gox:arg theme implicit`, or a type listed in
  `Options.ImplicitTypes`. The callee takes the value of the same name from
  the caller's scope. The builder and `*evmruntime.Ambient` are always
  implicit.
- Optional arguments: `//gox:arg label optional`. `gox.Optional[T]` tells an
  omitted value from a zero one.
- `//gox:arg label legacyName="Label Text"` names an input for hosts that
  pass arguments by string key.
- Map arguments: `extras:key={v}`, `extras:{key}={v}`, and nested
  `extras:attrs:type="email"`.
- Ambient context: `ambient:color="blue"` passes
  `ambient.CloneWith(map[string]any{"color": "blue"})` down.
  `//gox:arg tone ambient="tone"` reads a key into a parameter.
- `content := <Translate key={key} />` assigns a component's result instead
  of printing it.
- Attribute helpers act on the open tag: `b.Class("active")`,
  `b.Attr("id", id)`, `b.AttrOrOmit("id", id)`, `b.Data("action", action)`.
- `b.Open("div")`, `b.Close()`, `b.Text(s)` and `b.HTML(h)` render from Go
  code without markup.
- `gox.ClassList` builds a class string: `classes.Add("button")`,
  `classes.String()`.

#### Compile and render

- `gox.Compile(filename, source, packages...)` compiles one file.
  `gox.CompileSources(files, packages...)` compiles a package.
  `gox.CompileFiles(entry, files, packages...)` also picks the function that
  `Render` runs. `.go` and `.gox` files can share a package.
- `gox.Options{ImplicitTypes, Format, ParseFrontmatter,
  AllowMissingPackage}` has the same three methods with settings.
- `Options.Format`: `gox.HTMLFormat`, `gox.TextFormat` or `gox.CSSFormat`.
- `tmpl.Link(packages...)` returns a `*gox.Program`. It renders concurrently;
  each render has its own package variables and builder.
- `env.NewLinkEnvironment(packages...)` on a `gox.Environment` prepares the
  bound packages once; its `Link(tmpl)` and `LinkModule(module)` replace
  `tmpl.Link` and `gox.LinkModule` for a host that links many templates.
- `program.RenderNative(ctx, []any{...}, options)` renders the entry with Go
  values. `program.RenderFuncNative(ctx, "Card", args, options)` picks a
  function by name.
- `program.Render` and `program.RenderFunc` take `values.Value` arguments,
  including KVO records.
- `gox.RenderOptions{Strict, Report, Budget, MaxOutputBytes, Coverage}`.
  Output is capped at 16 MiB by default. A failed render returns no partial
  output.
- `program.RenderFunction(ctx, handle, args, options)` renders a function
  value that a script returned. `program.RenderFunctionInCall(call, handle,
  args, options)` renders inside a running host call.
- `program.EVM()` returns the linked `*evm.Program`. `gox.ProgramOf(p)` goes
  back. `gox.LinkModule(module, packages...)` links a plain EVM module that
  uses the builder.

#### Host integration

- Templates import host packages bound with `evmbind.New`, as plugins do.
- `evmbind.Options.Imports` with `gox.Schema()` lets a host function take a
  `*gox.Builder` and render. `gox.TrustedContentSchema()` and
  `gox.AmbientSchema()` share the trusted content types and `Ambient`.
- A host field of type `*evm.FunctionOf[func(*gox.Builder)]` holds a renderer
  closure from a script.
- `tmpl.Parameters`, `tmpl.FunctionParameters` and
  `program.ParametersFor("Card")` describe inputs: `gox.Parameter{Name, Type,
  Optional, Implicit, LegacyName, Ambient}`.
- Frontmatter: a file can start with a `---` block.
  `Options.ParseFrontmatter` parses it; `tmpl.Frontmatter("Card")` returns
  the value. `gox.SplitFrontmatter` splits a file without parsing.
- `tmpl.Files()`, `tmpl.File(name)` and `tmpl.FunctionFile("Card")` list
  files and their functions.
- `tmpl.Classes` lists the constant CSS classes with positions;
  `tmpl.DynamicClasses` flags computed ones. Use them for a Tailwind
  safelist.
- `gox.PackageSource` and `gox.PackagesSource` print package declarations
  with their `//gox:arg` lines.

#### Tools

- `gox fmt [-w] [-l] paths...` formats `.gox` and `.go` files and adds missing
  imports. `gox check` checks syntax. Install with `go install ./cmd/gox`.
- `gox.FormatSource(filename, source, packages...)` and
  `gox.CheckSyntax(filename, source)` are the same as functions.
- Tags nest up to `gox.MaxTagNesting`, 100 levels, and Go in a template has
  the `evmcompiler` nesting limits. Checking, formatting and compiling all
  report the first level too deep.
- `gox.Preprocess`, `PreprocessSources` and `PreprocessFiles` lower markup to
  Go without compiling. `GeneratedSources` shows the result;
  `preprocessed.Compile()` finishes.
- [Editor support](_editors): Zed, Vim, Neovim, VS Code, TextMate, Sublime
  Text, Helix.
- `RenderOptions.Coverage` collects coverage in GoX source positions.

GoX renders on the server. It has no client-side runtime. It does not
generate native Go; `GeneratedSources` is for inspection.

### KVO

Details: [KVO](kvo/README.md), [access guide](kvo/_docs/access.md),
[records in scripts](_docs/embedding.md#records-in-scripts).

#### Declare

- `kvo.NewSchema()`; `kvo.NewEntityType(schema, "customer", goTypes...)`;
  `kvo.NewModel(schema, entityType, func(b *kvo.ModelBuilder) { b.Prop(p) })`.
- Scalar properties: `kvo.NewSignedProp[T]`, `NewUnsignedProp[T]`,
  `NewStringProp[T]`, `NewBoolProp[T]`, `NewFloat32Prop[T]`,
  `NewFloat64Prop[T]`, `NewBytesProp[T]`. `T` can be a named type such as
  `Money`.
- `kvo.NewTimeProp(schema, code, name)` stores a `time.Time` with nanoseconds.
  `NewLegacyTimeProp` keeps the older microsecond encoding.
- `kvo.NewScalarProp[T](..., codec)` uses a custom `values.Codec[T]`.
- `kvo.NewListProp(..., kvo.ListOf(elemType), codec)` declares a list.
- `kvo.NewMapProp(..., kvo.MapOf(keyType, valueType), keyCodec, valueCodec)`
  declares a map. `NewJSONMapProp` holds arbitrary JSON values.
  `kvo.NewNestedMapCodec(...).Prop(...)` declares a map of maps.
- `kvo.NewRecordProp` declares a child record, `NewRecordListProp` a list of
  records, `NewRecordMapProp` a map of records.
- `kvo.NewProp` and `NewValueProp` declare properties without a Go type, for
  schemas built at run time. `schema.MustPropByCode(code)` finds one.
- `kvo.BindSignedProp[T](schema, code)` and the other `Bind...Prop` functions
  give typed access to a scalar, list or record property declared elsewhere.
- Types: `kvo.TInt64`, `TUint32`, `TFloat64`, `TBool`, `TMachineString`,
  `TBytes`, `TTimestamp`, `TJSON`; `kvo.NewIntType`, `NewScalarSubtype` and
  their kind declare named ones.
- Property options on `b.Prop(p, ...)`: `kvo.Compact`,
  `kvo.PresenceIsSignificant` (the Go field is then a `kvo.Optional[T]`),
  `kvo.PrimaryKey` (an ordinary stored property that holds the database
  key), `kvo.PropName("x")` (another local name in this model).
- `b.Inline(otherModel)` with a `kvo:",inline"` struct field promotes the
  properties of an embedded struct.
- `b.SetRowName("TenantSettingsRow")` names the script type of a model.
- Field mapping by tags: `kvo:"name"` or `kvo:"customer:name"`; `kvo:"-"`
  excludes a field. Every field of a struct given to `NewEntityType` needs a
  tag.
- Field mapping without tags: leave the struct out of `NewEntityType` and call
  `model.RegisterNativeType(goType, map[kvo.PropCode]string{kvo.CodeOf(prop):
  "FieldName", ...})`. The map names a field for every property of the model;
  other fields stay unexposed.
- A property can belong to several models when its meaning and type are the
  same.
- Codes are identities. Do not renumber them or reuse a retired one.
- A duplicate code, a duplicate name, or two properties with one local name
  in a model panic at declaration.
- Declare schemas, properties, models and bindings at package scope or at
  startup, never per request or per row.

#### Represent

- `kvo.Bind(goType, model)` binds a model to a struct; the struct needs a
  field for every property. `kvo.Project(goType, model, fields)` maps the
  properties to fields by an explicit map. A projection views structs and
  MessagePack rows; it does not encode or decode rows.
- `binding.Record(reflect.ValueOf(ptr))` views a Go struct.
- `binding.Encoded(row2.Open(bytes))` views a Fast2 or Compact2 row.
  `row2.Trusted(bytes)` skips validation. `kvo.Encoded(view).WithModel(model)`
  needs no Go struct.
- `binding.Encode(nil, reflect.ValueOf(ptr).Elem(), row2.Fast2)` writes a row;
  `row2.Compact2` writes the compact layout.
- One row can mix layouts: `b.Prop(childProp, kvo.Compact)` stores that child
  and everything under it as Compact2 inside a Fast2 row.
- `binding.MessagePack(bytes)` views a MessagePack row.
  `msgpack.Encode(nil, reflect.ValueOf(ptr))` writes one.
- `binding.Decode(bytes, dst)` and `binding.TryDecodeRecord(record, dst)`
  decode a whole Fast2 or Compact2 row into a struct. `binding.Validate(bytes)`
  checks one.
- `record.Fastest()` decodes once for repeated reads: a Go struct when bound,
  indexed memory otherwise. `record.Fast()` keeps Fast2 and decodes the rest.
- `record.NativePointer()` returns the struct pointer behind a Go struct
  record. `record.Native()` decodes a row into a new struct first.
- `binding.Generate(pkg, name)` returns Go source with typed field access for
  a binding; `binding.WithGenerated(&Bindings, mode)` uses it.
  `binding.WithNativeSchema(schema)` shares an `evmbind` schema's accessors.
- Build bindings once. Their metadata is never freed.

#### Read

- `record.Get(prop)` and `prop.Get(record)` return the property's Go type.
- `record.Missing()` tells an absent record. `record.Has(code)` tells an
  absent property.
- `record.Get(listProp)` returns a `kvo.List[T]`: `Len()`, `Iter()`,
  `Cursor()`, `Slice()`, `OwnedSlice()`. It borrows the row.
- `record.Get(mapProp)` returns a `kvo.Map[K, V]`: `Get(key)`, `Lookup(key)`,
  `Keys()`, `Iter()`, `Len()`.
- `record.Get(recordProp)` returns the child `kvo.Record`. A record list is a
  `kvo.RecordList`.
- Without typed references: `record.GetValue(code)` returns a
  `values.Value`, `record.GetOwnedString(code)` returns a string that owns its bytes,
  `record.Child(step)` opens a child, `record.ListValue(code)` iterates a
  list.
- `record.ToJSON(model, kvo.JSONOptions{})` returns a detached
  `map[string]any`. An empty list or map stays empty; a nil one, and a nil
  entry in a list of records, is null.
- Operations panic on an error. `TrySet`, `TryToJSON`, `TryDecode`,
  `TryEncode`, `TryValidate` and `row2.TryOpen` return it.

#### Edit

- `edit := record.Edit()`; `edit.Set(prop, value)`; `edit.Get(prop)`;
  `edit.Delete(prop)`.
- `edit.List(listProp)`: `Append`, `AddUnique`, `Remove`, `RemoveAt`, `Set`,
  `Clear`. `listProp.SetSlice(edit, items)` replaces the list.
- `edit.Map(mapProp)`: `Set`, `Delete`, `Clear`, `Lookup`, and `SetNull` on a
  JSON map.
- Child records: `edit.Edit(recordProp)` returns nil when absent,
  `edit.New(recordProp)` creates, `edit.EditOrCreate(recordProp)` does either.
  A map of records has the same three: `edit.Map(p).EditOrCreate(key)`. A
  list of records has `Edit(index)` and `New()`, which appends.
- `kvo.EditMap`, `kvo.NewMap` and `kvo.EditOrCreateMap` edit nested maps.
- `edit.Record()` returns the edited record. `edit.Snapshot()` returns it as
  a row2 view. `edit.MessagePack()` returns the bytes of a MessagePack edit.
  `edit.Changes()` counts changes.
- An editor of a struct or of decoded memory writes through at once. A row
  editor batches changes and encodes on `Record`, `Snapshot` or
  `MessagePack`. The source bytes stay unchanged.
- A MessagePack edit keeps every untouched field's bytes, including fields
  the model does not know.
- Low level: `edit.SetValue(code, value)`, `edit.GetPathValue(path)`,
  `edit.FastSetPathValue(path, value)`.

#### Lifetimes

- A view borrows its bytes. A row read inside a database transaction is valid
  for that transaction.
- `record.Own()` keeps the data past its source. `record.Clone()` makes an
  independent copy to edit.
- `list.Slice()` copies the slice; its strings still borrow.
  `list.OwnedSlice()` copies them too. `recordList.CloneSlice()` clones each
  record.

#### Records in scripts and templates

- `evm.ModelPackage("m", schema)` makes a package with a type per model:
  `m.Customer` to read, `m.CustomerEdit` to edit.
- Fields are local property names: `c.points_earned`. The qualified form
  `c.customer_points_earned` also works.
- Pass a `kvo.Record`, or a pointer to the bound struct, to `RunEntryNative`
  and `RenderNative`. Pass `record.Value()` to `vm.Run`.
- `for _, tag := range c.tags`, `len(c.tags)` and `c.tags[i]` read a list
  from the row without building a slice.
- `c.ToJSON()` returns the record as JSON-compatible data.
- A host function returns a record with `call.SetRecordResult(model, record)`
  or `call.RecordValue(model, record)`, and reads an argument with
  `call.Record(value, model)`.
- Editors: `ctx, scope := evm.WithRecords(ctx)`;
  `scope.EditRecord(model, editor)` makes the script value;
  `scope.MutableRecord(value, model)` gets the editor back in a save
  function. A host function returns one with
  `call.SetEditRecordResult(model, editor)`.
- A script assignment to a collection property of an editor is pending until
  a host function calls `call.FlushRecord(editor)`, usually the one that
  saves. Scalar assignments apply at once.
- `evmbind.RecordMethod(model, "PointBalance", fn, evmbind.BorrowOnly)` with
  `evmbind.RegisterMethods(&pkg, methods, options)` makes a Go function a
  method of a model's read type. `evmbind.GenerateMethods` generates direct adapters;
  without them the call uses reflection.
- `evmbind.Options.Models` lets a host API take and return model structs as
  rows. `evmbind.Options.Mutable` exposes chosen pointers as editors that
  write to the Go struct.
- `RecordType.NativeAllocation` with `evm.RegisterNativeCopies(&pkg)` adds
  `row.NativeCopy()`, which decodes a row into a Go struct inside a script.
- The compiler copies a record that a script keeps past its scope; see
  [ownership](#ownership-of-borrowed-data).
- GoX templates take the same types:
  `func Card(b *gox.Builder, c *m.Customer)`.

## Packages

![Package layers, from examples at the top to values at the bottom](_docs/packages.svg)

Each package imports only packages below it. `evm` imports `kvo` for record
support; `kvo`, `nativebind` and `values` never import the VM, so a host can use
the data model without it. `kvo/codec/fast1` sits above `kvo` because it
imports `kvo`; the other two codecs sit below because `kvo` imports them.

| Package | Imports |
|---|---|
| `examples/*` | `evm`, `evmbind`, `evmcompiler`, `evmstd`, `gox`, `kvo`, `kvo/codec/msgpack`, `kvo/codec/row2`, `values` |
| `gox` | `evm`, `evmbind`, `evmcompiler`, `evmruntime`, `evmstd`, `values` |
| `evmstd` | `evm`, `evmbind` |
| `evmbind` | `evm`, `evmcompiler`, `kvo`, `nativebind`, `values` |
| `evmcompiler` | `evm`, `kvo`, `nativebind`, `values` |
| `evmasm` | `evm`, `values` |
| `evm` | `kvo`, `kvo/codec/row2`, `nativebind`, `values` |
| `kvo/codec/fast1` | `kvo` |
| `kvo` | `kvo/codec/msgpack`, `kvo/codec/row2`, `nativebind`, `values` |
| `kvo/codec/row2`, `kvo/codec/msgpack` | `values` |
| `nativebind` | `values` |
| `values`, `evmruntime` | nothing |

## Internals and tests

[Inside EVM](_docs/EVMBook.md) is a book about the machine: values,
instruction encoding, compilation, calling conventions, the host boundary, the
evaluator, optimization, and what a GoX template becomes. Bytecode has no persisted
compatibility format. [Benchmarks](bench/README.md) separate execution,
compilation and database costs.

```sh
go test ./...
go run ./examples/evm-basic
go run ./examples/gox-basic
go run ./examples/kvo-basic
go run ./EVMBook-rebuild.go
go test ./bench -run '^$' -bench . -benchmem
go test ./kvo -run '^$' -bench 'Benchmark(AccessStrategy|DecodedAccess|TypedList)$' -benchmem
go test ./gox -run '^$' -bench BenchmarkApplication -benchmem
```

A [test](README_test.go) checks that the example code in this file matches
the sources under `examples/`.

## License

Copyright (c) 2026 Andrey Tarantsov. Published under the [Zero-Clause BSD license](LICENSE).
