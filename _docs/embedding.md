# Embedding EVM: host contracts

The [README](../README.md) gives an overview of what EVM can do. This guide
covers the contracts a host application relies on when embedding the VM. For
bindings generated from Go source, see [`evmbind`](../evmbind/README.md); for
hand-written declarations and Go-backed storage, see
[binding declarations](bindings.md).

## Sessions and environments

A linked `*evm.Program` is immutable and safe to share across goroutines. An
`*evm.VM` is a single execution session that owns registers and package
variables; it is neither concurrent nor reentrant, so use one VM per goroutine.

- Package initialization runs once, right before the first entry call. Later
  calls reuse the session's package variables.
- `vm.Reset()` clears session state so the next call initializes again.
- Any execution failure invalidates the session until you call `vm.Reset()`.
- Entry results are written only on success. The VM never rolls back host side
  effects.

Compile and link programs during setup, and build schemas, models, and bindings
there too—keep all of that out of your request loop. Reusing a VM together with
its argument and result slices avoids per-call allocations once the VM's
internal storage has grown.

### Compiling and linking many scripts

If your host compiles and links many scripts against the same package set,
prepare the packages once:

1. Build an `evmcompiler.NewEnvironment(declared...)` for compiling. Each
   subsequent compile type-checks only the script itself.
2. Build a matching link environment with `env.NewLinkEnvironment(bound...)`.
   Each link then copies only the tables that belong to its module.

Programs linked through that environment share the host bindings, the linked
storage shapes of host types, and the method tables of host types whose methods
are all native (the VM and native bindings treat all of these as read-only).

Under the hood, a module compiled from an environment keeps native methods that
only appear in method tables in `MethodImports` (separate from the `Imports`
its bytecode calls directly), and selects host type shapes from the
environment's `HostShapes` list rather than repeating them in `Shapes`
(`ListedShapes` yields both). Because modules from one compile environment
share those tables, do not mutate them. A linked program still exposes only the
shapes its own module reaches—`Program.Shape` returns `nil` for the rest.

Both environments are immutable and safe for concurrent use, provided the
underlying packages do not change while they are active.

### Passing arguments and results

Low-level calls pass and return [`values.Value`](../values/README.md) according
to the entry signature. For ordinary Go values—including pointers to bound
native structs—use `RunEntryNative`:

```go
err := vm.RunEntryNative(ctx, "Bonus", []any{int64(1500)}, out, evm.Budget{})
```

Each argument must match the exact Go type of its parameter (there are no
implicit numeric conversions). Program-local interfaces and function references
belong to one program; if they cross to another program, repack them through
the destination program's APIs.

## Host packages

`schema.Package()` supplies compile-time declarations, and `schema.Bind(host)`
supplies the runtime implementations. Reuse the same schema across compiles and
links so nominal type identities match. Generated bindings call Go directly;
reflection is the fallback. Native pointers keep their Go identity and
mutations, so the host is responsible for synchronizing concurrent access.

### Package values

`Package.Values` exposes host-owned package variables such as `db.Customers`.
Each `ExternalValue` declares its type and a `Read` callback that returns the
current value.

Scripts can copy the value and call its methods, but they cannot reassign or
take the address of the package variable itself. If `Read` returns a pointer,
that pointer keeps its normal aliasing and mutation behavior. Reads use the
host-call ABI and must not invalidate borrowed records or change transaction
scopes. Package values appear as variables in printed package declarations.

### Hand-written packages

For full control without source parsing, build an `evm.Package` directly using
`Types`, `Functions`, and `Methods`; see [binding declarations](bindings.md).
Only exports you supply can satisfy script imports. Both `Compile` and `Link`
accept each import path once—use `evm.MergePackages` when several bindings
contribute to the same path.

A `*evm.Call` and its argument and result windows are borrowed only for the
duration of that callback:

- Read all arguments before overwriting overlapping result slots.
- Never retain the `*evm.Call` or its windows after returning.
- Honor `c.Context` cancellation inside your host callback.

### Native function values

To expose a Go closure at run time—for example, a returned iterator, a yield
callback, or bound host state—cache `NewNativeFunctionType(signature)` during
setup, then wrap the closure with `Call.NativeFunction(typ, closure)`.

Guest calls, deferred calls, and `Call.RunCallbackArgs` all accept these
values and share the current invocation's limits. Keep these rules in mind:

- A native function value belongs to the program that created it; another
  program cannot call it.
- Its captured host state can outlive the call that created it, so the host
  must enforce any borrowed transaction lifetime itself.
- Never capture the creating `*evm.Call`.
- A `nil` closure produces a `nil` function value.
- Allocating the function holder is metered against the VM budget, while the
  closure body and its own allocations remain the host's responsibility.

When passing a host callback *into* an initial `Run` call (such as a streaming
row writer), use `Program.NativeFunction(typ, closure)` instead. The host owns
that holder allocation, while execution still shares the VM's limits and
propagates callback errors. Prefer `Call.NativeFunction` during execution so
holder allocations stay metered.

### Retained functions

To keep a script callable past the call that produced it, retain it with
`Program.FunctionValue(value)` and store the returned `*FunctionHandle`. You
can invoke it later with `VM.RunFunction` or `VM.RunFunctionWithBudget`.

`*FunctionHandle` works for guest closures, bound methods, static native
functions, and runtime native closures:

- It keeps captures alive without copying them and rejects calls in any other
  program.
- The receiving VM supplies package variables and initialization, while mutable
  captured state stays shared even across VMs or after a `Reset`.
- Synchronize concurrent calls and validate any captured host resource
  lifetimes yourself.
- Signature inspection returns a defensive copy.

If you only want public, non-capturing entry functions, use the stricter
`NamedFunctionValue` API instead.

### Native field codecs

`TypeDecl.NativeCodec` provides contextual conversion for native struct fields
whose Go representation differs from their VM type. Its `Decode` and `Encode`
callbacks receive the current `*evm.Call` for program checks and allocation
accounting; they must leave its argument and result window untouched and must
not retain the call.

Native field access, address loads and stores, and struct snapshots all go
through these codecs. Linked programs copy codec declarations, and conflicting
declarations for the same type fail linking.

### Source packages and printed declarations

A package can also supply Go `Source` whose shared scalar types and methods
compile as guest code (see [shared scalar source](#shared-scalar-source)).

To generate documentation for a human or an agent writing a plugin, call
`evmcompiler.PackageSource`, `PackagePaths`, or `PackagesSource`. If you want
to include application-specific comment annotations on functions, pass a
callback in `evmcompiler.DeclarationOptions.Annotations` and call
`options.PackageSource(...)`. The callback receives the package path, function
name, and raw declaration comments, and returns the comment lines (including
their `//` prefix) to emit. Native bindings preserve comments in
`ExternalFunction.Doc`, while source packages read them from their Go
declarations. By default, no application annotations are added, and EVM does
not interpret them.

## Limits, errors, and trust

`VM.Limits` sets session-wide limits, and `RunWithBudget` sets per-call limits
(including initialization). Zero fields fall back to defaults or inherit the
session limit.

- **Time:** `Budget.Duration` and `ctx` bound elapsed time cooperatively;
  native host calls must check cancellation and return on their own.
- **VM meters:** Instruction, call-depth, register, allocation, and output
  limits apply to VM execution. Allocation accounting is cumulative until
  `vm.Reset()` and includes transient capacity—it is not live heap size.
- **Host work:** Native APIs run outside the VM's meters; they are responsible
  for their own CPU work and allocations unless they explicitly charge them.

Script panics and language errors can be caught by a direct deferred `recover()`
inside the script. Uncaught errors retain source positions and unwrap to their
underlying cause. Budget exhaustion, context cancellation, invalid-value
errors, and native callback errors are fatal and skip script defers. Native Go
panics inside hand-written callbacks propagate after VM cleanup. In all cases,
the host owns transaction rollback and resource cleanup.

> **Trust model:** Bytecode and native bindings are trusted inputs. `Link`
> validates operands and layouts, but it cannot prove arbitrary pointers safe.
> Always accept script source through the compiler rather than uploaded
> bytecode. EVM is a sandbox for API surface and execution budgets, not a
> process isolation boundary.

## Standard packages and output

Every script can import the VM's built-in packages: `errors`, `fmt`, `log`,
`math`, `strconv`, `strings`, `evmruntime`, and `evmruntime/log`. Formatting
supports standard verbs, flags, aggregates, custom formatting methods, and
wrapped errors. `errors.As` is omitted, and `log` has no process-exit or
configuration APIs.

[`evmstd`](../evmstd/API.md) adds general-purpose standard packages: `bytes`,
`cmp`, `encoding/base64`, `encoding/hex`, `encoding/json` (validation and
reformatting only), `html`, `iter`, `maps`, `math/bits`, `net/url`, `path`,
`path/filepath`, `regexp`, `slices`, `sort`, `time`, `unicode`, `unicode/utf8`,
and `unicode/utf16`. Pass `evmstd.Packages()` alongside your host packages to
`Compile` and `Link` (GoX includes them automatically).

- Nothing in `evmstd` reads files, the network, or the environment—functions
  like `time.Sleep` and `filepath.Glob` are left out.
- `time.Now` is exposed and carries the host's time zone; `time.Local` and
  `time.LoadLocation` are omitted.
- [`evmstd/API.md`](../evmstd/API.md) lists every exposed declaration per
  package and explains why each omitted one was excluded. Unlisted symbols fail
  type checking.
- Most `evmstd` packages call the real Go standard library outside the VM's
  instruction and allocation budgets, charging only their results. `cmp`,
  `iter`, `maps`, `slices`, and `sort` compile from script source and are
  metered like ordinary script code.
- Linking `evmstd` makes `[]string`, `[]byte`, `[]int`, and other slices used
  by those packages native Go slices in the program, so you can pass them
  directly through `RunEntryNative` and `RenderNative`.
- Pass `evmstd.Imports()` to `evmbind.Options.Imports` so your host APIs share
  the exact same `time.Duration`, `*url.URL`, and other standard types.

`VM.Output` (`func(string) error`) receives output from `print`, `fmt.Print`,
and `log`; when `nil`, output goes to Go's default logger. Returning an error
from `VM.Output` aborts execution. `Limits.OutputBytes` defaults to 1 MiB and
caps both any single formatted value and total output per invocation (including
initialization). Standard-library callbacks share the VM's execution limits.

## Records in scripts

Build a model package once during setup:

```go
models, err := evm.ModelPackage("m", schema)
```

By default, a model's script type uses its exported schema name, and its
editable counterpart appends `Edit` (for example, `m.Customer` and
`m.CustomerEdit`). You can override the base type name in a model declaration
with `ModelBuilder.SetRowName("TenantSettingsRow")` without changing the stored
model identity or property codes. Duplicate or invalid names fail package
creation.

Field selectors come directly from the model, including both local and
qualified aliases. Registering models never grants database access on its own:
the host still supplies lookup, scan, and save operations.

### Reading records

A cursor or lookup wrapper returns a read-only record with
`call.SetRecordResult(model, cursor.Record())`. Immutable records travel as
16-byte pairs—even through `any`—and read fields through shared representation
metadata, with no read-record arena or expiry checks. Generated and reflected
native records expose the same script API. To expose Go helpers like
`customer.PointBalance()`, see
[explicit record methods](../evmbind/README.md#explicit-record-methods).

### Editing records

Mutable editors require a record scope opened with `evm.WithRecords(ctx)` for
the duration of the transaction:

1. Register an editor for the script with `scope.EditRecord`.
2. Recover and validate it in your save wrapper with `scope.MutableRecord`.
3. Flush any pending materialized collections before saving using
   `call.FlushRecord(editor)` or `call.FlushMutableRecord(value, model)`.

Saving and committing remain ordinary host operations.

### JSON conversion

`record.ToJSON()` returns detached, JSON-compatible data by traversing the
record directly—no serialize-and-reparse round trip. It preserves 64-bit
integer precision and invokes custom JSON or text marshalers at scalar leaves.

On the host side, `Program.JSONValue` and `Call.JSONValue` convert a VM `any`
into an owned Go tree whose numbers are `json.Number` (the `Call` form charges
the allocation budget). Their byte limit counts the equivalent escaped JSON
output. `EncodeJSON` writes JSON bytes directly.

## Borrowing effects

The compiler inserts ownership copies whenever a borrowed value escapes its
scope. You can give ownership analysis exact information about your host APIs
by attaching `*evm.OwnershipEffects` to `ExternalFunction.Ownership` or
`ExternalMethod.Ownership`:

```go
readEffects := &evm.OwnershipEffects{
    NoRetain: true, NoInvalidate: true,
    Borrowed: []evm.BorrowSource{{Result: 0, Domain: "db.tx", ScopeParameter: 1}},
}
endEffects := &evm.OwnershipEffects{
    NoRetain: true,
    Ends: []evm.ScopeEnd{{Domain: "db.tx", ScopeParameter: 1}},
}
writeEffects := &evm.OwnershipEffects{
    NoRetain: true,
    Callbacks: []evm.ScopedCallback{{Argument: 1, Domain: "db.tx"}},
}
```

How indices and promises work:

- `Result` and callback `Argument` indices are zero-based.
- `ScopeParameter` is one-based (counting the receiver as 1), with `0` meaning
  the enclosing scope.
- Promise `NoInvalidate` only when the call cannot close or replace existing
  borrowed storage. A cursor that advances to a different transaction must not
  promise it.
- Promise `NoRetain` only when the call never keeps arguments past its
  synchronous return.
- Effects are part of the compiled binding contract: `Link` rejects mismatches.

Additional ownership rules:

- Register custom borrowable host types with `values.NewOwnedType` and a
  budgeted ownership callback. Immutable KVO record types provide this
  automatically.
- Calls without effect declarations are treated conservatively: they are
  assumed to retain arguments and invalidate live borrows. Simple read-only
  guest helpers are analyzed automatically.
- Scope-local flat arrays, slices, and maps stay borrowed when all aliases stay
  local and nothing invalidates the scope. Other containers own their borrowed
  leaves on insertion, and opaque borrowed types copy on acquisition.
- Inspect `Function.OwnershipSites` to see every ownership copy the compiler
  inserted. Scope matching treats potentially aliased instances of a domain
  conservatively, and nested callbacks stay safe through leaf copying.
- Read-only KVO handles represent snapshots, not stable database identity.
  Owning a snapshot can change its representation or address, so use an
  explicit primary key for identity or map keys that must cross an ownership
  boundary.
- Explicit struct dereferences (`copy := *row`) allocate VM struct storage and
  own their payload, while ordinary handle assignments and interface packing do
  not box. Returned strings are conservatively copied even when their source
  record was already owned, and materialized collection fields own their
  contents.

Raw Go access and handwritten bytecode do not go through compiler ownership
analysis. Call `kvo.Record.Own()` before retaining encoded bytes past a
transaction, and make sure host effect declarations accurately describe actual
invalidation—no runtime lease or per-record transaction bookkeeping can repair
an inaccurate declaration.

## Shared scalar source

A package can supply Go `Source` alongside—or instead of—native declarations.

### Bodyless declarations and generics

A source package can declare bodyless functions and bind them to matching
`ExternalFunction` entries:

- **Generic functions** use `ExternalFunction.Instantiate`, which receives the
  concrete VM type arguments and returns a concrete signature and native call.
  The compiler validates the returned signature against the instantiated Go
  declaration, and the linker binds and re-checks it. The factory must be
  deterministic, reject unsupported type arguments, and not return another
  factory.
- **Fixed-receiver generic methods** use `ExternalMethod.Instantiate` and
  return a receiver-free signature (the native call still receives the receiver
  in argument zero). Type inference, explicit type arguments, function and
  method values, and method expressions all use the same specialized bindings.
- **Generic structs** map a source-declared type to a native shape via
  `TypeDecl.SourceName` and `TypeArgs` (using existing VM type identities)—for
  example, `SourceName: "Index", TypeArgs: []*values.Type{values.StringType}`
  maps `Index[string]`. Register its pointer shape separately as an unnamed
  type. The compiler checks source fields against the supplied shape, and
  native-backed fields and guest allocations use that native layout.
- **Methods on generic receivers** need a bodyless source declaration (such as
  `func (i *Index[K]) Read() K`) and one `ExternalMethod` per concrete
  receiver. The source signature drives type substitution, and bindings are
  keyed by receiver type. They support direct calls, method values, method
  expressions, and interface dispatch, without `Instantiate`.

### Mixing source and native declarations

Source declarations can share a package with native types, records, values,
functions, and methods, provided the mixed package contains declarations only
(put function bodies in a separate source package). Declare method receivers in
source and map their native shapes with `SourceName`.

Native declarations are seeded before source type checking. A native struct
(`TypeDecl.Name`) may contain or embed a registered source generic instance—even
one parameterized by that same struct. The compiler fills in those native
fields after binding the source instances and then checks constraints.
Registration order does not affect type identity.

Because those deferred native structs have no fields while the package's own
source is being checked, source declarations in that package cannot reference
their fields in constant expressions, use them as map keys, or pass them to
layout-dependent constraints such as `comparable`. If you need that, declare
the type in source and register its native layout with `SourceName` instead.
Consumer scripts see the completed native structs with all fields and
constraints intact.

### Sharing a scalar type with its methods

A package can provide `Source` whose functions and methods compile directly as
guest Go code, while `SharedTypes` preserves the native scalar's type identity
across host signatures and KVO fields:

```go
idType := values.NewType("ids.ID", values.Raw)
ids := evm.Package{
    Path:   "ids",
    Source: idsSource, // the same Go scalar/method source used by the host
    SharedTypes: []evm.SharedType{{
        Name: "ID", Native: reflect.TypeFor[ID](), Type: idType,
    }},
}
```

Register `ids` alongside the model package for compilation and linking. Any KVO
word whose native type is `ID` then appears as `ids.ID` (including inside
collections), and its methods work even when a script only imports the model
package. Shared source must use supported EVM imports and cannot declare
package variables or `init` functions, and the shared type's scalar kind must
match its native declaration. Keep host-specific integrations in separate files
and embed only the shared source.

## Execution coverage

To collect execution coverage, create a collector with
`coverage := evm.NewCoverage(program)` and attach it via
`vm.SetCoverage(coverage)` before running (`vm.SetCoverage(nil)` turns it off).
Guest calls and synchronous native-to-guest callbacks share the active
collector. Collectors belong to one linked program and are not concurrent—use
one collector per concurrent run, then combine them with `coverage.Merge` once
the runs finish.

- `coverage.Instructions()` returns execution counts, conditional jump
  outcomes, and source positions for every instruction, including ones that
  never ran.
- `coverage.Lines()` groups those results by source file and line. A line is
  complete only when every instruction on it ran and every conditional jump
  took both branches. Positions without a line number are omitted.
- Coverage is a bytecode-level diagnostic: host code and instructions removed
  by the compiler are not measured.

Some compiler optimizations reshape branches without changing what `Lines()`
reports:

- Loop fusion can split a source condition into an entry test and a back edge.
  `Lines()` merges their normalized outcomes, while raw instruction counts keep
  both machine branches.
- Proven-constant entry guards on unrolled loops carry no source coverage
  obligation, though raw counters still record them.
- Boolean conditions map back to their use sites, including repeated tests of
  the same parameter.

Coverage selects a dedicated generated interpreter at invocation entry, so the
ordinary interpreter pays zero per-instruction coverage overhead. Fused
operations stay fused (and fused comparisons record the consumed jump's
outcome), while float-loop acceleration is bypassed so every iteration is
observed. If you change the interpreter, regenerate coverage dispatch with
`go generate .`; a test rejects stale generated code.
