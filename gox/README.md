# GoX

GoX is a typed templating language that puts HTML inside Go functions. Use Go
variables, loops, conditions and closures to build a view; write HTML where you
want output. Components are functions, their inputs are checked, and values are
escaped for the HTML, attribute, URL, JavaScript or CSS context where they appear.

GoX runs through [EVM](../README.md). You can compile templates once, link them
to host APIs, and render them repeatedly without rebuilding the host application.
It can be used independently of EDB's database and KVO models.

## One example to show a friend

**Typed Go, HTML components, and templates you can recompile while the server
keeps running.**

This is a complete template package. `Updates` is the entry point. The host
passes `viewer` and `accent`; GoX supplies the builder. Like every GoX file,
it imports each package it names.

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
					<strong>
						item.Name | strings.ToUpper
					</strong>
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

What to notice:

- **XML-style components:** `<Panel>`, `<List items={items}>` and `<Badge />`
  call ordinary typed Go functions. Attributes are named arguments, not string
  dictionaries.
- **Typed slots:** `row func(item Release)` introduces `item` inside `<slot-row>`.
  The slot also captures `status` and `accent` from the caller. Named slots let
  `Panel` place its body and footer independently.
- **Explicit data, implicit context:** `List` receives `items` explicitly.
  `Badge` inherits the theme accent through `//gox:arg accent implicit`.
  Both receive the builder automatically.
- **Real Go:** structs, slices, a local closure, `if`, `range`, and `fmt.Sprintf`
  work alongside markup. `value | strings.ToUpper` is a typed formatting pipeline.
- **Contextual escaping:** `viewer` becomes HTML text in the greeting and a
  JavaScript string in the script. CSS interpolation and the badge's quoted
  attribute use their own contexts. No manual escaping calls are needed.

With `viewer` set to `<Ada & Bob>`, the greeting contains
`&lt;Ada &amp; Bob&gt;`, and the script receives a quoted, escaped JavaScript
string. The native-build entry deliberately says “on the roadmap.”

[Copy the source](testdata/examples/showcase.gox). The [showcase test](showcase_test.go)
compiles, links and renders it, verifies escaping, checks formatting preserves
output, then edits its heading and compiles and renders the new version in the
same process.

### Recompile templates without rebuilding the host

GoX uses EVM's compiler inside the application. The host can accept edited source,
compile it against its registered package descriptions, link it, and replace its
active rendering program. This does not require rebuilding or restarting the
host binary. The host APIs themselves still belong to that binary.

Keep the old program serving requests until the replacement compiles and links
successfully; in-flight requests can finish with the old program. File watching,
publication and the program swap are application responsibilities, not an
implicit GoX reload service.

**Native Go generation is planned, not implemented.** The intended pairing is
EVM iteration during development and optional native precompilation for
production, using the same templates and semantics. The
[native compilation proposal](_docs/NATIVE-COMPILATION.md) covers shared lowering,
host bindings, parity tests, generated-code freshness and release fingerprints.
Today's `GeneratedSources` is inspectable compiler output, not a deployable
native-Go artifact.

GoX is server-side rendering. It emits HTML; it does not provide browser state,
hydration or a client-side component runtime. Guest code uses EVM's supported Go
subset and registered packages, not unrestricted access to every Go package.

## Guide

- [Showcase](#one-example-to-show-a-friend) and [live recompilation](#recompile-templates-without-rebuilding-the-host)
- [Run your first template](#run-your-first-template)
- [Syntax](#syntax) and [what produces output](#what-produces-output)
- [Attributes and escaping](#attributes-and-escaping)
- [Compile, link, render](#compile-link-render)
- [Components, slots and helpers](#components-slots-and-helpers)
- [Implicit arguments](#implicit-arguments) and [ambient context](#ambient-context)
- [Whitespace and output formats](#whitespace-and-output-formats)
- [Safety and failures](#safety-and-failures)
- [Host integration](#native-host-callbacks) and [class discovery](#class-discovery)
- [Formatting and tooling](#source-tooling)
- [Troubleshooting](#troubleshooting)

## Run your first template

In a Go module that depends on `github.com/andreyvit/evm`, save this as `main.go`
and run `go run .`. Use the Go version required by [go.mod](../go.mod).
The GoX source is a string here; applications can load the same text from `.gox`
files or their own template storage.

```go
package main

import (
	"context"
	"fmt"

	"github.com/andreyvit/evm/gox"
	"github.com/andreyvit/evm/values"
)

func main() {
	tmpl, err := gox.Compile("greeting.gox", `package greeting

import "github.com/andreyvit/evm/gox"

func Greeting(b *gox.Builder, name string) {
		<p>
			"Hello, "
			name
			"!"
		</p>
	}`)
	if err != nil {
		panic(err)
	}
	program, err := tmpl.Link()
	if err != nil {
		panic(err)
	}
	html, err := program.Render(context.Background(), []values.Value{
		values.String("Alice & Bob"),
	}, gox.RenderOptions{Strict: true})
	if err != nil {
		panic(err)
	}
	fmt.Println(html)
}
```

Output:

```html
<p>Hello, Alice &amp; Bob!</p>
```

The host passes `name`; GoX supplies the builder. Compile and link once, then
reuse `program`. The [executable example](example_test.go) checks this rendering
path, and the [loyalty example](https://github.com/andreyvit/edb/tree/main/examples/loyalty) adds typed host models.

## Syntax

- Markup can appear anywhere, independent of line breaks. A single `<` starts
  a tag unless protected by parentheses or brackets. Strings and Go comments
  protect their contents; multiline opening tags continue through `>`.
- Parenthesize comparisons: `<p>(age < minimumAge)</p>` and
  `for i := 0; (i < count); i++ { ... }`. Operators `<=`, `<<`, `<<=` and `<-`
  keep their Go meanings. Ordinary `{` enables markup, even inside a callback
  passed within parentheses; `}` restores the enclosing delimiter mode.
  Attribute `{expression}` is Go throughout.
- Tags provide statement separators: `<p>name<strong>"VIP"</strong></p>` works.
  Adjacent Go expressions still need a newline or semicolon: `<p>"Hi, "; name</p>`.
  Go blocks keep their scopes; body braces are not interpolation delimiters.
  To migrate older templates, replace `{a}{b}` with `a; b` and parenthesize
  bare `<` comparisons. A single `{a}` still works as a Go block.
- Inside elements/fragments, standalone expressions render. Assignments,
  conditions and arguments do not. Outside them, write ordinary Go.
- Quote text. Source indentation emits nothing; string whitespace is preserved.
  Use `<p>name</p>` for inline values and `<p>"{"</p>` for a literal brace.
- Attributes accept quoted strings with interpolation or `{expression}`.
  Boolean HTML attributes omit false; ARIA attributes retain it.
- JS/CSS bodies retain their syntax. Insert Go data with `{=expression}`.
- Inputs are typed. Inside markup, omitted arguments bind by name; explicit
  inputs win and incompatible matching types fail compilation.
- KVO selectors use model aliases. Go fields also accept unambiguous JSON names
  and snake_case (`AppIDs` → `app_ids`, `URLs` → `urls`).

GoX lowers markup before Go parsing, then uses EVM's compiler and budgets.
HTML, text and CSS share that pipeline but use different escaping contexts.

Use a fragment for output that needs no HTML element:

```gox
func Greeting(b *gox.Builder, name string) {
    <>
        "Hello, "
        name
        "!"
    </>
}
```

Fragments must balance within each branch, loop iteration and component. They
do not create a Go variable scope; use a Go block for that. Inside an element,
attribute helpers called through a fragment still modify that element's pending
opening tag. `<>...</>` also works in text and CSS templates.

## What produces output

Within an element or fragment:

| Source | Meaning |
| --- | --- |
| `"Hello"` | Render literal text |
| `customer.Name` | Render a value, escaped for its context |
| `label(customer)` | Render the helper's result |
| `Card(customer)` | Call a renderer; it writes through the builder |
| `label := customer.Name` | Assign a variable; emit nothing |
| `_ = lookup()` | Call and deliberately discard the result |
| `if enabled { ... }` | Run ordinary Go control flow |

A function call returning no values can perform rendering or another side effect.
A call returning one value in output position renders that value. Multiple return
values cannot be rendered as one expression. A function value itself must be
called to render it.

Outside markup, ordinary Go rules apply. To emit text without a wrapper element,
enter a fragment with `<>...</>`. Braces alone create a Go block, not an
interpolation expression. In attributes, `{expression}` has a different role:
it supplies that attribute's value.

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

## Attributes and escaping

Use quoted text for fixed attributes, `{expression}` for a whole dynamic value,
and braces inside a quoted attribute to interpolate part of its text:

```gox
func Link(b *gox.Builder, id string, label string, disabled bool) {
	<a href="/customers/{id}" aria-disabled={disabled}>
		label
	</a>
}
```

HTML boolean attributes such as `disabled` are omitted when false. Attributes
such as `aria-disabled` retain the textual `false` value. Component attributes
are typed function arguments instead of HTML attributes.

Escaping is contextual: text and attribute values are escaped differently, and
URLs, JavaScript and CSS receive their own filtering. Put ordinary data into
expressions; do not pre-escape it. Explicit trusted-content types such as
`gox.HTML` are for content the application has already decided to trust, not a
way to display arbitrary user-provided markup. See [safety](#safety-and-failures).

## Compile, link, render

There are three distinct stages:

| Stage | Input | Result |
| --- | --- | --- |
| Compile | Source and package descriptions | Checked EVM bytecode and rendering plan |
| Link | Compiled template and compatible host implementations | Reusable rendering program |
| Render | Program, context and ordered input values | Escaped output or an error |

`Compile` accepts the same `evm.Package` descriptions as EVM's compiler. `Link`
accepts their implementations; the two sets must have compatible signatures.
Every GoX file starts with a Go package clause after any frontmatter and
leading comments, then imports each package its source names: standard library
packages such as `fmt`, `github.com/andreyvit/evm/gox` for the builder, and
supplied host packages. Naming a known package without importing it is a
compile error at the reference, and the message names the import:

```text
greeting.gox:3:18: undefined: gox (add import "github.com/andreyvit/evm/gox")
```

Besides host packages, a template can import the VM's own `errors`, `fmt`,
`log`, `math`, `strconv` and `strings`, and the [`evmstd`](../evmstd/API.md)
standard packages such as `time`, `slices`, `maps`, `net/url` and `regexp`.

This covers package-qualified tags such as `<ui.Button>`. A local value with a
package's name is an ordinary value. `gox fmt` and `gox.FormatSource` add
missing imports; see [formatting rules](#formatting-rules).

To let templates use a Go package, give [`evmbind.New`](../evmbind/README.md)
that package's own source; it exposes every exported type, method, function
and constant. Pass `schema.Package()` to `Compile` and `schema.Bind(nil)` to
`Link`.

`RenderNative(ctx, []any{user, cart, query}, options)` passes ordinary Go
values: strings and other scalars, time values, and pointers to bound native
structs, which the template shares with the host. A Go slice or map passes
when a linked schema binds its type. Each value must have the Go type of its
parameter; there are no implicit numeric conversions. `RenderFuncNative`
selects a renderer by name.

`Template.Parameters` gives the entry argument order. `Render` and `RenderFunc`
take arguments in the existing EVM value ABI, including host types, script
structs and KVO models. Compiled
`Module`, original source positions, `GeneratedSources` and class metadata are
available for tooling. Keep the template's rendering plan with its module.

A linked `Program` supports concurrent renders. Each render gets isolated VM
globals and builder state; registered host implementations retain their own
state. Context cancellation and `RenderOptions.Budget` apply to execution.
Output is capped at 16 MiB by default (`MaxOutputBytes` overrides it). Failed
renders return an error and no partial document.

## Ambient context

`evmruntime.Ambient` is a persistent map of `string` to `any`. A nil pointer is
an empty context. `Get(key)` searches from the newest node to its parents;
`CloneWith(map[string]any{...})` copies a delta map and shares its parent.
An explicit nil value hides the parent value. Stored values are not deep-copied.
`Values()` returns a separate flattened map for interop with older extras APIs.
Files that name the type import `"evmruntime"`.

Pass it as an ordinary optional parameter, usually `ambient *evmruntime.Ambient`.
Calls inside tags and fragments inherit it by name like other parameters. A local
override can use `ambient = ambient.CloneWith(...)`; callers keep their original context.

## Building class arguments

Use separate `b.Class` or class-helper calls when styling the current element.
When another renderer needs a combined string, use `ClassList`:

```gox
var classes gox.ClassList
classes.Add("button")
classes.Add(extraClass)
if disabled {
    classes.Add("opacity-50")
}
<Button class={classes.String()} />
```

`Add` skips empty groups, trims surrounding whitespace, and inserts a space
between groups. It does not deduplicate or reorder classes. The zero value is
ready to use. It wraps `strings.Builder`; do not copy a list after its first
addition. `String()` does not copy the buffer, and later additions leave earlier
strings unchanged.

## Components, slots and helpers

### Host frontmatter

A `.gox` file may start with a `---` line, opaque metadata, and a closing
`---` line. Both delimiters occupy whole lines. Metadata belongs to every
named function declared in that file; use separate files for different metadata.

```gox
---
title: "Rewards"
---

package rewards

import "github.com/andreyvit/evm/gox"

func Render(b *gox.Builder) {
    <h1>{"Rewards"}</h1>
}
```

Set `gox.Options.ParseFrontmatter` to `func([]byte) (any, error)`. GoX calls
it during preprocessing, before Go compilation, with bytes between the delimiter
lines. The host chooses the format (YAML, JSON, NAML, or something else).
An error fails parsing with the filename; frontmatter without a callback fails
as well. Files without frontmatter need no callback.

`Template.Files()` lists all GoX files in filename order, including files
without metadata or functions. `Template.File(filename)` looks up an exact
source-map key; `Template.FunctionFile(functionName)` finds a function's file.
Each `gox.File` contains `Filename`, `Name` (basename without `.gox`),
`Functions` in declaration order, `Frontmatter`, and `HasFrontmatter`.
The first function is `Functions[0]` when the list is nonempty. Methods,
`init`, blank names, and anonymous functions are excluded. Ordinary `.go`
files are excluded too. `Program` exposes the same methods after linking.
Returned function slices are copies; host metadata remains shared and immutable.

`Template.Frontmatter(functionName)` and `Program.Frontmatter(functionName)`
return `(value, present)`. Presence remains true if the callback returns nil.
Treat the parsed value as immutable. Metadata stays with the template/program;
serializing an EVM module alone does not serialize host objects.

`Options.CheckSyntax` also validates frontmatter. `FormatSource` preserves its
opaque bytes without interpreting them, so formatters need no host callback.
The editor grammar recognizes the block without imposing a metadata language.

`CompileSources(sources, packages...)` compiles a map of filenames to source text.
Call `program.RenderFunc(ctx, "Page", args, options)` to select a renderer; `args`
omits the builder. `CompileFiles(entry, sources, ...)` also selects a default
renderer for `Render`: `entry` can be a function name or a GoX filename (its first
renderer). Go and GoX files each need their own imports; they are file-local.
Go and GoX files in one package must declare the same name.
Hosts can opt into `Options.AllowMissingPackage` for legacy sources. Files that
omit the clause then inherit the package name, defaulting to `plugin`.

Function names identify components: `func Button(...)` is `<Button>`;
`func EarnCard(...)` is `<EarnCard>`. They also work as ordinary function
values: `chosen := Button`, `Button(...)` or `<chosen ... />`.
Tags resolve exact, case-sensitive Go names, including lexical function values
and selectors such as `<ui.Button>` or `<card.Render>`. Component attributes keep
parameter spelling, including camelCase. A callable in scope takes precedence
over a same-named HTML tag; prefer capitalized component names to avoid collisions.
Unknown capitalized tags are compile errors. The full Go signature includes the
builder and every input. See the tested
[page](testdata/examples/page.gox) and [button](testdata/examples/button.gox).

`Compile`, `CompileFiles` and `CompileSources` use the same compiler. Every
renderer declares its function name, builder and inputs; anonymous parameter-list
templates are not supported.

### Slots are typed callbacks

A component receives child content as a function and chooses where to call it:

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

Children without `<slot-...>` wrappers supply `body`. A slot typed
`func(item *Item)` exposes `item` inside that slot, while retaining access to the
caller's lexical variables. The component passes the item when it invokes the
callback. Repeated or unknown inputs are compile errors. A slot renders when
called, so the component can call it conditionally or repeatedly.

### Local components

Local components use Go closures:

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

GoX calls can omit parameters explicitly marked `implicit`, or whose exact type
is on the implicit allowlist. The builder and `*evmruntime.Ambient` are built in.
Hosts add types with `gox.Options{ImplicitTypes: []string{"*bh/model.ViewData"}}`.
Type names use the full import path. Other parameters never inherit just because
there is a same-named local. This applies to both Go and XML calls:

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

The first call becomes `Badge(b, "Member", active)`. The component call becomes
`Badge(b, "Guest", false)`. An explicit argument wins over an inherited one.

Ordinary calls bind supplied positional arguments in order, inserting approved
implicit parameters where the next argument does not match. The builder comes
from the current rendering function; other implicit values use the parameter
name in lexical scope and must have an assignable type. Explicit arguments win.
Optional omitted parameters receive zero values. The same rules apply outside
markup in `.gox` files; ordinary `.go` source keeps Go's argument rules.

Rendering functions containing markup return no values. Ordinary helpers can
return values, including `gox.HTML`; output-position calls render their result.
Use `_ = helper()` to discard a value deliberately.

### XML parameter conventions

Component calls support three explicit conventions:

```gox
<Card
    ambient:color="blue"
    someExtras:class="compact"
    otherExtras:{key}={value}
    enabled={false}
/>
```

- `ambient:key={value}` requires a caller variable named `ambient` of type
  `*evmruntime.Ambient` and a callee parameter of that type. GoX passes
  `ambient.CloneWith(map[string]any{"key": value})`. Descendant calls inherit
  the callee's ambient parameter normally; the parent context is unchanged.
  Use the parameter name `ambient` to retain name-based inheritance.
- `someExtras:key={value}` adds an entry to the map parameter named `someExtras`.
  Each prefix selects its own parameter; there is no special `extras` name or
  last-map selection. `extras:key` works when the parameter is named `extras`.
  Maps are freshly constructed; they do not merge with a caller's map.
- `someExtras:{key}={value}` uses a Go expression as the key. Keys and values
  must match the parameter's map types. Named map types and typed maps work too.
  Static keys are strings; hyphens and further colons remain part of the key.
  `ambient:{key}={value}` also works, with a string key.
- Do not combine `someExtras={wholeMap}` with entries for that same map in one
  call. Other maps remain independent. Bare `someExtras:enabled` means `true`.
  The old `ambient-` and `extra-` prefixes have no special meaning.

See [the runnable map-input example](testdata/examples/map-inputs.gox) for two
map parameters, a dynamic key, and ambient inheritance.

- Parameter names have no optionality convention. Declare optional inputs with
  a function doc directive; omission supplies the zero value without inheritance:

```gox
//gox:arg label optional legacyName="String Arg Name"
func Card(b *gox.Builder, label string) { <p>label</p> }
func Page(b *gox.Builder) { <Card label="Hello" />; <Card /> }
```

`//gox:arg` attaches to the following named function and accepts `optional` and
`implicit` as shorthand for `optional=true` and `implicit=true`, respectively.
Explicit boolean values (`optional=false`, `implicit=false`) and
`legacyName="..."` are also supported. Unknown parameters/options and duplicate
legacy names are errors. XML calls always use the Go parameter name (`label`);
legacy names are for string-keyed native adapters. An optional `*T` accepts a
raw T in either GoX call syntax, evaluated once
and wrapped in a fresh pointer; omission supplies nil.

`Program.ParametersFor(name)` exposes `Parameter{Name, Type, Optional, Implicit,
LegacyName}`. `Optional` includes structural optional recognition and explicit
overrides; `InputName()` returns the legacy name when configured, otherwise the
Go name. `Template.Parameters` exposes the selected entry's same metadata.

Use lower camel case for Go parameters and `legacyName` for older snake-case or
human-readable keys. Prefer plain `string` with `optional` when empty and
missing mean the same thing. Use `gox.Optional[T]` (`Value T`, `Present bool`)
when presence matters, including distinguishing false or zero from omission.

Optional wrapper types are recognized structurally. A named struct whose name
starts with `Optional`, `Opt`, `Maybe` or `Null` must have a `Value` field and
exactly one bool presence field named `Valid`, `Present`, `IsValid` or
`IsPresent`. Generic instantiations such as `Optional[bool]` work too. An
explicit `optional=false` makes even a structural optional input required.

An omitted wrapper receives its zero value; a supplied raw value constructs the
wrapper with `Value` set and the presence field true. For a pointer to such a
wrapper, omission produces nil and a supplied value constructs a new wrapper.
False and zero therefore remain distinguishable from omission. Other fields
retain their zero values. The value-field whitelist currently contains only
`Value`; lookalike structs without the required fields are ordinary parameters.

Supplying both a prefixed group and its explicit target parameter is an error;
there is no implicit map merge. Unknown attributes remain errors. These conventions apply to
XML component calls for map entries; optional and implicit parameter rules are
shared by XML calls and ordinary calls in GoX source.

### Nested map inputs

Additional colon-separated segments build submaps:

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

This passes `extras["attrs"]` as a `map[string]any`. Typed nested map parameters
retain their declared map types. Computed leaf keys work too:
`extras:attrs:{key}={value}`. Quote a key containing colons to keep it as one
key: `args:{"tier::class"}={className}`.

A supplied map and nested entries for that same path cannot be combined.
Duplicate constant leaf keys and scalar values used as submaps are errors.
Expressions in leaf keys and values are evaluated once.

### Assigning XML call results

Self-closing XML component calls can be the sole right-hand side of `:=`
or `=` assignments. They return the function's ordinary Go result; assignment
neither renders that result nor captures output from a rendering function.
Optional inputs, implicit context and named-map inputs work as in statement
calls. A function with no return values cannot be assigned.

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

Use braces around Go value expressions (`args:points={value}`), and quote
literal strings (`key="tier"`). Map entries use their parameter name as the
prefix; `args:tier` targets the `args` map, while `tier` names a Go parameter.
Assignment syntax currently supports self-closing calls, not calls with slots
or arbitrary HTML fragments. Parentheses and brackets retain ordinary Go
expression parsing.

### Attribute helpers and pipelines

```gox
func decorate(b *gox.Builder, disabled bool) {
    b.Class("button")
    b.Attr("aria-disabled", disabled)
    b.Attr("disabled", disabled)
}
```

Call `decorate(disabled)` inside a pending opening tag. `b.Class("...")` appends
classes to the fixed class attribute; empty strings do nothing. `b.Attr("class", "")`
explicitly clears classes; other attributes replace the earlier value.
`b.AttrOrOmit("id", anchor)` skips nil and empty strings, including named string
types. It preserves trusted content types and normal attribute escaping and
filtering. Zero, false, and whitespace keep normal `Attr` behavior. Omitted
values leave any existing attribute unchanged. Adding attributes after
child output fails. `b.Open`, `b.Close`, `b.Text` and `b.HTML` support native-style
render helpers. The builder is valid only during its render. It cannot write
HTML text into a JavaScript, CSS, or attribute context through a hidden callback.

`b.Data("action", "change->upload#select")` sets `data-action` as escaped
text. Use it for data attributes whose values are not URLs, such as Stimulus
actions. Unlike `Attr`, it does not infer URL or CSS filtering from the suffix.
The suffix must be a nonempty ASCII attribute name. Quotes and markup in the
value remain escaped. Like other attribute helpers, call it before child output;
void elements such as `<input>` and `<img>` also stay pending until the next
output (with or without `/>`). A following helper modifies that void element,
not its parent. `b.Open("input")` follows the same rule. Attribute helpers also
work before content inside `<textarea>` and `<title>`; their content keeps its
normal escaping rules.

```gox
<input type="text">
decorate(false)
<textarea>
    decorate(false)
    value
</textarea>
```

`value | format` means `format(value)`; `value | format(arg)` means
`format(arg, value)`. Integer bitwise OR keeps its Go meaning. Pipelines require
a callable right side and a single result.

### Typed ambient inputs

Use ambient context for inherited presentation choices. Pass ordinary data,
such as list items or view models, explicitly or through a registered implicit
context type. Keep extras and ambient parameters last, in that order.

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

An `ambient` annotation reads the named key from the ambient passed to the
callee. The function must declare exactly one `*evmruntime.Ambient` parameter.
The bound input cannot be passed explicitly in XML or ordinary GoX calls.
Missing or nil values yield the input type's zero value; other values must match
its type. Reading does not change ambient or remove its key. Child overrides
are evaluated once and leave the parent's ambient unchanged.

`ambient` cannot combine with `optional`, `implicit`, or `legacyName` metadata.
Host adapters can inspect `Parameter.Ambient`. `gox.AmbientSchema()` exposes the
native Ambient type and its methods for host-bound helpers; register its bound
package and use its full Go import path when crossing native boundaries.

## Whitespace and output formats

Indentation and newlines between Go/markup statements emit nothing. Add spaces
and newlines explicitly in strings. Raw strings preserve their contents, useful
inside `<pre>`:

```gox
<pre>
    "first\n  second\n"
</pre>
```

Inline JS/CSS retain their native source whitespace and use `{=value}` for data:

```gox
<script>const customer = {=customer};</script>
<style>.button { color: {=color}; }</style>
```

Rendered output has no automatic pretty-print mode: it could change inline text or `<pre>`
semantics. `gox.Options{Format: gox.TextFormat}` renders top-level expressions as plain
text. `gox.CSSFormat` treats literal strings as CSS source and dynamically rendered
expressions as contextually escaped CSS data. Both use the same Go frontend,
type analysis, nil guards and EVM compiler; HTML markup requires `HTMLFormat`.

## Safety and failures

The contextual analyzer and escaping functions are copied from Go's
`html/template`; see [upstream notes](internal/htmltemplate/README.md).
Compilation chooses filters for HTML text, attributes, URLs, srcsets, JavaScript
and CSS. Runtime calls those filters directly; it never executes a Go template.
The copied upstream tests and end-to-end comparisons cover the escaping behavior.

Template source and host packages are trusted code. Data is untrusted.
`gox.HTML`, `CSS`, `JS`, `JSStr`, `URL` and `Srcset` have Go's explicit trusted-content
semantics; converting untrusted data to one of these types bypasses protection.
Dynamic `script`/`style` types are rejected. Runtime attribute helpers cannot
change those types or meta-refresh parsing. Their other attribute names and
values pass contextual validation. Rendering operations also check their input
context at runtime to prevent callbacks from bypassing the compiled filters.

Nil pointer field accesses in output expressions report a `Diagnostic` and skip
that expression or component call. Attribute failures omit the whole attribute.
Inline JS/CSS use a context-safe empty/null replacement so omission cannot join
unsafe tokens. Guards preserve operand order and short-circuiting without defers.
`Strict: true` turns these failures into render errors; other EVM errors always
fail the render. `Report` defaults to `slog.Warn` and can be replaced by the host.
This does not recover arbitrary host panics or silently swallow script failures.

Branches, loop iterations and components must contain complete elements.
`return`, `break` and `continue` close elements they leave. Labeled jumps across
open elements are rejected. Malformed syntax, incompatible input types and
ambiguous escaping contexts fail compilation with source positions.

## Native host callbacks

For a renderer returned by guest code, retain its value with
`program.EVM().FunctionValue(value)` and call
`program.RenderFunction(ctx, handle, args, options)`. The callable must take a
builder first and return no values. Closures and bound methods retain captures;
render sessions reset their globals and builders between calls. Output limits,
escaping, budgets and program ownership checks still apply.

To render within a handler's existing execution state, use
`program.RenderFunctionInCall(call, handle, args, options)` inside a native
call (or `VM.RunNative`). Globals, captures, context and budget stay shared
with that invocation. Nested renders use separate builders. `options.Budget`
must be zero; set limits on the enclosing invocation instead. Execution errors
from the callback fail that invocation, as with other synchronous EVM callbacks.
Errors detected afterwards while finalizing output are returned to the host.
No partial output is returned on failure. Native callback wrappers should use
`Call.CallbackError` or `Call.PanicCallback` to preserve guest panic recovery.

Builder arguments are ordinary borrowed native pointers. They must not escape
their render or be used concurrently. Runtime operations use the explicit
pointer and verify its rendering program; they do not select a builder from
context or enforce the borrowed-pointer lifetime.
Markup in a function literal with its own builder parameter uses that builder;
the parameter must have a name other than `_`. Literals without a builder
parameter can capture the enclosing renderer's builder.

Use `gox.Schema()` in the host binding's `evmbind.Options.Imports` for the
native GoX import path. The process-wide schema shares the actual `Builder`
and trusted-content type identities with the runtime package; it does not
expose Builder's private fields.

A native field of type `*evm.FunctionOf[func(*gox.Builder)]` can hold a guest
renderer closure or bound method. Invoke that handle with
`Program.RenderFunction`, which supplies the live builder and rendering context.
Captured typed view data survives the invocation that returned the renderer.
Native helpers taking `*gox.Builder` receive that same pointer and can call its
normal methods. As in native Go, do not retain or concurrently use the builder
outside its render.

## Class discovery

`Template.Classes` includes static class attributes, constant interpolations and
constant arguments to `class`, `Class` and `b.Attr("class", ...)`, with source
positions. `bg-{tone}` is dynamic and does not falsely contribute `bg-`.
`DynamicClasses` flags values that need a separate Tailwind safelist or host
policy. Arbitrary host helper output cannot be enumerated by this compiler.

## Source tooling

[GoX editor support](../_editors) covers Zed, Vim, VS Code, TextMate, Sublime Text, Neovim, and Helix.

From a checkout, install the standalone tool:

```sh
go install ./cmd/gox
gox check page.gox          # syntax only
gox fmt page.gox            # formatted source on stdout
gox fmt -w views/           # format .go and .gox files recursively
gox fmt -l views/           # list files that need formatting
cat page.gox | gox fmt      # stdin; -filename sets its path for import discovery
```

`check` checks Go parsing and balanced markup without resolving names, imports,
call signatures or rendering contexts. It accepts unresolved host packages.
Successful syntax validation does not imply successful compilation. Both commands
report source filenames and positions on errors and return a nonzero exit status.
`fmt -l` lists differences without changing files; `fmt -w -l` also writes them.
Directories skip hidden subdirectories and `vendor`. Invalid files are not written.

The same operations are available as APIs:

```go
if err := gox.CheckSyntax("page.gox", source); err != nil {
	return err
}
formatted, err := gox.FormatSource("page.gox", source, hostPackages...)
if err != nil {
	return err
}
lowered, err := gox.Preprocess("page.gox", formatted, hostPackages...)
if err != nil {
	return err
}
fmt.Print(lowered.GeneratedSources["page.gox.go"])
tmpl, err := lowered.Compile()
if err != nil {
	return err
}
// Link tmpl with the matching host implementations, then render it.
```

`PreprocessSources` handles a mixed Go/GoX package; `PreprocessFiles` also selects
an entry by renderer name or filename. All three have `Options` methods for text
and CSS output. Preprocessing performs typed lowering and checks the resulting
Go against the supplied EVM package descriptions. It needs the same package
environment as compilation, but does not generate bytecode, link implementations,
or run a VM. `Compile` reuses that result and checks EVM-specific restrictions.
Existing compilation entry points use the same pipeline.

`GeneratedSources` is compiler output for debugging and learning, with source
position directives. It is not a standalone native Go program: generated calls
refer to a separate rendering plan containing markup and escaping operations.
The preprocessing result retains that plan for `Compile`; treat it and its
exported map as immutable.

| Operation | Package environment needed? | What success establishes |
| --- | --- | --- |
| `CheckSyntax` / `gox check` | No | GoX and Go syntax is accepted |
| `FormatSource` / `gox fmt` | Optional for host import paths | Source is formatted; unresolved names can remain |
| `Preprocess` | Yes, for referenced host packages | Typed lowering and contextual analysis succeeded |
| `Compile` | Yes, for referenced host packages | The result can be represented as EVM bytecode |
| `Link` | Compatible implementations | Required runtime bindings are available |

A generic CLI does not know an application's virtual packages. Full preprocessing
belongs in the application or tooling that supplies that environment. The EDB
CLI provides syntax checking and formatting without requiring it.

### Formatting rules

Formatting has no style options:

- Go code follows `gofmt`, with tabs for indentation. Each paired tag or fragment
  adds one indentation level. Required parentheses around `<` comparisons remain.
- A single body expression or child element can share a line with its paired tags.
  Multiple body values use separate lines.
- Merge a complete element or opening tag when its line is **80 columns or
  less**. Split above **80 columns**, regardless of the previous layout.
  Indentation counts; each tab counts as four columns. Long literals and
  indivisible expressions can exceed the limit.
- Multiline opening tags use one attribute per line, indented one level, with
  `>` or `/>` on its own line aligned with the opening `<`. Attribute order stays
  unchanged. Self-closing tags have a space before `/>` on one line.
- Attribute expressions use `{expr}` with normal Go spacing. String contents,
  raw strings and literal JS/CSS bodies keep their contents and whitespace.
  JS/CSS bodies are not reformatted as Go.
- Preserve the package clause, comments and blank-line separation.
- Organize imports with `goimports`, including adding missing imports and removing
  unused ones. Retain aliases and group standard-library and other imports.
  GoX runtime and EVM standard packages are known automatically. Host paths come
  from the `evm.Package` descriptions passed to `FormatSource`; the `gox fmt`
  CLI has none, so it adds only standard library and GoX runtime imports. Module
  discovery uses the source filename. Unresolved packages can remain unresolved
  without failing formatting. Component selectors and attribute expressions
  participate in import discovery.

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

| Symptom | Check |
| --- | --- |
| Text is treated as an identifier | Quote literal text: `<p>"Hello"</p>` |
| A `<` comparison looks like markup | Parenthesize it: `if (a < b) { ... }` |
| A missing argument is not inherited | Check its parameter name, lexical scope, type and whether the call is inside markup |
| A component is unknown | Use its exact case-sensitive Go name and make its package available |
| `undefined: x (add import "...")` | The file names package `x` without importing it. Add the import, or run `gox fmt` / `FormatSource` |
| Spaces between words disappear | Source indentation emits nothing; add spaces inside strings |
| Attributes fail after calling a helper | Attribute helpers must run before the element's first child output |
| A branch leaves an element open | Keep complete elements within each branch and loop iteration |
| `check` succeeds but compilation fails | Syntax checks do not resolve imports, types, escaping contexts or EVM restrictions |
| Generated Go cannot run by itself | It refers to the separate rendering plan retained by `Preprocessed` and `Template` |
| A value silently disappears | Inspect nil-field diagnostics or render with `Strict: true` |

For debugging, start with `CheckSyntax`, then use `Preprocess` with the same
package descriptions as the application. Inspect `GeneratedSources` to see
implicit arguments, component calls and output operations after lowering.
Compile the result to check EVM-specific restrictions, then link and render with
strict diagnostics. Preserve the original filename so errors identify the source.

## Validation

```sh
go test ./...
go test -race ./...
go test ./gox -run '^$' -fuzz FuzzCompileGoX -fuzztime=15s
go test ./gox -run '^$' -bench BenchmarkApplication -benchmem -benchtime=200ms -count=8
```

The [application benchmarks](_docs/BENCHMARKS.md) use anonymized production-derived
card, history and comparison templates, each at two sizes. Both engines render
identical output to immutable strings using reused buffers. Compilation, linking
and view-model preparation are excluded.

On Apple M5 Max with Go 1.27.3, GoX uses 27–40% less execution time than cached
`html/template` across these six workloads (eight samples, 2026-11-06). The report
includes allocation counts, before/after samples, EVM regression controls and
reproduction commands. `BenchmarkRender` remains a small diagnostic workload.

### Plain text or trusted HTML

Use `gox.Content` for inputs such as titles that accept either text or markup:

```go
func Title(b *gox.Builder, title gox.Content) { <h1>title</h1> }
// XML callers use title={gox.Text("<plain text>")} or
// title={gox.TrustedHTML(gox.HTML("<em>trusted markup</em>"))}.
```

`Content` has `Content string` and `TrustedHTML bool` fields. Its zero value is
empty plain text. `Text` escapes text for its output context; `TrustedHTML`
asserts that the caller trusts the markup, without sanitizing it. Attribute
contexts still apply contextual escaping. Native helpers can accept the same
struct. `b.Text(...)` remains the imperative builder operation.

EVM stores Content and common Optional specializations in one cell. Public Go
fields and pointer semantics are preserved. See [compact value representations](_docs/COMPACT_VALUES.md)
for supported optional payloads, fallback layouts, and host binding details.

Native component bindings preserve parameter names and declaration comments.
`//gox:arg` optional overrides also apply to imported native functions exposed
through `evmbind`, so their XML callers can omit optional plain-typed inputs.

`gox.PackageSource` and `gox.PackagesSource` preserve argument annotations in
printed package declarations. They use `evmcompiler.DeclarationOptions.Annotations`,
a generic comment-selection callback; the EVM printer has no GoX-specific rules.

## Execution coverage

Pass `evm.NewCoverage(program.EVM())` as `RenderOptions.Coverage` to collect
instruction counts and branch outcomes across renders. Reports use original
GoX source locations. Use one collector per concurrent run, then merge them.
A render without this option does not collect coverage, including when it
reuses a previously instrumented render session. Rendering inside a native
call inherits that call's collector; set it on the enclosing EVM session.

Source coverage excludes compiler-generated nil guards and their fallback
paths. Raw instruction coverage still records them. Explicit template
conditions require both outcomes.
