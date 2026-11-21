# GoX

**Typed HTML templates written in real Go.** Write ordinary Go functions with
variables, loops, branches, and closures, and drop HTML markup right where you
want output. Components are just functions, their inputs are type-checked at
compile time, and every value is escaped automatically for the exact HTML,
attribute, URL, JavaScript, or CSS context where it lands.

GoX compiles and runs on [EVM](../README.md). You can compile templates once,
link them to your Go application's APIs, and recompile them at run time without
rebuilding or restarting your server. It requires no database and no KVO
models.

## A 60-second tour

Here is a complete GoX package. `Updates` is the entry point: the host passes
`viewer` and `accent`, and GoX supplies the `*gox.Builder` automatically. Like
ordinary Go, every `.gox` file imports the packages it uses.

```gox
package updates

import (
	"fmt"
	"strings"

	"github.com/andreyvit/evm/gox"
)

type Release struct {
	Name string
	Done bool
}

func Updates(b *gox.Builder, viewer string, accent string) {
	items := []Release{{"Live templates", true}, {"Native builds", false}}
	status := func(done bool) string {
		if done {
			return "shipped"
		}
		return "on the roadmap"
	}
	<Panel title="What shipped">
		<slot-body>
			<p>
				"Hello, "
				viewer
				"!"
			</p>
			<List items={items}>
				<slot-row>
					<strong>item.Name | strings.ToUpper</strong>
					" "
					<Badge label={status(item.Done)} />
				</slot-row>
			</List>
		</slot-body>
		<slot-after>
			fmt.Sprintf("%d updates. No host rebuild.", len(items))
		</slot-after>
	</Panel>
	<style>.badge { color: {=accent}; }</style>
	<script>window.viewer = {=viewer};</script>
}

func Panel(b *gox.Builder, title string, body func(), after func()) {
	<section>
		<h1>title</h1>
		body()
		<footer>after()</footer>
	</section>
}

func List(b *gox.Builder, items []Release, row func(item Release)) {
	<ul>
		for _, item := range items {
			<li>row(item)</li>
		}
	</ul>
}

//gox:arg accent implicit
func Badge(b *gox.Builder, label string, accent string) {
	<span class="badge" title="Accent: {accent}">label</span>
}
```

Five things to notice right away:

- **Components are just Go functions.** `<Panel>`, `<List items={items}>`, and
  `<Badge />` are type-checked function calls—attributes map to named
  parameters, not untyped string maps.
- **Slots are typed callbacks.** `row func(item Release)` passes each `item`
  into `<slot-row>`, while the slot still closes over `status` and `accent`
  from the caller. Named slots let `Panel` place its body and footer wherever
  it likes.
- **Explicit data, implicit context.** `List` takes `items` explicitly, while
  `Badge` picks up `accent` from the caller's scope via
  `//gox:arg accent implicit`. Every component receives `*gox.Builder`
  automatically.
- **Real Go everywhere.** Structs, slices, closures, `if`, `for`, `fmt.Sprintf`,
  and formatting pipelines (`item.Name | strings.ToUpper`) live right alongside
  markup.
- **Automatic contextual escaping.** When `viewer` is `<Ada & Bob>`, the
  greeting emits `&lt;Ada &amp; Bob&gt;` while `<script>` receives a safely
  quoted JavaScript string. Attributes and CSS use their own rules—no manual
  escaping needed.

The runnable version of this example lives at
[testdata/examples/showcase.gox](testdata/examples/showcase.gox), and
[showcase_test.go](showcase_test.go) compiles, links, renders, formats, and
live-recompiles it in a single process.

### Live recompilation—and what GoX is not

Because GoX embeds EVM's compiler, your server can accept edited template
source, compile and link a new `*gox.Program`, and atomically swap it in while
in-flight requests finish on the old program. You never rebuild or restart the
host binary (your host Go APIs stay compiled into the binary). File watching
and deployment policy stay in your application—GoX does not run a background
reload daemon.

Two important boundaries to know up front:

- **Server-side only.** GoX emits HTML, text, or CSS on the server. It has no
  client-side runtime, browser state, or hydration.
- **Native Go code generation is on the roadmap, not implemented yet.** The
  [native compilation proposal](_docs/native-compilation.md) describes how
  EVM-in-dev + optional native-in-prod will share one frontend and manifest
  format. Today's `GeneratedSources` is inspectable compiler output for
  debugging, not standalone Go code.

## Contents

- [Run your first template](#run-your-first-template)
- [How GoX syntax works](#syntax)
- [What produces output](#what-produces-output) & [Values by position](#values-by-position)
- [Attributes and escaping](#attributes-and-escaping)
- [Components](#components): [slots](#slots-are-typed-callbacks), [local components](#local-components), [implicit arguments](#implicit-arguments), [optional & map inputs](#xml-parameter-conventions), [helpers & pipelines](#attribute-helpers-and-pipelines)
- [Ambient context](#ambient-context)
- [Compile, link, render](#compile-link-render) & [Files and frontmatter](#files-and-frontmatter)
- [Whitespace and output formats](#whitespace-and-output-formats)
- [Safety and failures](#safety-and-failures)
- [Host integration](#host-integration), [Class discovery](#class-discovery), & [Execution coverage](#execution-coverage)
- [Source tooling & formatting](#source-tooling)
- [Troubleshooting](#troubleshooting)
- [Tests and benchmarks](#tests-and-benchmarks)

## Run your first template

In any Go module that depends on `github.com/andreyvit/evm` (using the Go
version in [go.mod](../go.mod)), save this as `main.go` and run `go run .`:

```go
package main

import (
	"context"
	"fmt"

	"github.com/andreyvit/evm/gox"
	"github.com/andreyvit/evm/values"
)

func main() {
	tmpl := must(gox.Compile("greeting.gox", `package greeting

import "github.com/andreyvit/evm/gox"

func Greeting(b *gox.Builder, name string) {
		<p>
			"Hello, "
			name
			"!"
		</p>
	}`))
	program := must(tmpl.Link())
	html := must(program.Render(context.Background(), []values.Value{
		values.String("Alice & Bob"),
	}, gox.RenderOptions{Strict: true}))
	fmt.Println(html)
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
```

Output:

```html
<p>Hello, Alice &amp; Bob!</p>
```

Notice that you pass only `name`; GoX supplies the builder. Compile and link
once at startup, then reuse `program` across concurrent requests.

For larger runnable examples, see [example_test.go](example_test.go),
[examples/gox-basic](../examples/gox-basic) (multi-file templates with layouts,
slots, implicit arguments, pipelines, and live recompilation), and the
[loyalty example](https://github.com/andreyvit/edb/tree/main/examples/loyalty)
with typed KVO models.

## Syntax

GoX is Go with HTML tags. Before Go parses the file, GoX lowers the markup into
builder calls and then runs EVM's type checker and compiler. HTML, text, and
CSS templates share the same frontend and differ only in their escaping
context.

Keep these core rules in mind:

- **Tags start with `<` anywhere in a rendering function.** Strings and Go
  comments protect their contents, and multiline opening tags continue until
  `>`.
- **Parenthesize `<` comparisons:** Write `if (age < minimumAge) { ... }` and
  `for i := 0; (i < count); i++ { ... }`. Operators `<=`, `<<`, and `<<=` keep
  their normal Go meaning. Ordinary `{` re-enables markup (even inside a
  callback inside parentheses), and `}` restores the enclosing mode. Inside an
  attribute's `{expression}`, everything is pure Go.
- **Tags separate statements.** `<p>name<strong>"VIP"</strong></p>` works on
  one line. Adjacent Go expressions still need a newline or semicolon:
  `<p>"Hi, "; name</p>`.
- **Quote literal text.** Source indentation and line breaks emit nothing;
  spaces inside quoted strings are preserved. Use `<p>name</p>` for variables
  and `<p>"{"</p>` for a literal brace—a bare `{a}` in element body is a Go
  block, not interpolation.
- **JavaScript and CSS keep their own syntax.** Insert Go data inside
  `<script>` and `<style>` with `{=expression}`.
- **Friendly field selectors.** KVO selectors use model aliases, and Go struct
  fields also accept unambiguous JSON names and `snake_case` (`AppIDs` →
  `app_ids`, `URLs` → `urls`).

### Fragments

When you want to emit output without wrapping it in an HTML element, use a
fragment (`<>...</>`):

```gox
func Greeting(b *gox.Builder, name string) {
    <>
        "Hello, "
        name
        "!"
    </>
}
```

Fragments must balance inside each branch, loop body, and component. They do
not introduce a Go variable scope (use a Go `{ ... }` block if you want one).
Inside an element, attribute helpers called through a fragment still apply to
the element's pending opening tag. Fragments also work in text and CSS
templates.

## What produces output

Inside an element or fragment, standalone expressions render to the output;
assignments and control flow do not. Outside markup, ordinary Go rules apply.

| Inside an element or fragment | What happens |
| --- | --- |
| `"Hello"` | Renders literal text |
| `customer.Name` | Renders a string or trusted content, escaped for its context |
| `strconv.Itoa(customer.Points)` | Converts a number, then renders the string |
| `order.Placed \| app.FormatDate` | Calls `app.FormatDate(order.Placed)` and renders the result |
| `label(customer)` | Renders the helper's single return value |
| `Card(customer)` | Calls a renderer that writes through the builder |
| `label := customer.Name` | Assigns a variable; emits nothing |
| `_ = lookup()` | Calls a function and discards its result |
| `if enabled { ... }` | Ordinary Go control flow |

Functions returning no values run for their rendering or side effects;
functions returning one value render that value. Multiple return values cannot
be rendered as a single expression, and a function value must be called to
render it.

```gox
func Names(b *gox.Builder, names []string) {
	<ul>
		for i, name := range names {
			<li>
				fmt.Sprintf("%d. %s", i+1, name)
			</li>
		}
	</ul>
}
```

### Why text requires strings

Text positions—element bodies, fragments, `<title>`, `<textarea>`, and text
templates—accept only strings (including named string types), trusted content
types such as `gox.HTML`, and `gox.Content`. Passing an `int` or other type is
a compile error:

```text
page.gox:25:5: cannot output a value of type int: text takes strings and trusted content; convert it
```

This is deliberate: GoX never guesses how to format a value for a human reader.
Even one integer type reads differently depending on context—a balance as
`1,250`, a delta as `+1,250`, a year as `2026`, a count as `2,026`. Convert in
place (`strconv.Itoa(n)`, `strconv.FormatBool(ok)`, `fmt.Sprintf(...)`) or pipe
through a domain formatter (`price | app.FormatMoney`).

### Values by position

A simple rule of thumb explains every position's accepted types: **text is for
people, attributes/CSS/JS are for the browser.** People need explicit
formatting, so text requires strings. Browsers expect raw numbers and booleans
in attributes, CSS, and JavaScript, so GoX accepts them directly there.

| Position | Example | Accepted types |
| --- | --- | --- |
| **Text** | `<p>name</p>`, `<title>`, `<textarea>` | Strings, trusted content, `gox.Content` |
| **Whole attribute** | `colspan={n}`, `hidden={flag}` | Strings, integers, `bool` |
| **Interpolated attribute** | `href="/items/{id}"`, `title="{count} items"` | Strings, integers |
| **CSS** | `{=value}` in `<style>`, `style="width: {percent}%"` | Strings, integers, floats |
| **JavaScript** | `{=value}` in `<script>`, `onclick="go({id})"` | JSON-compatible values |

For anything else, format it with a function or pipeline. Extra parameters on
that function are filled using [implicit arguments](#implicit-arguments), so a
helper like `func FormatMoney(m Money, locale *Locale) string` needs no
explicit locale at the call site:

```gox
<td title="{order.Placed | app.FormatTime}" data-total={int64(order.Total)}>
	order.Total | app.FormatMoney
</td>
```

Type nuances to know:

- **Strings** include named string types, trusted content types, and
  `gox.Content`.
- **Integers in attributes** cover `int`, `int8`, `int16`, `int32`, `int64`,
  `uint`, `uint16`, `uint32`, and `uint64`. `byte`, `uint8`, and `rune` are
  rejected as ambiguous (character vs. number—convert to `string` or `int`),
  and `uintptr` is rejected too. CSS accepts all of them as numbers.
- **Named numeric types** (like `type Money int64`) are not treated as plain
  integers, because raw cents are rarely what a browser or reader expects.
  Convert explicitly (`int64(price)`) or call a formatter.
- **Floats** are allowed in CSS (always decimal, never scientific notation) and
  JavaScript.
- **Booleans** are accepted as whole attribute values only.
- Component arguments, `b.Attr`, `b.Text`, and pipelines keep their normal Go
  types.

## Attributes and escaping

HTML attributes come in three flavors: quoted static text (`class="card"`), a
whole dynamic value (`aria-disabled={disabled}`), or interpolation inside a
quoted string (`href="/customers/{id}"`):

```gox
func Link(b *gox.Builder, id string, label string, disabled bool) {
	<a href="/customers/{id}" aria-disabled={disabled}>
		label
	</a>
}
```

**Boolean attributes** follow HTML semantics:

- Standard boolean HTML attributes (`disabled`, `hidden`, `checked`, etc.) are
  omitted when `false` and emitted when `true`.
- All other attributes (`aria-disabled`, `aria-expanded`, `contenteditable`,
  `draggable`, `spellcheck`, `data-*`) print `"true"` or `"false"`, because
  those attributes distinguish `"false"` from absent.

Component attributes are typed Go function arguments, not HTML attributes.

**Special attribute contexts:**

- `style` attributes are CSS (strings, integers, floats), just like `{=value}`
  in `<style>`.
- Event-handler attributes (`onclick`, etc.) are JavaScript and accept
  JSON-compatible values (booleans, numbers, strings, `time.Time`, and slices,
  arrays, or maps of those). Passing a struct compiles and fails at render
  time—pass a map or string instead.

**Escaping is always contextual.** Text, attributes, URLs, srcsets, JavaScript,
and CSS each get their own escaping and filtering rules (the same rules as Go's
`html/template`). Put raw data in expressions—never pre-escape it. Reserve
trusted-content types like `gox.HTML` strictly for markup your application
already trusts (see [Safety and failures](#safety-and-failures)).

## Components

Every component is an ordinary Go function taking `*gox.Builder` as its first
parameter. Call it with XML syntax (`<Button label="Save" />`) or Go syntax
(`Button("Save")`), or pass it around as a function value (`chosen := Button`,
`<chosen label="Save" />`).

- Tags resolve exact, case-sensitive Go names, including package selectors
  (`<ui.Button>`) and method values (`<card.Render>`).
- Component attributes match parameter names verbatim, including `camelCase`.
- A callable in scope takes precedence over a same-named HTML tag—capitalize
  component names to avoid collisions. Unknown capitalized tags are compile
  errors.
- Every renderer declares its full Go signature (anonymous parameter-list
  templates are not supported). See the tested examples in
  [page.gox](testdata/examples/page.gox) and
  [button.gox](testdata/examples/button.gox).

### Slots are typed callbacks

A component accepts child markup as one or more `func(...)` parameters and
decides where—and how many times—to invoke them:

```gox
func Panel(b *gox.Builder, title string, body func(), after func()) {
	<section>
		<h2>title</h2>
		body()
		<footer>
			after()
		</footer>
	</section>
}

func Page(b *gox.Builder, name string) {
	<Panel title="Profile">
		<slot-body>
			<p>name</p>
		</slot-body>
		<slot-after>
			<a href="/profiles">"All profiles"</a>
		</slot-after>
	</Panel>
}
```

- Children placed directly inside `<Panel>...</Panel>` without a `<slot-...>`
  wrapper fill the `body` slot.
- Parameterized slots like `row func(item *Item)` bring `item` into scope
  inside `<slot-row>`, while keeping access to all of the caller's local
  variables.
- Because a slot is a real callback, the component can render it conditionally,
  skip it, or call it inside a loop. Duplicate or unknown slot/attribute names
  are compile errors.

### Local components

You can define helper components inline as Go closures and call them with
either Go or XML syntax:

```gox
item := func(label string) {
    <strong>
        page.Title
        ": "
        label
    </strong>
}
<div>
    item("one")
    <item label="two" />
</div>
```

### Implicit arguments

To avoid threading request-wide context (like a theme, store, or viewer)
through every component call, mark a parameter `implicit`—either per function
with `//gox:arg name implicit` or globally by exact full type path via
`gox.Options{ImplicitTypes: []string{"*bh/model.ViewData"}}`.

`*gox.Builder` and `*evmruntime.Ambient` are always implicit. No other
parameter ever inherits implicitly just because a local variable shares its
name.

```gox
//gox:arg active implicit
func Badge(b *gox.Builder, label string, active bool) {
	<span class="badge" aria-disabled={!active}>
		label
	</span>
}

func Page(b *gox.Builder, active bool) {
	<div>
		Badge("Member")
		<Badge label="Guest" active={false} />
	</div>
}
```

Here, `Badge("Member")` lowers to `Badge(b, "Member", active)`, while
`<Badge label="Guest" active={false} />` passes `false` explicitly—an explicit
argument always wins.

In positional Go calls inside `.gox` files, GoX binds supplied arguments in
order and inserts eligible implicit parameters whenever the next supplied
argument does not match (taking the builder from the enclosing renderer and
other implicit values from a same-named, assignable variable in lexical scope).
Optional omitted parameters receive zero values. Ordinary `.go` files keep
standard Go call rules.

Rendering functions containing markup must return no values. Helper functions
without top-level markup can return values (including `gox.HTML`); calling one
in output position renders its return value (use `_ = helper()` to discard it).

### XML parameter conventions

Beyond ordinary named attributes, XML component calls support **prefixed map
inputs**, **ambient overrides**, **optional parameters**, and **assignment**.

#### Prefixed map inputs and ambient overrides

```gox
<Card
    ambient:color="blue"
    someExtras:class="compact"
    otherExtras:{key}={value}
    enabled={false}
/>
```

- **`ambient:key={value}`** requires a caller variable `ambient` and a callee
  parameter both of type `*evmruntime.Ambient`. GoX passes
  `ambient.CloneWith(map[string]any{"key": value})`, leaving the caller's
  ambient untouched while descendants inherit the updated one (keep the
  parameter named `ambient` to preserve name-based inheritance).
  `ambient:{key}={value}` works with a dynamic `string` key too.
- **`someExtras:key={value}`** builds a fresh map for the map parameter named
  `someExtras` (there is no magic `extras` name—each prefix selects its own
  parameter, and maps never merge with a caller's map). Bare
  `someExtras:enabled` sets the value to `true`.
- **`someExtras:{key}={value}`** uses a Go expression as the key. Typed and
  named maps work as long as key and value types match; static keys are strings
  (hyphens and extra colons stay part of the key).
- You cannot mix `someExtras={wholeMap}` with `someExtras:key=...` on the same
  parameter in one call.

See [testdata/examples/map-inputs.gox](testdata/examples/map-inputs.gox) for a
runnable example.

#### Nested map inputs

Add more colon-separated segments to build nested submaps:

```gox
<helpers.Subblock
    key="email-input"
    data={customer}
    extras:attrs:type="email"
    extras:attrs:name="email"
    extras:attrs:value={customer.Email}
    extras:attrs:placeholder="you@example.com"
/>
```

This populates `extras["attrs"]` as `map[string]any` (typed nested map
parameters keep their declared map types). Computed leaf keys work
(`extras:attrs:{key}={value}`), and you can quote keys containing colons
(`args:{"tier::class"}={className}`). Mixing a whole submap with nested entries
at the same path, duplicate constant keys, or treating a scalar as a submap are
compile errors. Leaf key and value expressions are evaluated once.

#### Optional parameters and `//gox:arg`

Parameters are required by default—parameter names have no special optionality
convention. Mark optional inputs with a `//gox:arg` directive right above the
function:

```gox
//gox:arg label optional legacyName="String Arg Name"
func Card(b *gox.Builder, label string) { <p>label</p> }
func Page(b *gox.Builder) { <Card label="Hello" />; <Card /> }
```

- `//gox:arg` supports `optional` / `optional=false`, `implicit` /
  `implicit=false`, and `legacyName="..."`. Unknown parameters or options and
  duplicate legacy names are errors.
- XML calls always use the Go parameter name (`label`); `legacyName` is for
  host adapters that pass arguments by string key (exposed via
  `Program.ParametersFor(name)`, `Template.Parameters`, and
  `Parameter.InputName()`).
- An optional `*T` parameter accepts a raw `T` in either XML or GoX call
  syntax (evaluated once and wrapped in a fresh pointer), or `nil` when
  omitted.
- Use plain `string` with `optional` when empty and missing mean the same
  thing. Use `gox.Optional[T]` (`Value T`, `Present bool`) when you need to
  distinguish an explicit `false` or `0` from omission.
- **Structural optional types:** Any named struct starting with `Optional`,
  `Opt`, `Maybe`, or `Null` that has a `Value` field and one `bool` field named
  `Valid`, `Present`, `IsValid`, or `IsPresent` (including generic instances
  like `Optional[bool]`) is recognized as optional automatically unless marked
  `optional=false`. Omitting it passes the zero value (or `nil` for a pointer);
  passing a raw value constructs the wrapper with the presence flag set to
  `true`.

Optional and implicit rules apply to both XML calls and ordinary calls in
`.gox` files; prefixed map entries apply to XML calls.

#### Assigning XML call results

A self-closing XML call to a function that returns a value can be the right-hand
side of `:=` or `=`. Assignment returns the Go result without rendering it:

```gox
tierContent := <TierLabel name={tier.Name} />
content := <Translate
    key={key}
    args:{dimension}={value}
    args:{dimension + "::class"}={valueClass}
    args:tier={tierContent}
/>
<p>content</p>
```

Optional inputs, implicit context, and prefixed map inputs work normally here.
Assignment supports self-closing calls only (not calls with slots or HTML
fragments), and functions with no return values cannot be assigned.

### Attribute helpers and pipelines

#### Imperative attribute helpers

When you want to share attribute logic across elements, write a helper taking
`*gox.Builder` and call it right inside an opening tag before any child output:

```gox
func decorate(b *gox.Builder, disabled bool) {
    b.Class("button")
    b.Attr("aria-disabled", disabled)
    b.Attr("disabled", disabled)
}
```

- `b.Class("...")` appends classes to the element's `class` attribute (empty
  strings are ignored).
- `b.Attr("class", "")` clears classes; a nonempty `b.Attr("class", ...)`
  appends. For all other attributes, `b.Attr` replaces any earlier value.
- `b.AttrOrOmit("id", anchor)` skips `nil` and empty strings (including named
  string types) while keeping normal `Attr` behavior for `0`, `false`, and
  whitespace.
- `b.Data("action", "change->upload#select")` sets `data-action` as escaped
  text without inferring URL or CSS filtering from the suffix (ideal for
  Stimulus actions and similar data attributes). The suffix must be a nonempty
  ASCII attribute name.
- Void elements (`<input>`, `<img>`, with or without `/>`) stay pending until
  the next output, so a helper right after `<input type="text">` modifies that
  `<input>`, not its parent. Attribute helpers also work before content inside
  `<textarea>` and `<title>`:

```gox
<input type="text">
decorate(false)
<textarea>
    decorate(false)
    value
</textarea>
```

- Calling an attribute helper after child output begins fails the render.
- `b.Open`, `b.Close`, `b.Text`, and `b.HTML` let you emit markup imperatively
  from Go helpers. The builder is valid only during its render and checks
  context at run time so a callback can never inject HTML into a JS, CSS, or
  attribute context.

#### Formatting pipelines

Use `|` to chain formatting calls cleanly from left to right:

```gox
name | strings.ToUpper                  // strings.ToUpper(name)
name | strings.TrimPrefix(_, "Dr. ")    // strings.TrimPrefix(name, "Dr. ")
count | fmt.Sprintf("%d items", _)      // fmt.Sprintf("%d items", count)
name | strings.TrimSpace | strings.ToUpper
```

- `value | f` calls `f(value)`.
- `value | f(arg)` passes `value` as the last explicit argument: `f(arg, value)`.
- Put `_` where `value` should go when it is not last (one `_` per call, as a
  whole argument—`x | f(g(_))` is a compile error).
- Implicit arguments still apply, and pipelines work anywhere in a `.gox` file
  (output, attributes, component arguments, assignments).
- The right side must be a callable returning a single value. Integer bitwise
  OR keeps its normal Go meaning unless the right side uses `_`.

### Building class strings with `ClassList`

Use `b.Class` when styling the current element. When you need to build a class
string to pass into another component, use `gox.ClassList`:

```gox
var classes gox.ClassList
classes.Add("button")
classes.Add(extraClass)
if disabled {
    classes.Add("opacity-50")
}
<Button class={classes.String()} />
```

`Add` skips empty strings, trims surrounding whitespace, and joins groups with
single spaces (without deduplicating or reordering). Its zero value is ready to
use. Because it wraps `strings.Builder`, do not copy a `ClassList` after its
first `Add`. `String()` does not copy the buffer, and later additions leave
earlier strings untouched.

### Plain text or trusted HTML (`gox.Content`)

When a component input (such as a heading or label) should accept *either*
plain text or trusted HTML, type it as `gox.Content`:

```go
func Title(b *gox.Builder, title gox.Content) { <h1>title</h1> }
// Callers pass:
//   title={gox.Text("<plain text>")}
//   title={gox.TrustedHTML(gox.HTML("<em>trusted markup</em>"))}
```

`Content` has two fields (`Content string`, `TrustedHTML bool`) and its zero
value is empty plain text. `Text` escapes for the output context; `TrustedHTML`
asserts that the caller already trusts the markup (it does not sanitize it, and
attribute contexts still apply contextual escaping).

Inside EVM, `Content` and common `Optional[T]` specializations pack into a
single VM cell without heap boxing; see
[compact value representations](_docs/compact-values.md).

## Ambient context

`evmruntime.Ambient` is an immutable, persistent `map[string]any` chain for
inherited presentation choices (themes, density, overlay classes). Pass real
domain data (list items, view models) explicitly or via `ImplicitTypes`, and
keep `extras` and `ambient` parameters last, in that order.

- A `nil` `*evmruntime.Ambient` is an empty context.
- `Get(key)` walks from newest layer to parent; `CloneWith(map[string]any{...})`
  creates a child layer without mutating the parent (an explicit `nil` hides a
  parent value; stored values are not deep-copied). `Values()` returns a
  flattened map copy.
- Files naming the type import `"evmruntime"`. Because `*evmruntime.Ambient` is
  built-in implicit, naming the parameter `ambient` lets calls inherit it
  automatically.

### Binding an ambient key directly to a parameter

Use `//gox:arg paramName ambient="key"` to read an ambient key straight into a
typed parameter:

```gox
//gox:arg mediaOverlayClass ambient="media_overlay_class"
func Card(b *gox.Builder, title string, mediaOverlayClass string, ambient *evmruntime.Ambient) {
    <article class={mediaOverlayClass}>title</article>
}

func Page(b *gox.Builder, ambient *evmruntime.Ambient) {
    <Card title="Preview" ambient:media_overlay_class="overlay" />
    Card("Another card")
}
```

Rules for `ambient="..."`:

- The function must declare exactly one `*evmruntime.Ambient` parameter.
- The bound parameter cannot be passed explicitly in XML or GoX calls.
- Missing or `nil` values yield the parameter's zero value; non-nil values must
  match its type. Reading does not mutate or remove the key.
- `ambient` cannot be combined with `optional`, `implicit`, or `legacyName`.
  Host adapters can inspect `Parameter.Ambient`, and `gox.AmbientSchema()`
  exposes `Ambient` to host-bound helpers.

## Compile, link, render

GoX follows the same three-stage lifecycle as EVM:

| Stage | Input | Output |
| --- | --- | --- |
| **Compile** | `.gox`/`.go` source + package declarations | Checked EVM module + rendering plan (`*gox.Template`) |
| **Link** | `*gox.Template` + host implementations | Immutable, concurrent `*gox.Program` |
| **Render** | `*gox.Program` + `ctx` + ordered arguments | Escaped output string or an error |

Every `.gox` file starts with a `package` clause (after any frontmatter and
leading comments) and imports every package it references: EVM built-ins
(`errors`, `fmt`, `log`, `math`, `strconv`, `strings`),
[`evmstd`](../evmstd/API.md) packages (`time`, `slices`, `maps`, `net/url`,
`regexp`, etc., included automatically by GoX), `github.com/andreyvit/evm/gox`,
and your host packages. Referencing a known package without importing it fails
with a helpful message:

```text
greeting.gox:3:18: undefined: gox (add import "github.com/andreyvit/evm/gox")
```

Run `gox fmt` or `gox.FormatSource` to add missing imports automatically.

### Compile options and shared environments

`gox.Options` has `Compile`, `CompileSources`, and `CompileFiles` methods
matching the package-level functions:

| Option | Purpose |
| --- | --- |
| `Format` | Output language: `HTMLFormat` (default), `TextFormat`, or `CSSFormat` |
| `ImplicitTypes` | Full type paths whose parameters are [implicit](#implicit-arguments) |
| `ParseFrontmatter` | Parser callback for [file frontmatter](#files-and-frontmatter) |
| `AllowMissingPackage` | Accept legacy files without a `package` clause |
| `Environment` | Pre-checked package environment (replaces passing packages on every call) |

If your host compiles or links many templates against the same host packages,
prepare them once:

- `env := gox.NewEnvironment(packages...)` type-checks your host packages, the
  GoX runtime, and `evmstd` once. Pass `Options{Environment: env}` to compile,
  preprocess, or format without repeating the package list. `env` is immutable
  and safe for concurrent use, and `env.Compiler()` compiles plain Go against
  the same packages.
- `links := env.NewLinkEnvironment(packages...)` validates and binds host
  packages once, so `links.Link(tmpl)` and `links.LinkModule(module)` keep only
  what each template needs while sharing host shapes and native method tables.
  (Use `gox.LinkOptions{Compilers: ...}.NewEnvironment(packages...)` if you
  also have a separate `evmcompiler.Environment`, or
  `gox.NewLinkEnvironment(packages...)` without a compile environment.)

To expose a Go package from your app, bind it with
[`evmbind.New`](../evmbind/README.md) and pass `schema.Package()` to `Compile`
and `schema.Bind(nil)` to `Link` (or the bound package to both).

### Rendering

- **`program.RenderNative(ctx, []any{user, cart, query}, opts)`** passes
  ordinary Go values (scalars, `time.Time`, pointers to bound native structs,
  and bound slices/maps). Types must match parameters exactly—no implicit
  numeric conversions. Use `RenderFuncNative(ctx, "Card", args, opts)` to pick
  a renderer by name.
- **`program.Render` / `program.RenderFunc`** accept `values.Value` arguments,
  including KVO records and script structs. Inspect `Template.Parameters` for
  entry argument order.
- A linked `*gox.Program` renders concurrently: each render gets an isolated VM
  session and builder from a pool. `RenderOptions.Budget` and `ctx` limit
  execution, and output is capped at 16 MiB by default (`MaxOutputBytes`
  overrides it). Failed renders return an error and never emit partial output.

## Files and frontmatter

- `CompileSources(files, packages...)` compiles a `map[string]string` of `.gox`
  and `.go` files with no default entry (call `RenderFunc` / `RenderFuncNative`
  by name).
- `CompileFiles(entry, files, packages...)` also selects the default renderer
  by function name or `.gox` filename (picking its first renderer).
- Imports are file-local, and all files in one compilation must declare the
  same package name (unless `AllowMissingPackage` is set for legacy sources,
  which default to `plugin`).

A `.gox` file may begin with a `---` frontmatter block:

```gox
---
title: "Rewards"
---

package rewards

import "github.com/andreyvit/evm/gox"

func Render(b *gox.Builder) {
    <h1>"Rewards"</h1>
}
```

Set `Options.ParseFrontmatter` to `func([]byte) (any, error)` using whatever
metadata format you prefer (YAML, JSON, NAML, etc.). GoX calls it during
preprocessing with the bytes between the `---` lines. Frontmatter without a
parser—or a parser error—fails compilation with the filename; files without
frontmatter need no parser.

Metadata attaches to every named top-level function in that file (use separate
files for different metadata). Inspect files and metadata via:

- `Template.Files()`, `Template.File(name)`, `Template.FunctionFile(fnName)`
  (and the same methods on `Program`). Each `gox.File` holds `Filename`, `Name`
  (basename without `.gox`), `Functions` (declaration order, excluding methods,
  `init`, `_`, anonymous functions, and `.go` files), `Frontmatter`, and
  `HasFrontmatter`.
- `Template.Frontmatter(fnName)` / `Program.Frontmatter(fnName)` return
  `(value, present)` (staying `present == true` even if the parser returned
  `nil`). Treat parsed metadata as immutable; it lives on the template/program,
  not inside a serialized EVM module.
- `Options.CheckSyntax` validates frontmatter delimiters; `FormatSource`
  preserves frontmatter bytes verbatim without needing a parser.

## Whitespace and output formats

Source indentation and newlines between statements emit nothing—put spaces and
newlines inside quoted strings (raw strings work nicely inside `<pre>`):

```gox
<pre>
    "first\n  second\n"
</pre>
```

Inline `<script>` and `<style>` bodies preserve their source whitespace and
insert Go values with `{=value}`:

```gox
<script>const customer = {=customer};</script>
<style>.button { color: {=color}; }</style>
```

Rendered HTML has no automatic pretty-printer (pretty-printing can change
inline spacing or `<pre>` semantics).

For non-HTML output, set `Options.Format`:

- **`gox.TextFormat`** renders top-level expressions as plain text (following
  the string/trusted-content text rule).
- **`gox.CSSFormat`** treats literal strings as CSS source and dynamic
  expressions as contextually escaped CSS values.

Both share the same Go frontend, type checking, nil guards, and EVM compiler;
HTML markup tags require `HTMLFormat`.

## Safety and failures

GoX's contextual analyzer and escaping functions come directly from Go's
`html/template` (see [upstream notes](internal/htmltemplate/README.md)). At
compile time, GoX determines the exact escaping filter for every expression; at
run time, it calls those filters directly without ever running a Go text
template.

- **Trust boundary:** Template source and host packages are trusted code; input
  data is untrusted. Converting untrusted data to `gox.HTML`, `CSS`, `JS`,
  `JSStr`, `URL`, `Srcset`, or `HTMLAttr` bypasses escaping.
- **Structural protection:** Dynamic `script`/`style` types are rejected,
  runtime attribute helpers cannot alter those types or meta-refresh parsing,
  and every rendering call verifies its active context at run time.
- **Nil-pointer resilience:** A nil pointer dereference in an output expression
  reports a `Diagnostic` (via `RenderOptions.Report`, defaulting to
  `slog.Warn`) and skips that expression or component call—omitting the whole
  attribute in attribute position, or emitting a context-safe empty/null token
  in inline JS/CSS so neighboring tokens cannot merge unsafely. Set
  `Strict: true` to fail the render on nil diagnostics instead. Other EVM
  errors and host panics are never silently swallowed.
- **Balanced markup:** Every branch, loop body, and component must contain
  complete elements. `return`, `break`, and `continue` automatically close any
  open elements they exit; labeled jumps across open elements are rejected.

## Host integration

### Calling guest renderers from Go

- **Standalone render:** Retain a renderer returned by a script with
  `program.EVM().FunctionValue(value)` and render it with
  `program.RenderFunction(ctx, handle, args, options)`. The function must take
  `*gox.Builder` first and return no values. Closures keep their captures while
  each render resets session globals and builders.
- **Rendering inside a host call:** Use
  `program.RenderFunctionInCall(call, handle, args, options)` inside a native
  callback or `VM.RunNative`. Globals, captures, context, and budget stay
  shared with the enclosing call (leave `options.Budget` zero), while nested
  renders get their own builder. Use `Call.CallbackError` or
  `Call.PanicCallback` in wrappers so guest panic recovery stays intact.
- **Storing renderers in Go structs:** A native field of type
  `*evm.FunctionOf[func(*gox.Builder)]` holds a guest renderer closure or bound
  method that you can invoke with `Program.RenderFunction`.

### Sharing GoX types with host packages

- Pass `gox.Schema()` in `evmbind.Options.Imports` (keyed by GoX's import path)
  so host helpers can accept `*gox.Builder` and trusted-content types with the
  exact same type identities (Builder's private fields stay hidden). Do not
  retain or concurrently use a `*gox.Builder` outside its render.
- `gox.TrustedContentSchema()` shares trusted-content types alone, and
  `gox.AmbientSchema()` shares `*evmruntime.Ambient`.
- Function literals with their own named `*gox.Builder` parameter (not `_`) use
  that builder; literals without one capture the enclosing renderer's builder.
- `//gox:arg` annotations also work on imported native functions bound via
  `evmbind`, and `gox.PackageSource` / `gox.PackagesSource` preserve them in
  printed declarations (via `evmcompiler.DeclarationOptions.Annotations`).
- Additional helpers: `gox.LinkModule(module, packages...)` links a plain EVM
  module using `*gox.Builder`, `gox.ProgramOf(evmProg)` recovers the
  `*gox.Program`, `gox.SplitFrontmatter` splits a file without parsing metadata,
  and `Template.FunctionParameters(name)` inspects inputs before linking.

## Class discovery

`Template.Classes` collects every static CSS class name (with source positions)
from static attributes, constant interpolations, `b.Class`, `b.Attr("class", ...)`,
`b.AttrOrOmit("class", ...)`, and `ClassList.Add`. Dynamic patterns like
`bg-{tone}` are recorded in `DynamicClasses` rather than falsely emitting
`bg-`, giving you exact input for a Tailwind safelist or host policy. (Classes
synthesized inside opaque host helpers cannot be discovered statically.)

## Execution coverage

Pass `evm.NewCoverage(program.EVM())` in `RenderOptions.Coverage` to collect
instruction counts and branch outcomes mapped back to original `.gox` source
positions. Use one collector per concurrent run and merge afterward. Renders
without this option collect nothing, even when reusing a pooled session; nested
renders inside a native call inherit the enclosing VM's collector.

Source line coverage excludes compiler-generated nil guards and their fallback
paths (raw instruction coverage still records them), while explicit template
branches require both outcomes to be complete.

## Source tooling

Editor highlighting is available for Zed, Vim, Neovim, VS Code, TextMate,
Sublime Text, and Helix—see [GoX editor support](../_editors).

Install the CLI from this repository:

```sh
go install ./cmd/gox
gox check page.gox          # syntax check only
gox fmt page.gox            # print formatted source to stdout
gox fmt -w views/           # format .go and .gox files recursively
gox fmt -l views/           # list files that need formatting
cat page.gox | gox fmt      # read stdin (-filename aids import discovery)
```

`gox check` validates Go syntax and balanced tags without needing host packages
(it rejects frontmatter because it has no frontmatter parser). `gox fmt`
formats files, skips hidden directories and `vendor`, never overwrites invalid
files, and accepts frontmatter verbatim. Both exit nonzero with file/line
positions on error.

The same pipeline is available as Go APIs:

```go
ensure(gox.CheckSyntax("page.gox", source))
formatted := must(gox.FormatSource("page.gox", source, hostPackages...))
lowered := must(gox.Preprocess("page.gox", formatted, hostPackages...))
fmt.Print(lowered.GeneratedSources["page.gox.go"])
tmpl := must(lowered.Compile())
// Link tmpl with matching host implementations, then render it.
```

| Operation | Needs host packages? | What success guarantees |
| --- | --- | --- |
| `CheckSyntax` / `gox check` | No | Go and GoX syntax and tag balance are valid |
| `FormatSource` / `gox fmt` | Optional (for host import paths) | Source is canonically formatted; unresolved names may remain |
| `Preprocess` | Yes | Typed lowering and contextual escaping analysis succeeded |
| `Compile` | Yes | Lowered Go compiles cleanly to EVM bytecode |
| `Link` | Yes (bound implementations) | All runtime bindings match and are ready to render |

`PreprocessSources` and `PreprocessFiles` handle multi-file packages, and all
three have `Options` variants for text and CSS formats. `GeneratedSources` is
immutable diagnostic output referencing the separate rendering plan kept inside
`Preprocessed` and `Template`.

All operations enforce nesting limits with source positions: up to
`MaxTagNesting` (100) nested tags, plus `evmcompiler`'s limits (32 type levels,
500 expression levels, 100 statement levels, 32 function-literal levels—note
that a component with children compiles to a function literal—and
`MaxBranchWeight` across the package).

### Formatting rules

`gox fmt` is opinionated with zero style flags:

- Go code follows `gofmt` with tabs; each paired tag or fragment indents one
  level, and required parentheses around `<` comparisons stay intact.
- A single body expression or child element stays on one line with its tags if
  the whole line fits in **80 columns** (counting each tab as 4 columns);
  anything longer splits across lines. Multiple body items always use separate
  lines.
- Multiline opening tags put one attribute per line (indented one level) with
  `>` or `/>` aligned with `<`, keeping attribute order unchanged. Single-line
  self-closing tags include a space before `/>`.
- String literals, raw strings, and `<script>` / `<style>` bodies preserve
  their contents and whitespace untouched.
- Imports are organized with `goimports` (adding missing imports and pruning
  unused ones). GoX runtime and `evmstd` paths are built in; host paths come
  from packages passed to `FormatSource` (unresolved packages stay intact).

```gox
func Card(b *gox.Builder, name string) {
	<section>
		<h2>name</h2>
		<Button
			label="An example with enough text to keep this opening tag multiline"
			name={name}
		/>
	</section>
}
```

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| Text is treated as an identifier | Quote literal text: `<p>"Hello"</p>` |
| A `<` comparison parses as a tag | Parenthesize it: `if (a < b) { ... }` |
| An argument is not inherited | Only implicit parameters inherit. Check `//gox:arg name implicit` or `ImplicitTypes`, plus the caller variable's exact name, scope, and type |
| Unknown component error | Match the exact case-sensitive Go name and ensure its package is imported |
| `tags nest more than 100 levels` / `statements nest more than 100 levels` | Extract the inner markup into a helper component or function |
| `function is too large` / `functions are too large together` | Split huge branching functions into smaller functions (or split the package) |
| `undefined: x (add import "...")` | Add the missing import, or run `gox fmt` / `FormatSource` |
| `cannot output a value of type int` | Text accepts strings and trusted content only—convert (`strconv.Itoa(n)`, `fmt.Sprint(v)`) or pipe (`price \| app.FormatMoney`) |
| `cannot use a value of type ... as an attribute value` | Whole attributes take strings, `bool`, or integers—convert or call a formatter |
| `cannot interpolate a value of type ... into an attribute` | Interpolated attributes take strings and integers—convert or pipe (`{price \| app.FormatMoney}`) |
| `cannot use a value of type ... in CSS` | CSS takes strings, integers, and floats |
| Spaces between words vanish | Source indentation emits nothing; put spaces inside quoted strings |
| Attribute helper fails at run time | Call attribute helpers before the element emits any child output |
| Branch leaves an element open | Keep complete, balanced elements inside every branch and loop body |
| `gox check` passes, `Compile` fails | `check` is syntax-only and does not resolve imports, types, escaping contexts, or EVM limits |
| `GeneratedSources` does not compile as standalone Go | It depends on the separate rendering plan stored in `Preprocessed` and `Template` |
| A value silently disappears | Check nil-pointer diagnostics or render with `Strict: true` |

When debugging a tricky template, work step by step: `CheckSyntax` →
`Preprocess` (inspect `GeneratedSources` to see lowered calls and implicit
arguments) → `Compile` → `Link` → render with `Strict: true`. Keep original
filenames so every error points to the exact source line.

## Tests and benchmarks

```sh
go test ./...
go test -race ./...
go test ./gox -run '^$' -fuzz FuzzCompileGoX -fuzztime=15s
go test ./gox -run '^$' -bench BenchmarkApplication -benchmem -benchtime=200ms -count=8
```

The [application benchmarks](_docs/benchmarks.md) test three anonymized,
production-derived template suites (cards, history tables, and plan comparison
matrices), each at two sizes, verifying byte-for-byte output parity with cached
`html/template`. Compilation, linking, and view-model setup are outside the
timer.

On Apple M5 Max with Go 1.27.3, GoX rendered all six workloads in 27–40% less
time than cached `html/template` (eight samples, 2026-11-06). See the report
for allocation counts, before-and-after samples, EVM controls, and reproduction
commands. `BenchmarkRender` remains as a small diagnostic workload.
