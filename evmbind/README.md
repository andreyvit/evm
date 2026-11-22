# Source-defined host bindings

`evmbind.New(guestPath, source, options)` builds a script-facing schema directly
from a Go package's own source files. Supply the source as an `fs.FS`
containing the package's `.go` files—usually through `//go:embed`:

```go
//go:embed app.go
var source embed.FS

schema, err := evmbind.New("app", source, evmbind.Options{
    Functions: map[string]any{"Pluralize": Pluralize, "Discount": Discount},
    Types:     []reflect.Type{reflect.TypeFor[Store](), reflect.TypeFor[User](), reflect.TypeFor[Cart]()},
})
```

By default, the schema exposes everything the source exports: every type with
its exported fields and methods, every package function, and every constant.
Function bodies, unbound variables, unexported declarations, unexported struct
fields, and `_test.go` files are ignored, so you can embed real implementation
files as they are. The source supplies declarations, parameter names, and doc
comments without ever being executed. See the
[runnable example](../examples/gox-basic).

Because Go cannot look up functions or types by name at run time, the host
provides the Go values:

- **`Options.Functions`** maps each exported package function to its Go value.
  Alternatively, you can implement package functions as methods on an
  `Options.Host` type taking a leading `*evm.VM`, and
  supply the receiver when calling `schema.Bind(host)`.
- **`Options.Types`** registers native Go types that no bound function, method,
  or struct field reaches on its own (reachable types are discovered
  automatically by reflection). Register concrete context types with
  `Pointer[T]()`.
- **`Options.Values`** maps exported package variables to their Go values. Only
  explicitly bound variables are exposed, and the source must declare their
  types. Scripts can read these variables and call their methods, but cannot
  reassign them.

Schema construction fails—naming the offending declaration—if an exported
function has no binding, a type has no native Go type, or a declaration uses an
unsupported type. To trim the API, use `Options.Omit` (by name, or
`Type.Method` for methods—types still referenced by exposed declarations are
kept), `Options.Methods` (methods per type), and `Options.Fields` or
`Options.OmitFields` (fields per type).

Compile scripts against `schema.Package()`, and link each module against
`schema.Bind(receiver)` (or `schema.Bind(nil)` when there is no host receiver).
You can acquire concurrent VMs from a linked program with
`program.AcquireVM(ctx)`. Remember that a **schema** owns nominal type
identities, while a **program** owns the numeric dynamic type IDs used during
execution.

Store each schema in a host-level `sync.OnceValue`. Parsing, type checking,
enum extraction, and native layout binding then happen once per binary, and
every environment shares `schema.Package()` and its type identities.

## Expose part of a package

When you want to expose only a slice of a larger Go package without duplicating
its declarations, set `Options.Only`. The schema then includes:

- The listed types and any local types they reference.
- Methods listed in `Options.Methods`.
- Functions bound in `Options.Functions` and `Options.GenericFunctions`.
- Typed constants belonging to exposed types.

`Options.Selective` works the same way, starting from every exported type.
Complete constant blocks are type-checked so `iota`, omitted expressions, and
aliases keep their exact native values, while constants of unrelated types stay
hidden. Struct pointers keep their native layout, and exported embedded structs
and struct pointers preserve Go field promotion, value copying, and pointer
aliasing. `Options.Omit` cannot be combined with selective mode—list only what
you want to expose.

## Concrete generic structs

Use `Options.Instances` to bind concrete instantiations of a generic source
struct:

```go
Instances: []evmbind.Instance{{
    Type:   "Box",
    Native: reflect.TypeFor[Box[int]](),
    Args:   []reflect.Type{reflect.TypeFor[int]()},
}},
```

The source template supplies fields, constraints, and selected method
signatures; scripts write `Box[int]` and the binding preserves that instance's
native layout. Every reachable instantiation must be registered explicitly
because reflection cannot recover generic type arguments (mismatches fail
registration).

Field omissions and method selections use the template name and apply to every
instance. Native generic aliases and non-struct generic templates are not
supported. Generic struct instances currently use reflection, so `StaticOnly`
rejects them.

## Consolidated native type aliases

A source package can re-export an imported native type using a Go type alias
when both schemas share the same guest package path and keep the same type
name. The alias reuses the dependency's type, pointer shape, and native layout
without emitting a second guest declaration, and `Schema.Type` resolves the
alias name.

Same-name constant re-exports share the owning declaration as long as their Go
types and exact values match (conflicts fail schema construction). Merge the
dependency and consumer packages before compiling or linking, and register
native methods, field omissions, and interface implementations on the owning
schema. Renamed aliases and aliases across different guest packages are not
supported by this path.

## Native model arguments and results

When selecting native model source with `Options.Only`, pass the shared model
package in `Options.Models`. The model metadata already knows its native Go
types, so you do not need a separate Go-type-to-model registry.

- Set `NativePackage` if the original Go package path differs from the script
  path. It can also be inferred from `Types`, `Implementations`, or
  `Static.Types`; set it explicitly when those roots include unrelated packages
  that happen to share a local type name.
- Model struct fields are stripped before dependency selection. Their script
  type is the model package's immutable row type, not a duplicate native struct
  declaration.

Import that schema into callable host schemas through `Options.Imports` and
link the shared model package alongside it:

- Native pointers keep their identity and `nil` value; native values use
  ordinary Go struct assignment.
- Encoded rows and rows backed by a different native struct require explicit
  conversion before being passed to a native pointer parameter—bindings never
  decode, cache, or copy changes back implicitly.
- Native-backed immutable rows are live views: host code can still mutate the
  underlying Go object.

### Borrowing byte slices from native methods

A native method that returns a single `[]byte` can lend its backing bytes to
the guest instead of copying them. Annotate its Go declaration with:

```go
//evm:borrow-bytes domain [nativeMethod]
```

The method must keep its arguments local and must not invalidate outstanding
borrows; the receiver supplies the borrow scope. If the public Go method
returns an owned copy, you can pass an optional `nativeMethod` (with the same
signature) that supplies the borrowed bytes instead. Both generated and
reflection bindings expose the original guest method name. Guest reads use the
lent storage directly, while any write, mutable address, or escape triggers a
single writable copy shared across all slices of that result.

### Editable model slots

To expose selected native model pointer parameters or results as editable rows
in a callable schema, list them in `Options.Mutable`:

```go
opt := evmbind.Options{
    Imports: imports,
    Mutable: map[string]evmbind.MutableSlots{
        "GetCustomer":    {Results: []int{0}},
        "UpdateCustomer": {Params: []int{0}},
    },
}
```

Slot indices are zero-based and exclude the method receiver and any host-only
`*evm.VM` (method keys use `Type.Method`). Pass the same `Mutable` options to
`Generate`. Unknown names, out-of-range indices, or non-model / non-pointer
slots fail schema construction. Each annotation applies only to that call site;
ordinary model pointers and `any` results stay immutable by default.

For a native `GetCustomer() *Customer`, an annotated result becomes
`*db.CustomerEdit` in scripts:

- Writing `customer.name = "Ada"` mutates the underlying Go struct immediately;
  native callbacks see the change without a flush, and pointer equality and
  interface boxing preserve identity.
- Editable encoded database records cannot be passed as native pointers without
  explicit conversion, and persistence remains the host's responsibility.
- Child reads keep native pointers (including `nil`), and whole-child
  replacement remains unsupported.
- Native slices keep their backing arrays (including nested scalar slices and
  slices of modeled pointers), while assigning a VM-cell slice to a native
  field requires explicit conversion.
- Native model maps require an explicitly bound native map layout; optional
  collection wrappers and slices of model struct values are not adapted
  implicitly.

Under the hood, editable row handles use two VM words to distinguish native
pointers from encoded editors. Native scalar writes allocate no editor, and
string writes follow the native setter's ownership rules. Programs that expose
native model slices use tagged addresses and general slice operations where
cell-only fast paths would be unsafe, while ordinary guest allocations need no
per-object registry.

Registered concrete generic models keep their shared model identity (only
registered instantiations may cross the model boundary), aliases resolve to
their original native types, and generated and reflected wrappers follow the
same rules. Local model source requires a selective schema; callable sources
import that shared schema.

## Omit host-only fields

Use `Options.OmitFields` to hide specific exported fields from scripts, mapped
by local type name. Omitted fields are removed before dependency selection, so
hiding a context or service field prevents its interface from being pulled into
the script API. Unknown types, unknown fields, and duplicates fail schema
construction (unexported fields are always omitted automatically). Pass the
same omissions to `Generate`.

Omitting a field changes only the script declaration, never the Go struct
layout. Native value copies keep omitted fields intact in an inaccessible
shallow snapshot (allocations count against the VM budget) and copy them on
assignment. Fresh native storage starts with omitted fields zeroed, whereas
assigning a fresh script literal over existing native storage updates only the
exposed fields and preserves the destination's omitted fields. Script field
operations see only exposed fields, while equality (`==`) and map hashing
include omitted fields via the snapshot.

## Share declarations across packages

`Options.Imports` maps native Go import paths to previously built schemas. The
native source keeps its ordinary Go imports, while script signatures and fields
reuse the imported schema's guest package and type identity. Link every
dependency package alongside the consuming schema so local and imported structs
keep native pointer aliasing across transitive dependencies.

A few rules keep shared schemas clean:

- Each native import path must resolve to a single shared schema instance
  across the import graph. If multiple consumers need different subsets of a
  package, combine them into that one schema.
- Standard declarations are shared across schemas and initialized once. Several
  native packages may also emit into one guest package without losing Go type
  identity.
- Pass the same `Imports` map to `Generate` so generated code imports the real
  native Go packages.
- Imported layouts stay owned by the dependency schema and are never mutated by
  consumers. Imported interfaces reuse the owning schema's registered
  implementations and conversions (including typed nils).
- Link the owning schema's `Bind(nil)` (or `Bind(host)` if it has package
  functions) to install its methods. Scripts do not need to import that package
  just to receive an implementation through another package's API.

## Generic native functions

Register compiled instantiations of generic package functions with
`Options.GenericFunctions`:

```go
GenericFunctions: map[string][]evmbind.FunctionInstance{
    "Var": {
        {Args: []reflect.Type{reflect.TypeFor[string]()}, Function: forms.Var[string]},
        {Args: []reflect.Type{reflect.TypeFor[int64]()}, Function: forms.Var[int64]},
    },
},
```

Scripts call the original generic signature with inferred or explicit type
arguments (`forms.Var(&name)` or `forms.Var[string](&name)`), and native
pointer arguments keep their storage. The host must register every supported
instantiation—unregistered types fail at compile or link time rather than
specializing at run time. Constraints and signatures are validated during
schema setup.

Register any generic structs that appear in those signatures (such as
`Binding[string]`) through `Options.Instances` as well. Type arguments must
match registered VM type identities (a script-defined named type does not match
its underlying native scalar). Generic function instances currently use
reflected calls, so generated bindings and `StaticOnly` reject them. Generic
function *types* (such as `Validator[T]`) are separate and not yet supported.

## Generate bindings

To generate direct, reflection-free Go wrappers for a whole package into a
separate bindings package, run:

```sh
go run github.com/andreyvit/evm/evmbind/cmd/evmbind \
    -dir ../api -path demo -native example.org/project/api \
    -package apibindings -o bindings_gen.go
```

For selective schemas or custom options (`Only`, `Selective`, `Omit`,
`Imports`, and so on), call
`evmbind.Generate(path, nativeImport, outputPackage, source, options)` from a
small generator program using the same `Options` as `New`.

Supply the generated bindings via `Options.Static: &apibindings.Bindings`:

- **`evmbind.Automatic`** (default) uses generated wrappers when both the
  source fingerprint and the full set of wrapper names match, and falls back to
  reflection when bindings are missing or stale.
- **`evmbind.StaticOnly`** enforces freshness: `New` panics on a mismatch, and
  `RegisterMethods` returns an error.
- The **`evmreflect`** build tag excludes generated files from a build, letting
  you rename or remove native APIs before regenerating (supply a zero `Static`
  value under that tag when a package builds in both modes).

### Variadic functions and methods

Native package functions and methods support variadic parameters, including
through interface dispatch and bound method values. Calls follow Go semantics:
omitted arguments produce an empty tail, and `values...` keeps the supplied
slice's backing array. Generated wrappers use Go's `...` call syntax, while
reflected wrappers use `reflect.Value.CallSlice`. Variadic calls use the
ordinary call window rather than direct-call opcodes. Variadic native function
*fields* are not yet supported.

## Native values

### Typed codecs (`evmbind.Codec[T]`)

Resolve a bound Go type once during setup with `evmbind.Codec[T](schema)`:

```go
customerCodec := evmbind.Codec[*Customer](shopSchema)
tagsCodec := evmbind.Codec[map[string][]string](shopSchema)
```

`Codec` panics if it cannot resolve a unique conversion. Use
`evmbind.TryCodec[T](schema)` to handle setup errors.

- `codec.Type()` returns the bound `*values.Type` for `program.LookupFunction`.
- For ordinary bound native struct pointers, `codec.Pack(ptr)` and
  `codec.Unpack(v)` convert without copying or reflection. They preserve the
  live Go address and map a typed `nil` pointer to/from the zero `values.Value{}`.
- For slices, maps, structs, and other bound types (or metered conversions in
  general), call `codec.PackIn(vm, x)` and `codec.UnpackIn(vm, v)` so
  allocations and bounds are checked against the active `*evm.VM` budget.
  On a non-pointer codec, `Pack` panics and `Unpack` returns an error pointing
  to `PackIn` / `UnpackIn`.
- Codecs follow the schema's declared types and static-only policy: unsupported
  or unregistered Go types fail at setup rather than registering at run time.
  Because `values.Value` slots carry no runtime type tag, callers must match
  each argument slot to its parameter type in the checked signature.

### Interfaces

Pass a host interface value across the Go boundary with
`schema.PackInterface(program, ptr)` and recover it with
`schema.UnpackInterface(program, v)`. The concrete type must be registered and
implement a declared interface. In the EVM value, `R` carries the program's
nonzero type index and `P` holds the original GC-visible Go pointer—a typed
`nil` preserves its type index with `P == nil`, whereas untyped `nil` packs as
the zero value. (`program.PackInterface` and `program.UnpackInterface` wrap and
unwrap already-packed EVM values.)

- Both generated and reflected paths expose only declared interface methods,
  and generated code dispatches through the native Go interface directly.
- Context values passed back to native code must come from a registered host
  implementation; repack them for the destination program when crossing
  programs.
- Use `PointerForCall` when a receiver needs the active `*evm.VM`
  (for example, to open or close a budget scope): it may return a shallow
  receiver copy for that call while leaving the original pointer in the guest
  interface.
- Embedded host interfaces can be passed wherever a smaller method set is
  expected.
- `evmbind` binds registered native Go implementations into script interfaces
  (Go → script); it does not synthesize runtime Go proxies for arbitrary
  script-defined types. For built-in guest-defined `error` bridging and the
  precompiled Go adapter pattern for arbitrary interfaces, see
  [Scripts implementing Go host interfaces](../_docs/embedding.md#scripts-implementing-go-host-interfaces).

### Data structs, pointers, and collections

The data boundary supports booleans, integers, floats, strings, named scalars,
nested data structs with exported fields, and pointers to data structs or
scalars:

- Data structs cross by value. Host pointers keep their Go address so guest
  fields, aliases, escaped field addresses, and native methods all operate on
  the same object. Prefer `evmbind.Codec[*T](schema)` (`codec.Pack(ptr)`) for
  entry arguments; `schema.NativePointer(ptr)` provides an untyped check when
  the Go type is held as `any` (the host must synchronize concurrent access).
- Guest allocations and package variables of registered data structs and
  scalars use native Go storage too. Other guest addresses backed by VM cells
  use the call-scoped temporary and copy-back boundary.
- Typed slices and maps of supported native types—including `map[string]any`
  and nested collections—keep their Go backing storage, element mutations, and
  aliases (slice growth may replace the backing array, as in Go). Numbers
  retain their Go types.
- Native method-interface fields, slice elements, and map values preserve the
  concrete native object and typed nils. Only registered native implementations
  can be stored in those slots (reading an unregistered concrete type—including
  an `error` implementation—fails with an unregistered-type error rather than
  synthesizing a proxy). Interface map keys and unsupported declarations fail
  schema creation.

### `any`, model rows, and `error`

- **`any`:** Works in arguments, results, struct fields, and typed slices.
  Registered native values keep their Go types and identity; guest-only types
  reach Go as `*evm.GuestValue` (carrying their owning program and exact VM
  value, and rejected by any other program). No JSON conversion happens.
  Schemas exposing `any` automatically link their registered concrete and
  primitive scalar types.
- **Model rows through `any`:** When an immutable model row is linked, a
  registered native model pointer inside `any` becomes that row type and keeps
  its original pointer (including typed nils) on return. Multiple Go types can
  share one model; if one Go type belongs to several linked models, pass a
  `kvo.Record` with its model to disambiguate. Modeled `kvo.Record` values keep
  their representation (encoded rows return without decoding), and guest-created
  `nil` rows return as missing records with the model attached. Model rows take
  precedence over a separately exposed native struct type. This applies to
  pointers and known-model records, not native struct values or unmodeled
  records.
- **`error`:** Built-in `error` works in arguments, results, struct fields, and
  map values. Native and guest errors preserve identity, `nil`, and `Error()`
  through EVM's error bridge in both generated and reflected bindings (see
  [Scripts implementing Go host interfaces](../_docs/embedding.md#scripts-implementing-go-host-interfaces)
  for `OpenCallbackScope` after `Run` or `Release`). Native `[]error` storage is
  not yet supported.

## Native package functions

Use `Options.Functions` to bind source declarations directly to Go function
values or closures:

```go
schema, err := evmbind.New("db", edb.TransactionBindingSource, evmbind.Options{
    Only:      []string{"RawRange"},
    Functions: map[string]any{"RawPrefix": edb.RawPrefix},
})
```

In a selective schema, only bound functions enter the guest package, and the
types they reference are pulled in recursively while function bodies are
ignored. Binding validates argument and result types against the supplied Go
function, calls it directly without a synthetic `*evm.VM` or host receiver,
and preserves native reference sharing on return.

`Generate` uses the same options and selection, invoking the registered
function value directly. Because the fingerprint distinguishes package
functions from host methods, generated wrappers for one form can never silently
serve the other.

## Retained functions and callbacks

EVM gives you typed and untyped handles for passing script functions across the
host boundary.

### Typed function handles

- **`*evm.FunctionOf[func(int) int]`** (type-aliased to `evm.Callable`) accepts
  named guest functions, closures, bound methods, and native functions; Go sees
  `*evm.Callable`. The type argument supplies the guest signature so
  incompatible functions fail compilation, and both generated and reflected
  bindings accept and return these callables as script function values without
  boxing them in `any`. Returned callables must belong to the current program
  and match the declared signature (`nil` round-trips as `nil`). Retaining a
  closure keeps its captures and owning `*evm.Program` alive; invoke it with
  `vm.Run(*fn, slots)` on an acquired `*evm.VM` (or synchronously on the active
  `*evm.VM` inside a binding). If a host requires a named, non-capturing guest
  function, check `fn.Name()` and `!fn.HasCaptures()`.
  - The signature may include nested function parameters and slices (such as
    `func(func([]string), func([]any))` for a header/row producer). Those
    values stay in VM storage, so invoke the callable with program-owned native
    callbacks.
  - Struct fields may also use `FunctionOf`: reads, writes, address-taking, and
    struct copies translate through the current program (foreign callables fail
    before invocation), stores meter the retained callable, and Go keeps an
    ordinary typed pointer field.

### Untyped (host-validated) handles

Use **`*evm.FunctionOf[any]`** (input-only; scripts see an `any` parameter)
when the host itself validates the signature at registration (for example, a
GoX renderer whose data type is declared by the script). It accepts named guest
functions, closures, bound methods, and native functions, returning
`*evm.Callable`. Both untyped `nil` and typed `nil` functions yield a `nil`
pointer, while non-functions fail at the call boundary. Validate the contract
with `fn.CheckSignature(want)` (and inspect `fn.Signature()`, `fn.Name()`, or
`fn.HasCaptures()`) before publishing or invoking.

### Native Go function types and callback scopes

Ordinary Go function types can also appear in data fields and method
signatures. Native calls and callbacks run synchronously on the active VM's
goroutine with its globals, context, and remaining budget, and must be
serialized like any other use of that VM (EVM provides no blanket guarantee of
guest race safety on shared captures or native pointers). Guest panics unwind
native defers and remain recoverable by guest callers.

- **Idle and post-release callbacks (`OpenCallbackScope`):** Native Go function
  wrappers and script-defined `error`s remember their originating VM's
  `CallbackScope`. While a host binding is actively running, they re-enter that
  VM directly (no `OpenCallbackScope` needed). If host code needs to invoke them
  *after* `vm.Run` returns, open `scope, err := vm.OpenCallbackScope(limits)`
  while `vm` is idle and `defer scope.Close()`. Every callback invoked outside
  an active host binding—whether `vm` is idle or already released—acquires a
  fresh VM lease from the same program and Go context with fresh package globals
  and a **fresh per-call budget of `limits`** (zero quotas are unlimited,
  matching `vm.SetBudget`; pass `program.DefaultLimits()` to reuse the program's
  defaults). Without an open scope, idle calls panic with
  `ErrCallbackInactive`, and `vm.Release()` expires them (`ErrCallbackExpired`).
  Keep `ctx` valid until `scope.Close()` and serialize calls sharing one scope.
  Native Go errors and native-created functions are unaffected.
- **Identity without wrapper stacking:** Native callable values remember their
  original Go function value, and guest-created functions read back from native
  storage recover their program-owned handle (tracked weakly and cleaned up
  after the Go wrapper is collected), so round-tripping never accumulates
  wrapper layers. Reading a native function field copies the function value, so
  later field writes do not alter an already-loaded reference. Variadic native
  function types are not supported.

## Direct calls and fields

A generated wrapper qualifies for a direct-call instruction when it is:

- a function with up to two arguments, or
- a pointer method with up to one explicit argument,

returning zero or one result over words (including named scalars and floats),
strings, and true pointers. Host functions and registered record methods share
the same signature classifier and scalar conversion generator. The compiler
emits the specialized opcode matching the wrapper's signature; reflection and
older wrappers stay on the general call ABI, and interface methods use virtual
dispatch. For hand-written bindings, `Direct` alone also handles function
values and deferred calls (supply `Call` alongside it to override the window
wrapper).

A generated pointer wrapper passes native addresses directly—only VM-cell
arguments need a temporary and copy-back. Returned strings are charged once and
keep their Go data pointer visible to the GC. In a hand-written direct binding
that returns a KVO row, construct its typed pair with `VM.RecordValue`.
Direct callbacks may return conversion or execution errors; expose fallible
application operations through `Native`.

Generated output also provides typed struct field access:

- `schema.NativeFields(goType)` returns the shared `nativebind.Struct`, and
  `schema.NativeType(goType)` returns its nominal `values.Type`.
- `kvo.Binding.WithNativeSchema` and `Projection.WithNativeSchema` reuse both,
  so KVO property access and host methods operate on the exact same Go object.
- KVO's record codec generator shares the same field generator for types that
  need persistence bindings without script exposure.
- Generated field access (including escaped scalar addresses) uses typed Go
  operations, while fallback bindings share `nativebind`'s reflection path.
  Regenerate whenever bound declarations change.

## Explicit record methods

You can expose ordinary Go helpers as methods on a specific KVO model. The Go
function name, model, and script method name are completely independent—there
is no naming convention or prefix stripping.

For example, in a native model package:

```go
func Customer_PointBalance(r kvo.Record) int64 {
    return r.Get(PointsEarned) - r.Get(PointsRedeemed) - r.Get(PointsExpired)
}
```

Declare the script-facing mapping in a separate package:

```go
func RecordMethods() []evmbind.Method {
    return []evmbind.Method{
        evmbind.RecordMethod(nm.MCustomerStatsOverall, "PointBalance",
            nm.Customer_PointBalance, evmbind.BorrowOnly),
    }
}
```

A small `go:generate` program imports that list, calls
`evmbind.GenerateMethods("bindings", RecordMethods())`, and writes the
generated Go file to package `bindings` (which must be a different package from
the helpers themselves). Register the generated `MethodBindings` **before
compiling scripts or publishing the package**:

```go
pkg := must(evm.ModelPackage("m", nm.Schema))
ensure(evmbind.RegisterMethods(&pkg, RecordMethods(), evmbind.MethodOptions{
    Mode:   evmbind.StaticOnly,
    Static: bindings.MethodBindings,
}))
```

`must` and `ensure` panic on error. Go callers invoke
`nm.Customer_PointBalance(r)` while scripts write `stats.PointBalance()`. Both
run the same helper over the original 16-byte record—whether it is Fast2,
Compact2, MessagePack, a native struct, or decoded memory—without decoding or
changing its representation.

Rules and limits:

- Methods attach only to explicitly registered immutable model types (editors
  have their own method set). One helper can be registered on multiple models,
  and registration rejects shadowed properties, existing methods, or duplicates
  without modifying the package.
- `BorrowOnly` is a trusted promise that the helper neither retains its
  arguments nor invalidates their backing storage (directly or transitively),
  allowing ownership analysis to skip unnecessary copies. Omit it for
  conservative behavior, or pass `NoRetain` and `NoInvalidate` separately.
  EVM does not infer these properties from Go code.
- Generated adapters call Go directly with no reflection or record boxing.
  `StaticOnly` rejects missing or stale adapters, while `Automatic` falls back
  to reflective calls (which allocate). Regenerate whenever a helper's
  signature changes.
- Generation requires exported top-level functions taking `kvo.Record` first,
  followed by scalar arguments and scalar results (`bool`, integers other than
  `uintptr`, floats, and strings, including named Go types—guest signatures use
  their underlying scalar types). Variadic functions, collections, record
  results, closures, and bound Go methods are not supported by the generator.
  Signatures beyond the direct-call limits use the ordinary call window; bound
  methods, method expressions, interface calls, and deferred calls all work.
- String results are copied and charged to the VM allocation budget so they can
  safely outlive borrowed storage; numeric and boolean calculations can stay
  allocation-free (the helper's own allocations remain its responsibility).
- Generated files respect the `evmreflect` build tag; pass an empty
  `StaticMethods` with `Automatic` under that tag if you want reflection-only
  development builds.

See [record-method benchmarks](../_docs/bench-record-methods.md), the runnable
generator in `internal/methodfixture/gen`, and the
[hand-written bindings guide](../_docs/bindings.md) for void calls, metadata
reuse, and manual `Shape` / `DirectBinding` declarations.

## Reflection-based discovery

When you do not have embedded source available, `evmbind.Discover` exposes
native functions and every type they reach using reflection. Prefer `New`
whenever source is available, because reflection has no parameter names, doc
comments, or constants. Exported fields are discovered recursively, unexported
fields stay hidden while keeping their native storage, and recursive references
preserve pointer identity.

```go
_, packages, err := evmbind.Discover("app", evmbind.Discovery{
    Options: evmbind.Options{
        Functions: map[string]any{"Foo": Foo}, // Foo returns *Bar
        Imports:   existingSchemas,
    },
    Packages: []evmbind.PackageExposure{
        {Pattern: "example.org/app/...", Methods: true},
        {Pattern: "example.org/app/models/...", Package: "app/models", Methods: true},
        {Pattern: "example.org/app/internal/secrets", Exclude: true},
    },
})
```

Pass the returned packages to `Compile` and `Link` alongside your existing
bound packages:

- Types mapped into the function package share it (`app.Foo` and `app.Bar` sit
  side by side), and each returned path appears once.
- Add named constants with `Discovery.Constants`, and extra type roots (such as
  concrete types passed through `any`) with `Options.Types`. The returned
  schema's `NativePointer` accepts any discovered struct pointer.
- By default, discovered types keep their native import paths with exported
  fields and no concrete methods (interfaces keep their method sets). Package
  rules can enable all exported methods, remap types into an umbrella package,
  or exclude a path. Rules match exact paths or `/...` subtrees (including the
  parent); longest match wins, exact beats `/...` at the same path, later rules
  break ties, and a matching rule replaces broader settings. Name collisions
  under an umbrella package fail explicitly.
- Existing schemas in `Options.Imports` keep their identities and API
  selections without being broadened (their referenced types must already be
  bound).
- Discovery uses reflection adapters and requires no generated wrappers.
  Because reflection cannot enumerate package globals, functions, or constants,
  register functions and constants explicitly (and use `ExternalValue` for
  host-owned globals). Concrete generic instances still require source
  bindings, and unsupported native shapes fail schema construction rather than
  being silently dropped. As with source bindings, pointers and supported
  collections keep their native storage and mutations.
