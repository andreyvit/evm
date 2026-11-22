# Embedding EVM: host contracts

The [README](../README.md) gives an overview of what EVM can do. This guide
covers the contracts a host application relies on when embedding the VM. For
bindings generated from Go source, see [`evmbind`](../evmbind/README.md); for
hand-written declarations and Go-backed storage, see
[binding declarations](bindings.md).

## Programs, VM leases, and environments

A linked `*evm.Program` is immutable, holds its default execution limits, and
is safe to share across goroutines. A `*evm.VM` is a single-goroutine execution
lease over that program, borrowed from a shared pool:

```go
vm := program.AcquireVM(ctx)
defer vm.Release()
```

- `AcquireVM(ctx)` attaches the program and Go `ctx` and initializes a fresh
  budget from the program's default limits. It runs no guest code.
- `vm.Run(fn, slots)` is the single execution entry on an acquired VM. Each
  outermost `vm.Run` (whether a guest function or a native root) uses the VM's
  own registers directly, allocates fresh package globals and runs package
  `init()` before entering `fn`, then clears globals, active scopes, and
  register buffers on return—whether it succeeded or failed.
- Synchronous re-entry from an active host binding (including a native root
  that calls `vm.Run`) runs on the same `*evm.VM` and goroutine, sharing the
  active globals, remaining budget, cancellation, and record scopes while
  swapping in separate stable register/frame storage for the nested call.
- `Release()` requires an idle VM; only the caller that acquired the VM
  releases it. Release drops references to the program, context, globals,
  records, callbacks, deadlines, and meters, and returns only cleared reusable
  buffers to the pool.
- Entry results are written only on success; failure leaves the caller's result
  slots untouched. The VM never rolls back host side effects.

Compile and link programs during setup, and build schemas, models, codecs, and
resolved callables there too—keep all of that out of your request loop.
Pooled VMs and stack-allocated `[N]values.Value` slot arrays avoid per-call
allocations once buffers have grown.

### Compiling and linking many scripts

If your host compiles and links many scripts against the same package set,
prepare the packages once:

1. Build an `evmcompiler.NewEnvironment(declared...)` for compiling. Each
   subsequent compile type-checks only the script itself.
2. Build a matching link environment with `env.NewLinkEnvironment(bound...)` or
   `evm.LinkOptions{DefaultLimits: &limits, Shapes: env.Shapes(), Methods: env.MethodTables()}.NewEnvironment(bound...)`.
   Each link copies only the tables that belong to its module and inherits the
   environment's default limits.

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

### Resolving functions and passing values

Resolve required entry functions once after linking with
`program.LookupFunction(name, want)`:

- A non-nil `*evm.Signature` checks every parameter and result in order by
  exact EVM type identity (`*values.Type` pointer equality) and reports the
  symbol, actual signature, and expected signature on mismatch.
- A `nil` signature accepts any signature. An empty `&evm.Signature{}` requires
  exactly `func()`.
- Every host-callable entry can be looked up, even one the script never takes
  as a function value.
- Validate a retained callable (for example, one registered by a script) with
  `fn.CheckSignature(want)`.

Pass arguments and receive results in one caller-owned `[]values.Value` slice:
all arguments first, followed by one slot per result (pass `nil` for `func()`).
Use a stack-allocated array sliced as `slots[:]` to avoid heap allocations.
Each slot holds a 16-byte untyped `values.Value`, and the checked signature
supplies the type of every slot. Because identical bits can represent different
guest types and `vm.Run` trusts caller-supplied values, callers are responsible
for packing each argument slot according to its declared parameter type (using
the matching `values.*` constructor, `evmbind.Codec`, `schema.PackInterface`,
or `vm.PackAggregate` / `vm.PackMap`) and unpacking each result slot
accordingly.

#### Live pointers and typed `nil`

Use `evmbind.Codec[T](schema)` once during setup to resolve a bound Go type:

```go
customerCodec := must(evmbind.Codec[*Customer](shopSchema))
orderCodec := must(evmbind.Codec[*Order](shopSchema))
onOrder := must(program.LookupFunction("OnOrder", &evm.Signature{
    Params:  []*values.Type{customerCodec.Type(), orderCodec.Type(), values.TimeType},
    Results: []*values.Type{values.StringType},
}))

vm := program.AcquireVM(ctx)
defer vm.Release()

// Live pointers keep their Go addresses. A typed nil (*Order)(nil) packs as
// the zero values.Value{} and unpacks back to (*Order)(nil).
slots := [4]values.Value{
    customerCodec.Pack(ada),
    orderCodec.Pack((*Order)(nil)),
    values.NativeTime(now),
}
if err := vm.Run(onOrder, slots[:]); err != nil {
    return "", err
}
return slots[3].AsString(), nil
```

For ordinary bound native struct pointers, `Pack` and `Unpack` preserve pointer
identity without copying or reflection. On a non-pointer codec, `Pack` panics
and `Unpack` returns an error—use `PackIn(vm, x)` and `UnpackIn(vm, v)` instead
so conversions that allocate or cross length limits are metered against the
active VM's budget.

#### Interfaces and typed `nil` interfaces

Use `schema.PackInterface(program, x)` and `schema.UnpackInterface(program, v)`
to pass registered native pointer implementations across the Go boundary into an
EVM interface parameter (such as `any` or a declared host interface):

```go
// Pass a live implementation or a typed nil (*MyContext)(nil).
// Typed nils keep their concrete program type ID in R with P == nil;
// only an untyped Go nil packs as the zero interface (R == 0, P == nil).
arg, err := shopSchema.PackInterface(program, (*MyContext)(nil))
if err != nil {
    return err
}
slots := [2]values.Value{arg}
if err := vm.Run(entry, slots[:]); err != nil {
    return err
}
got, err := shopSchema.UnpackInterface(program, slots[1])
```

`program.PackInterface` and `program.UnpackInterface` operate on values that
are already packed as `values.Value`. Interface type IDs are program-local;
repack when crossing to another `*evm.Program`.

Schemas follow their declared types and static-only policy: you cannot register
arbitrary Go types at run time to pass them through `any`. Unregistered concrete
types return an error from `PackInterface` or `PackIn`.

#### Nested collections and guest storage

For native-backed slices, maps, and structs bound by a schema, resolve a typed
codec and use its metered `PackIn` / `UnpackIn` methods on an acquired VM:

```go
tagsCodec := must(evmbind.Codec[map[string][]string](shopSchema))
tagsVal, err := tagsCodec.PackIn(vm, map[string][]string{
    "ada": {"vip", "early"},
})
if err != nil {
    return err
}
slots := [2]values.Value{tagsVal}
if err := vm.Run(summarize, slots[:]); err != nil {
    return err
}
out, err := tagsCodec.UnpackIn(vm, slots[1])
```

For guest-only structs, arrays, slices, and maps (types that live in EVM
storage cells rather than native Go layouts), obtain the linked guest types from
a resolved function signature (`fn.Signature()` and `program.Shape`), build
values with `vm.PackAggregate` and `vm.PackMap`, and read guest results back
with `vm.UnpackAggregate` and `vm.UnpackMap`:

```go
// Guest script:
//   type Row struct { Tags map[string][]string; Counts [2]int }
//   func Tags(r Row) Row { ... }
tagsFn := must(program.LookupFunction("Tags", nil))
sig := tagsFn.Signature()
// Obtain linked guest shapes from the resolved signature.
rowShape := program.Shape(sig.Params[0])
tagsShape := rowShape.Fields[0].Shape   // map[string][]string
countsShape := rowShape.Fields[1].Shape // [2]int

// Each member or key/value must match its declared field, element, or key type.
items, err := vm.PackAggregate(tagsShape.Elem.Type, values.String("vip"), values.String("early"))
if err != nil {
    return err
}
byUser, err := vm.PackMap(tagsShape.Type, values.String("ada"), items)
if err != nil {
    return err
}
counts, err := vm.PackAggregate(countsShape.Type, values.Int(0), values.Int(1))
if err != nil {
    return err
}
rowVal, err := vm.PackAggregate(rowShape.Type, byUser, counts)
if err != nil {
    return err
}

slots := [2]values.Value{rowVal}
if err := vm.Run(tagsFn, slots[:]); err != nil {
    return err
}

// Unpack the returned guest Row back into fields, map entries, and slice elements.
fields, err := vm.UnpackAggregate(sig.Results[0], slots[1])
if err != nil {
    return err
}
entries, err := vm.UnpackMap(tagsShape.Type, fields[0])
if err != nil {
    return err
}
labels, err := vm.UnpackAggregate(tagsShape.Elem.Type, entries[1])
```

`PackAggregate` takes struct fields in declaration order or array/slice elements
in index order; `PackMap` takes alternating key/value pairs. Both validate
shapes, own borrowed leaves, charge allocations to the VM's budget, and reject
native-backed shapes so host layouts stay on their bindings; callers are still
responsible for supplying each field, element, key, and value in its declared
EVM representation. `vm.UnpackAggregate(t, v)` returns fields or elements in the
same order, and `vm.UnpackMap(t, v)` returns alternating keys and values, with
nested values keeping their EVM representation and the returned slice charged to
the active budget.

#### Borrowed records

Pass a read-only KVO record with `record.Value()` (no copy or decode), and pass
an editable record through an `evm.WithRecords(ctx)` scope with
`scope.EditRecord(model, edit)`:

```go
ctx, scope := evm.WithRecords(ctx)
defer scope.Close()
vm := program.AcquireVM(ctx)
defer vm.Release()

// Read-only borrowed record (valid while its DB transaction stays open).
readSlots := [3]values.Value{record.Value(), values.NativeTime(now)}
if err := vm.Run(describe, readSlots[:]); err != nil {
    return err
}

// Editable record inside the transaction's record scope.
edit := record.Edit()
editSlots := [3]values.Value{scope.EditRecord(CustomerModel, edit), values.Int(250)}
if err := vm.Run(redeem, editSlots[:]); err != nil {
    return err
}
```

See [Records in scripts](#records-in-scripts) below for flushing edits and
ownership rules.

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

A host binding (`func(*evm.VM) error`) borrows the executing VM and its current
register window for the duration of that call:

- Read arguments with `vm.CallArgument(i)` and write results with
  `vm.SetCallResult(i, v)`. Read all arguments before overwriting overlapping
  result slots.
- Never retain the borrowed `*evm.VM` or its `R`/`P` slices after returning,
  and never call `vm.Release()` from inside a binding (only the owner that
  called `AcquireVM` releases it).
- Honor `vm.Context` cancellation inside your host callback.

### Synchronous nested calls and interface dispatch

Inside a host binding, call a guest callback synchronously on the same borrowed
`*evm.VM` and goroutine with `vm.Run(cb, slots)`:

```go
// Inside a host binding, e.g. a database scan calling yield(record) bool:
yield, err := vm.Program().Callable(vm.CallArgument(0))
if err != nil {
    return err
}
if err := yield.CheckSignature(yieldSig); err != nil {
    return err
}
for cursor.Next() {
    slots := [2]values.Value{cursor.Record().Value()}
    if err := vm.Run(yield, slots[:]); err != nil {
        return vm.CallbackError(err)
    }
    if !slots[1].AsBool() {
        break
    }
}
```

`vm.Run` always runs on the same `*evm.VM`:

- **Shared state and limits:** Nested calls share the active invocation's
  package globals, remaining budget, cancellation context, coverage collector,
  and active record scopes. Combined call depth and register counts charge the
  shared limits.
- **Stable register windows only on re-entry:** An outermost `vm.Run`—whether
  its target is a guest function or a native root—uses the leased VM's own
  registers directly. Only re-entry from an active native call (including a
  native root that calls `vm.Run`) saves the outer host function, source
  position, and `vm.R`/`vm.P` window slices and swaps in a separate pooled
  register/frame buffer segment. Even if the nested callback grows its own
  register banks, any `R`/`P` window already lent to the outer Go binding stays
  valid. Outer state is restored on every exit, including errors, panics, and
  budget exhaustion.
- **Program check:** `vm.Run` rejects any callable whose target belongs to a
  different `*evm.Program`.

For dynamic interface dispatch on a guest receiver, resolve the method to a
bound `evm.Callable` with `vm.LookupMethod` and invoke it with `vm.Run`:

```go
method, err := vm.LookupMethod(ifaceVal, "DBTx", dbTxSig)
if err != nil {
    return err
}
var slots [1]values.Value
if err := vm.Run(method, slots[:]); err != nil {
    return err
}
```

`LookupMethod` validates the method name and signature against the concrete
receiver type and binds that receiver into the returned `Callable`.

### Native function values

To expose a Go closure at run time—for example, a returned iterator, a yield
callback, or bound host state—cache `NewNativeFunctionType(signature)` during
setup, then wrap the closure with `vm.NativeFunction(typ, closure)`.

Guest calls, deferred calls, and nested `vm.Run` calls all accept these values
and share the current invocation's limits. Keep these rules in mind:

- A native function value belongs to the program that created it; another
  program cannot call it.
- Its captured host state can outlive the call that created it, so the host
  must enforce any borrowed transaction lifetime itself.
- Never capture the borrowed `*evm.VM` or its `R`/`P` windows.
- A `nil` closure produces a `nil` function value.
- Allocating the function holder is metered against the VM budget, while the
  closure body and its own allocations remain the host's responsibility.

When passing a host callback *into* an initial `vm.Run` call (such as a
streaming row writer), use `program.NativeFunction(typ, closure)` instead. The
host owns that holder allocation, while execution still shares the VM's limits
and propagates callback errors. Prefer `vm.NativeFunction` during execution so
holder allocations stay metered.

### Retained callables and delayed execution

A retained `evm.Callable` is a two-word value holding a shared linked target
pointer (with program, function index, and signature) plus an optional capture
or bound-receiver pointer. Retain a guest function argument with
`program.Callable(value)` and check its contract at registration with
`fn.CheckSignature(want)`.

`Callable` covers plain guest functions, closures, bound methods, static native
functions, and runtime native closures:

- **Retained captures and program lifetime:** Copies share the same captures
  and bound receiver without copying them, and keeping a `Callable` keeps its
  owning `*evm.Program` alive (so a hot-reloaded version remains valid for
  in-flight retained views). Calls on a VM from a different program fail with
  `evm.ErrFunction`.
- **Delayed execution (including GoX views):** To run a retained callable after
  the registering call has returned, acquire a fresh VM lease from
  `fn.Program().AcquireVM(ctx)` (with `defer vm.Release()`) and call
  `vm.Run(fn, slots)`—or for a GoX renderer, call `goxProgram.RenderFunction`.
  Each delayed run gets fresh package globals and its own budget while still
  seeing its original receiver and captures.
- **No blanket race-safety guarantee:** EVM does not synchronize concurrent
  guest access to shared mutable captures or shared native Go pointers. If the
  same closure or bound receiver can run concurrently on separate VMs, the host
  must synchronize access or keep captures read-only.
- **Signature inspection:** `fn.Signature()` returns a defensive copy.

### Native field codecs

`TypeDecl.NativeCodec` provides contextual conversion for native struct fields
whose Go representation differs from their VM type. Its `Decode` and `Encode`
callbacks receive the active `*evm.VM` for program checks and
allocation accounting; they must leave its argument and result window untouched
and must not retain the VM.

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

Default limits live on the linked `*evm.Program`, and the remaining allowance
lives on each acquired `*evm.VM`:

- **Link-time defaults:** Pass `LinkOptions{DefaultLimits: &limits}` to `Link`
  or `NewEnvironment` (linking copies the value before publishing the program).
  Omitting `DefaultLimits` (`nil`) selects EVM's finite defaults: 1,000,000
  steps, 1,000,000 allocations, 64 MiB allocation bytes, and 1 MiB output bytes.
  Passing an explicit empty `&evm.Limits{}` disables those quotas while keeping
  structural bounds (`Depth` 1,024, `RSlots` 1M, `PSlots` 1M) and context
  cancellation.
- **VM lease and overrides:** `AcquireVM(ctx)` starts a fresh allowance and
  deadline from the program's default limits. Successive `vm.Run` calls and
  synchronous nested callbacks on that VM spend the same allowance; parallel
  VMs spend independently. Call `vm.SetBudget(limits)` while the VM is idle to
  replace the remaining counters and start a fresh deadline (for a batch, or
  between outermost runs to give each its own allowance). Passing `evm.Limits{}`
  to `SetBudget` disables quotas while keeping structural bounds and
  cancellation; `UnlimitedSteps: true` lifts only the step cap.
- **Time:** `Limits.Duration` / `HardDuration` and `ctx` bound elapsed time
  cooperatively from acquisition (or from `SetBudget`); native host calls must
  check `vm.Context` and return on their own.
- **VM meters:** Instruction, call-depth, register, allocation, and output
  limits apply to VM execution, including package `init()`. Allocation
  accounting is cumulative over the budget's lifetime and includes transient
  capacity—it is not live heap size. Work is charged on every exit including
  failure, and an exhausted meter stays exhausted until `vm.SetBudget` or a
  fresh `AcquireVM`.
- **Host work:** Native APIs run outside the VM's meters; they are responsible
  for their own CPU work and allocations unless they explicitly charge them
  with `vm.ChargeAllocation(bytes)`.

Script panics and language errors can be caught by a direct deferred `recover()`
inside the script. Uncaught errors retain source positions and unwrap to their
underlying cause. Budget exhaustion, context cancellation, invalid-value
errors, and native callback errors are fatal and skip script defers. Native Go
panics inside hand-written callbacks propagate after VM cleanup. Every outermost
`vm.Run` clears globals, active scopes, and execution buffers on exit and
preserves caller result slots on failure; in all cases, the host owns
transaction rollback and resource cleanup.

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
  instruction and allocation budgets, charging only their results.
  `bytes.Replace`, `bytes.ReplaceAll`, `bytes.Join`,
  and `bytes.Repeat` charge their output size against the VM allocation budget
  before allocating (`bytes.Repeat` also keeps its 16 MiB output cap). The six
  `regexp.Regexp` `ReplaceAll` methods—literal (`ReplaceAllLiteral`,
  `ReplaceAllLiteralString`), capture expansion (`ReplaceAll`,
  `ReplaceAllString`), and callback (`ReplaceAllFunc`,
  `ReplaceAllStringFunc`)—charge their output against the VM allocation budget.
  `cmp`, `iter`, `maps`, `slices`, and `sort` compile from script source and
  are metered like ordinary script code.
- Linking `evmstd` makes `[]string`, `[]byte`, `[]int`, and other slices used
  by those packages native Go slices in the program, so host schemas and
  `evmbind.Codec` / `RenderNative` share their backing storage directly.
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
`vm.SetRecordResult(model, cursor.Record())`. Immutable records travel as
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
   `vm.FlushRecord(editor)` or `vm.FlushMutableRecord(value, model)`.

Saving and committing remain ordinary host operations.

### JSON conversion

`record.ToJSON()` returns detached, JSON-compatible data by traversing the
record directly—no serialize-and-reparse round trip. It preserves 64-bit
integer precision and invokes custom JSON or text marshalers at scalar leaves.

On the host side, `Program.JSONValue` and `VM.JSONValue` convert a VM `any`
into an owned Go tree whose numbers are `json.Number` (the `VM` form charges
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
