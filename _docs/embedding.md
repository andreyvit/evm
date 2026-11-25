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

Use `evmbind.Codec[T](schema)` once during setup to resolve a bound Go type.
It panics if the conversion is unavailable or ambiguous. Use
`evmbind.TryCodec[T](schema)` to handle the setup error.

```go
customerCodec := evmbind.Codec[*Customer](shopSchema)
orderCodec := evmbind.Codec[*Order](shopSchema)
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
types return an error from `PackInterface` or `PackIn`. For the reverse
direction—calling methods on a script-defined type from Go—see
[Scripts implementing Go host interfaces](#scripts-implementing-go-host-interfaces).

#### Nested collections and guest storage

For native-backed slices, maps, and structs bound by a schema, resolve a typed
codec and use its metered `PackIn` / `UnpackIn` methods on an acquired VM:

```go
tagsCodec := evmbind.Codec[map[string][]string](shopSchema)
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
  guest access to shared mutable captures or shared native Go pointers.
  Callbacks registered through plain `*evm.FunctionOf[S]` are not checked by the
  compiler. If the same closure or bound receiver can run concurrently on
  separate VMs, declare the registration parameter as `*evm.ConcurrentFunctionOf[S]`
  (and use `concurrency.Cache` for shared mutable state; see
  [Concurrent callbacks and caches](#concurrent-callbacks-and-caches)), keep captures
  read-only, or synchronize calls on the host side.
- **Signature inspection:** `fn.Signature()` returns a defensive copy.

### Concurrent callbacks and caches

When a host registers callbacks that may run concurrently across pooled VMs,
split the contract into two parts:

1. **Host registration parameter (`*evm.ConcurrentFunctionOf[S]`):** Declare the
   registration parameter as `*evm.ConcurrentFunctionOf[S]` (`S` = non-variadic
   function signature or `any`, type-aliased to `evm.Callable`), instead of
   `*evm.FunctionOf[S]`. The compiler verifies callbacks passed at statically
   visible calls, and `evmbind` enforces `fn.CheckConcurrentUse()` at the native
   boundary.
2. **Synchronized shared cache (`evmruntime/concurrency`):** `evmstd.Packages()`
   (or `evmstd.ConcurrencyPackages()` alone) exposes `concurrency.Cache[K, V]`,
   backed by native Go package
   `github.com/andreyvit/evm/evmruntime/concurrency`.

#### `FunctionOf[S]` vs. `ConcurrentFunctionOf[S]`

Both types alias `evm.Callable` so Go receives `*evm.Callable`, and both accept
a concrete non-variadic signature or `any` for host-checked registrations:

- **`*evm.FunctionOf[S]`** marks an ordinary retained callable (for example, a
  single-request view callback or a callback the host serializes). It also works
  in struct fields and return values. The compiler does not restrict its
  captures, and `evmbind` does not call `CheckConcurrentUse()`.
- **`*evm.ConcurrentFunctionOf[S]`** marks a registration parameter for a callback
  that may run concurrently across VMs—for example:

```go
type InitContext interface {
    AddBlock(renderer *evm.ConcurrentFunctionOf[any])
    AddAdminHandler(path, method string, handler *evm.ConcurrentFunctionOf[func(HandlerContext) any], options *AdminHandler)
}
```

Plugins pass the handler or bound method directly (`c.Handle`) without any
wrapper call, and only import `"evmruntime/concurrency"` if they use
`concurrency.Cache`:

```go
// Allowed plugin: immutable config field + synchronized concurrency.Cache.
import (
    "bh"
    "bh/security"
    "evmruntime/concurrency"
    "time"
)

type Client struct {
    prefix string
    tokens *concurrency.Cache[string, string]
}

func (c *Client) Handle(h bh.HandlerContext) any {
    entry, err := c.tokens.GetOrLoad("token", func() (string, time.Time, error) {
        var expiry time.Time
        return c.prefix + h.Method(), expiry, nil
    })
    if err != nil {
        panic(err)
    }
    return entry.Value
}

func Initialize(ctx bh.InitContext) {
    c := &Client{prefix: "first-", tokens: concurrency.NewCache[string, string]()}
    ctx.AddAdminHandler("token", "GET", c.Handle, &bh.AdminHandler{
        Permission: security.PermissionAccessSuperadmin,
    })
}
```

The host answers for native code, so a callback can use native calls freely
with shared state, and can return shared values to the host:

```go
// Allowed plugin: native handle captured at setup, shared value returned.
func Initialize(ctx bh.InitContext) {
    frame := ctx.AddStaticFrame(&bh.StaticFrame{HTMLFile: "library.html"})
    raw := &model.RawOutput{Data: []byte("ok"), ContentType: "text/plain"}
    ctx.AddAdminTab(func(page bh.AdminPageContext) any {
        return &model.ViewData{ViewFunc: frame.Render()} // native method on a capture
    }, &bh.AdminTab{...})
    ctx.AddAdminHandler("raw", "GET", func(h bh.HandlerContext) any {
        return raw // the host receives the shared value and must not change it
    }, &bh.AdminHandler{...})
}
```

```go
// Forbidden plugin patterns:
// 1. Mutating receiver/captured state (compile error):
func (c *Client) BadWrite(h bh.HandlerContext) any {
    c.prefix = h.Method() // fails: cannot prove read-only use of receiver or capture
    return c.prefix
}

// 2. Handing native code a pointer to a plain shared field (compile error):
type Counter struct{ n int }
func (c *Counter) BadFill(h bh.HandlerContext) any {
    forms.Var(&c.n) // fails: native code takes such a pointer to write through it
    return c.n
}

// 2a. Using the registration context inside the callback (compile error):
func BadLateUse(ctx bh.InitContext) {
    ctx.AddAdminHandler("late", "GET", func(h bh.HandlerContext) any {
        ctx.AddLog("late") // fails: uses the bh.InitContext it is registered on
        return nil
    }, &bh.AdminHandler{...})
}

// 3. Passing a dynamic callback value (compile error):
func RegisterDynamic(ctx bh.InitContext, fn func(bh.HandlerContext) any) {
    ctx.AddAdminHandler("dyn", "GET", fn, &bh.AdminHandler{...}) // fails at compile time
}

// 4. Indirect registration call (bypasses compile check; fails at native boundary):
func BadIndirect(ctx bh.InitContext, c *Client) {
    add := ctx.AddAdminHandler
    add("tok", "GET", c.Handle, &bh.AdminHandler{...}) // fails at run time via CheckConcurrentUse()
}
```

#### Compiler verification and native-boundary enforcement

When `evmbind` sees a `*evm.ConcurrentFunctionOf[S]` parameter, it records its
zero-based explicit parameter index (excluding any receiver) in
`Package.ConcurrentCallbackParams[key]` (where `key` is `"FuncName"` or `"Receiver.Method"`).
Hand-written bindings can populate this metadata directly. Before retaining a
capturing callback, check both `fn.CheckConcurrentUse()` for the body and
`vm.CheckConcurrentBorrow(i)` for the registration site, with the zero-based
parameter index excluding the receiver. `evmbind` performs both checks.

- **Compile-time check (statically visible calls):** Right after SSA discovery
  and before optimization, the compiler scans all statically visible calls
  (direct function calls, concrete method calls, and interface method calls) to
  functions/methods listed in `Package.ConcurrentCallbackParams`. At each concurrent callback parameter
  position it accepts `nil`, plain top-level functions (no captures), or
  statically known closures/bound methods (tracing through locals, `Phi` nodes,
  interface boxing, and type conversions). Passing a dynamic function parameter,
  field, or unknown expression fails compilation with
  `concurrent callback parameter requires a statically known function, closure, or bound method`.
- **Tracked state:** Starting from the closure's free variables and bound
  receiver, the checker tracks values whose types can carry writable aliases:
  pointers, slices, maps, interfaces, functions, `unsafe.Pointer`, structs or
  arrays containing them, and named structs imported from other packages (whose
  native layouts may hide unexported fields). Pure guest scalars and guest
  structs/arrays of scalars copy by value and are not tracked.
  Copying slice elements that contain pointers can leave both slices referring
  to the same object. The checker follows those references, closure captures,
  and the storage returned by guest setup helpers. A helper can still return a
  fresh object for the caller to initialize before registration.
- **Allowed uses:** Reading fields, elements, dereferences, slices, maps, and
  ranges; comparisons (`==`, `!=`, etc.); `len` and `cap`; passing tracked
  values to statically resolved guest functions (checked transitively through
  their parameters); calling `*concurrency.Cache` receiver methods (`Get`,
  `Set`, `GetOrLoad`, `InvalidateIfCurrent`); and capturing tracked values in
  nested closures only when every use calls that closure synchronously (direct
  call or loader argument to `Cache.GetOrLoad`).
- **Rejected uses:** Writing to captured/receiver fields, pointers, slices, or
  maps (`append`, `copy`, `delete`, `clear`, stores); returning or storing an
  alias to tracked state; capturing tracked state in an escaping closure;
  calling a captured function or making an indirect call on tracked state; or
  passing tracked state to an external/native function (other than
  `*concurrency.Cache` receiver methods).
- **Native-boundary enforcement (`evmbind` and `fn.CheckConcurrentUse()`):** Verified
  closures and bound methods receive `evm.Function.CapturesVerifiedForConcurrency = true`. For
  every `*evm.ConcurrentFunctionOf[S]` parameter, `evmbind` automatically checks
  the incoming value at the native boundary before calling the host function or
  method: `nil` passes through as `nil` (for optional registrations), and any
  non-nil value must pass `fn.CheckConcurrentUse()`. `fn.CheckConcurrentUse()` accepts plain
  non-capturing guest functions and compiler-verified closures/bound methods,
  and rejects unverified capturing callbacks, native callables, and `nil`
  (`evm.ErrNilFunction` / `evm.ErrFunction`). Because `CapturesVerifiedForConcurrency` is
  compiler metadata, hosts must compile Go source rather than trusting raw
  uploaded bytecode.
- **Limitations (aliases and indirect calls):**
  - **Indirect registration calls:** Calling a registration method/function
    indirectly through a function value (for example, `add := ctx.AddAdminHandler; add(...)`)
    is not statically visible to the compiler. Capturing callbacks passed that
    way are not verified at compile time and are rejected at run time by
    `evmbind`'s `CheckConcurrentUse()` check (plain top-level functions and `nil` still
    pass).
  - **Writes after a borrow:** Taking a method value or closure for concurrent
    registration borrows its receiver and captures at that expression. For an
    annotated interface parameter the borrow starts at the call. A branch that
    borrows locks the object at the join. The compiler follows local aliases
    and statically resolved helpers. Registering another read-only method and
    using a synchronized cache are allowed. Ordinary method values used only
    for local calls retain their usual behavior.
  - This is a static check. Independently supplied arguments can refer to the
    same native object. Go code holding a pointer remains responsible for it.

#### Guest vs. native Go `concurrency.Cache` API

Guest and native Go share the same cache concepts, with one signature difference
on `GetOrLoad`: the VM supplies `vm.Context` automatically in guest code,
whereas native Go callers pass `ctx` explicitly.

Guest script (`import "evmruntime/concurrency"`):

```go
func NewCache[K comparable, V any]() *Cache[K, V]

type Entry[V any] struct { Value V /* plus unexported version ticket */ }

func (c *Cache[K, V]) Get(key K) (Entry[V], bool)
func (c *Cache[K, V]) Set(key K, v V, expiry time.Time) Entry[V]
func (c *Cache[K, V]) GetOrLoad(key K, fetch func() (V, time.Time, error)) (Entry[V], error)
func (c *Cache[K, V]) InvalidateIfCurrent(key K, e Entry[V])
```

Native Go (`import "github.com/andreyvit/evm/evmruntime/concurrency"`):

```go
func NewCache[K comparable, V any]() *Cache[K, V]

type Entry[V any] struct { Value V /* plus unexported version */ }

func (c *Cache[K, V]) Get(key K) (Entry[V], bool)
func (c *Cache[K, V]) Set(key K, value V, expiry time.Time) Entry[V]
func (c *Cache[K, V]) GetOrLoad(ctx context.Context, key K, load func() (V, time.Time, error)) (Entry[V], error)
func (c *Cache[K, V]) InvalidateIfCurrent(key K, entry Entry[V])
```

#### Cache behavior and limits

- **Synchronization:** `Cache[K, V]` synchronizes concurrent calls across VMs
  and goroutines.
- **Capacity:** Each cache holds at most **1,024 entries**. Inserting a new key
  at capacity evicts one arbitrary existing entry.
- **Expiry:** A zero `time.Time{}` keeps an entry eligible until eviction or
  invalidation. A non-zero expiry at or before `time.Now()` is not stored (and
  removes any existing entry for that key on `Set`/put); expired entries are
  deleted lazily on `Get`.
- **Versioned invalidation:** Each `Set` or successful `GetOrLoad` creates a
  new publication identity stored inside `Entry[V]` (copies retain that
  identity). `InvalidateIfCurrent(key, entry)` removes `key` only if the cached
  entry still has that exact publication identity, so invalidating a stale
  entry never deletes a newer `Set` or refresh.
- **Single-flight `GetOrLoad`:** On a hit, returns the cached entry without
  running the loader. On a miss, runs the loader synchronously while concurrent
  callers for the same key wait and can cancel their wait independently via
  context (`vm.Context` in guest code; explicit `ctx` in native Go). If the
  loader returns an error or panics, waiters are released to retry with their
  own loader. If `Set` replaces the key while a load is in flight, the older
  loader can finish without overwriting the newer `Set` entry.
- **Initialization:** Guest code must construct a cache with
  `concurrency.NewCache[K, V]()` (accessing a zero-value guest `Cache` panics).
  In native Go, a zero-value `Cache[K, V]` is ready to use, and must not be
  copied after first use.
- **Key and value limits:**
  - **Guest keys:** `bool`, integers (`int*`/`uint*`), `string`, and
    guest-defined structs or fixed-size arrays composed only of those leaf
    types. Blank `_` fields do not contribute to key equality.
  - **Guest values:** `bool`, integers, floats (`float32`/`float64`), and
    `string`, plus guest structs and fixed-size arrays made from those types.
    The compiler rejects types that can carry guest references. The binding
    copies aggregate values and strings before publication and rejects opaque
    native storage. Each key or value admits up to 4,096 visited fields and
    elements and 1 MiB of string data.
  - **Native Go keys and values:** Any `comparable` `K` and any `V`; `Set` and
    `GetOrLoad` panic on non-reflexive keys (`key != key`, such as `NaN`), and
    callers own the concurrency safety of native Go keys and values.
- **Nested loads:** In guest code, calling `GetOrLoad` inside an active loader
  on the same VM returns the entry if already cached, but fails on a miss with
  `concurrency: nested cache load would block` (across the same key, another
  key, or another cache) to prevent deadlocks. In native Go, a loader must not
  wait on its own pending load.

### Scripts implementing Go host interfaces

A guest type can implement a named host interface. For a host method that
retains the implementation for concurrent calls, annotate its parameter in
the Go binding declaration:

```go
type Registry interface {
    //evm:concurrent impl
    Register(impl Foo)
}
```

Register a factory in `evmbind.Options.InterfaceAdapters` under
`reflect.TypeFor[Foo]()`. It receives a map of method names to bound
`evm.Callable` values and returns a Go adapter implementing `Foo`. The
factory runs once per non-nil registration after verification. Calling
`registry.Register(bar)` checks every method of `Foo` on the concrete guest
receiver and borrows that instance. A different `Bar` instance remains
independent. A nil interface passes through as nil. The compiler requires
statically known guest implementations and nonvariadic interface methods.

Ordinary interface parameters, fields, slices and maps retain their existing
native conversion rules. Hosts can also accept a guest value through `any`
or use the hand-written adapter pattern below.

#### Built-in guest-defined `error` and `OpenCallbackScope`

Built-in `error` is the one Go interface EVM bridges automatically in both
directions (`vm.NativeError`, `vm.NativeAny`, and `evmbind` `error` parameters,
results, struct fields, and map values). A script-defined type implementing
`Error() string`—and optionally `Is(error) bool`, `Unwrap() error`, or
`Unwrap() []error`—arrives in Go as a Go `error` that forwards those methods
back into the VM and preserves identity when passed back to the same program.

```go
// Guest script:
//   import "fmt"
//   type QuotaError struct { Limit int }
//   func (e *QuotaError) Error() string { return fmt.Sprintf("quota %d exceeded", e.Limit) }
//   func Check(n int) error {
//       if n > 10 { return &QuotaError{Limit: 10} }
//       return nil
//   }
checkFn := must(program.LookupFunction("Check", &evm.Signature{
    Params:  []*values.Type{values.IntType},
    Results: []*values.Type{values.ErrorType},
}))

vm := program.AcquireVM(ctx)
scope, err := vm.OpenCallbackScope(evm.Limits{
    Steps:           10_000,
    AllocationBytes: 64 << 10,
    Duration:        100 * time.Millisecond,
})
if err != nil {
    vm.Release()
    return "", err
}
defer scope.Close()

slots := [2]values.Value{values.Int(15)}
if err := vm.Run(checkFn, slots[:]); err != nil {
    vm.Release()
    return "", err
}
guestErr, err := vm.NativeError(slots[1])
vm.Release()
if err != nil || guestErr == nil {
    return "", err
}
// Still callable after vm.Run and vm.Release while scope stays open.
return guestErr.Error(), nil
```

Guest `error` method calls (and native Go `func(...)` wrappers exported from a VM)
follow `CallbackScope` lifetimes:

- **Inside an active host binding:** `guestErr.Error()` (as well as `errors.Is`
  and `errors.Unwrap`) and native Go callbacks re-enter synchronously on the
  borrowed `*evm.VM` with its active globals, context, and remaining budget.
  You do **not** need `OpenCallbackScope` for synchronous callbacks that finish
  before the host binding returns.
- **Without `OpenCallbackScope`:** Calling a script-backed `error` or `func(...)`
  after `vm.Run` returns while `vm` is idle panics with
  `evm.ErrCallbackInactive`; after `vm.Release()` it panics with
  `evm.ErrCallbackExpired`. EVM blocks these by default so leaked Go wrappers
  cannot silently retain a `context.Context`/`*evm.Program` or run unbounded
  script code after a request finishes.
- **With `OpenCallbackScope(limits)`:** Open `scope, err := vm.OpenCallbackScope(limits)`
  while `vm` is idle (invalid limits fail immediately; passing a second time on
  the same VM returns `evm.ErrCallbackScopeOpen`). While `scope` stays open,
  every call outside an active host binding—both while `vm` is idle after
  `vm.Run` and after `vm.Release()`—acquires a fresh VM lease from the same
  `*evm.Program` and Go `ctx`, applies a **fresh per-call budget of `limits`**
  (zero quotas are unlimited, matching `vm.SetBudget`; pass
  `program.DefaultLimits()` to reuse the program's default policy), runs with
  fresh package globals, and releases that VM when the call returns. It does not
  consume or overwrite the originating idle VM's budget. After `scope.Close()`,
  calls panic with `evm.ErrCallbackExpired` and references are cleared (calling
  `Close` while a VM on the scope is running returns `evm.ErrReentry`).
- **Host responsibilities:** Serialize calls on exported adapters sharing one
  scope, and keep `ctx` valid until `scope.Close()`. Native Go errors
  (`errors.New(...)` round-tripped through a script) and native-created Go
  functions do not use a callback scope.

#### Precompiled Go adapter for arbitrary interfaces

Because Go cannot create a new concrete Go type implementing an arbitrary
interface at run time, bridge any other host interface with a small precompiled
Go adapter struct. Resolve each method once with `vm.LookupMethod` into a bound
`evm.Callable` (which retains the guest receiver and owning `*evm.Program`), and
forward each Go method call with `vm.Run`:

```go
// Host package "shop" (exposed with evmbind):
type PricingRule interface {
    Name() string
    Discount(cents int) int
}

// Guest script:
//   import "shop"
//   type VIPRule struct { Percent int }
//   func (r *VIPRule) Name() string { return "vip" }
//   func (r *VIPRule) Discount(cents int) int { return cents * r.Percent / 100 }
//   func NewRule() shop.PricingRule { return &VIPRule{Percent: 15} }

type guestPricingRule struct {
    ctx        context.Context
    nameFn     evm.Callable
    discountFn evm.Callable
}

func newGuestPricingRule(ctx context.Context, vm *evm.VM, iface values.Value) (PricingRule, error) {
    if iface.R == 0 {
        return nil, nil
    }
    nameFn, err := vm.LookupMethod(iface, "Name", evm.Signature{
        Results: []*values.Type{values.StringType},
    })
    if err != nil {
        return nil, err
    }
    discountFn, err := vm.LookupMethod(iface, "Discount", evm.Signature{
        Params:  []*values.Type{values.IntType},
        Results: []*values.Type{values.IntType},
    })
    if err != nil {
        return nil, err
    }
    return &guestPricingRule{ctx: ctx, nameFn: nameFn, discountFn: discountFn}, nil
}

func (r *guestPricingRule) Name() string {
    vm := r.nameFn.Program().AcquireVM(r.ctx)
    defer vm.Release()
    var slots [1]values.Value
    if err := vm.Run(r.nameFn, slots[:]); err != nil {
        panic(err)
    }
    return slots[0].AsString()
}

func (r *guestPricingRule) Discount(cents int) int {
    vm := r.discountFn.Program().AcquireVM(r.ctx)
    defer vm.Release()
    slots := [2]values.Value{values.Int(int64(cents))}
    if err := vm.Run(r.discountFn, slots[:]); err != nil {
        panic(err)
    }
    return int(slots[1].AsInt())
}
```

Obtain the interface value from an entry function (such as
`program.LookupFunction("NewRule", &evm.Signature{Results: []*values.Type{shopSchema.Type("PricingRule")}})`)
and construct the adapter before releasing that VM:

```go
vm := program.AcquireVM(ctx)
var out [1]values.Value
if err := vm.Run(newRuleFn, out[:]); err != nil {
    vm.Release()
    return nil, err
}
rule, err := newGuestPricingRule(ctx, vm, out[0])
vm.Release()
```

Keep these adapter details in mind:

- **Re-entry during an active host call:** If Go code invokes the adapter from
  inside a host binding—or needs the same `OpenCallbackScope` lifetime as guest
  errors—store `scope := vm.CallbackScope()` on the adapter and run `vm.Run`
  inside `scope.WithCall(func(vm *evm.VM) error { ... })` instead of calling
  `AcquireVM`. That re-enters synchronously on the active VM with its globals
  and remaining budget, and falls back to a fresh VM after `Release` while the
  scope stays open.
- **VM errors in interface methods:** If the Go interface method returns
  `error`, return the `vm.Run` error instead of panicking (if the script method
  also returns an `error`, open a callback scope before inspecting or returning
  it past `vm.Release`). Inside an active host callback whose signature cannot
  return `error`, call `vm.PanicCallback(err)` so guest `recover()` and VM traps
  unwind cleanly.
- **Accepting implementations through a host function:** If the script registers
  its implementation by calling a host function instead of returning it from an
  entry point, declare the host parameter as `any` (non-`error` guest types
  arrive as `*evm.GuestValue` carrying `gv.Value`) or use a hand-written
  `ExternalFunction` taking `shopSchema.Type("PricingRule")`, and call
  `vm.LookupMethod` inside that binding. Alternatively, expose
  `*evm.FunctionOf[...]` (or `*evm.ConcurrentFunctionOf[...]` for concurrent
  registrations) when explicit callbacks fit better than a guest type.
- **Concurrency:** `AcquireVM` gives each call fresh globals, but all calls on
  one adapter share the same guest receiver. Methods bound via `vm.LookupMethod`
  are not checked by the compiler, so synchronize access in the adapter if the
  guest receiver mutates its fields across concurrent Go calls (or keep the
  receiver read-only and move shared mutable state into `concurrency.Cache` or a
  synchronized host API).

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
reformatting only), `evmruntime/concurrency`, `html`, `iter`, `maps`,
`math/bits`, `net/url`, `path`, `path/filepath`, `regexp`, `slices`, `sort`,
`time`, `unicode`, `unicode/utf8`, and `unicode/utf16`. Pass `evmstd.Packages()`
alongside your host packages to `Compile` and `Link` (GoX includes them
automatically).

- Nothing in `evmstd` reads files, the network, or the environment—functions
  like `time.Sleep` and `filepath.Glob` are left out.
- `time.Now` is exposed and carries the host's time zone; `time.Local` and
  `time.LoadLocation` are omitted.
- [`evmstd/API.md`](../evmstd/API.md) lists every exposed declaration for the
  generated Go standard library packages and explains why each omitted one was
  excluded; `evmruntime/concurrency` is documented in
  [Concurrent callbacks and caches](#concurrent-callbacks-and-caches). Unlisted symbols
  fail type checking.
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

One kind of mixed package may have function bodies: a package whose native
types are all nongeneric source types bound with `SourceName`, pointers to
them, or pointers to shared scalars, and that declares no records or values.
Its source sees every field, so its bodies compile like any script. See
[Sharing a struct type with its methods](#sharing-a-struct-type-with-its-methods).

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

### Sharing a struct type with its methods

A small domain value, such as an amount with its currency, can be one type for
the host, for stored data and for scripts. The host keeps its Go struct. Scripts
compile the same Go source, so the methods run as script code: no host call,
and no Go struct to build first.

```go
type Coin struct {
    Amount int64  `kvo:"amount"`
    Code   uint32 `kvo:"code"`
}

func (c Coin) Valid() bool { return c.Code != 0 }
```

Three declarations connect the parts:

```go
// 1. The host struct, with value copies kept in one cell.
native, _ := evmbind.New("money", source, evmbind.Options{
    Only: []string{"Coin"}, Types: []reflect.Type{reflect.TypeFor[Coin]()},
    CellStructs: map[string]*values.CellStruct{"Coin": cell},
})

// 2. The package that scripts import: the source, and the source type bound
// to the native shape.
money := evm.Package{Path: "money", Source: coinSource}
for _, d := range native.Package().Types {
    if d.Name == "Coin" {
        d.Name, d.SourceName = "", "Coin"
    }
    money.Types = append(money.Types, d)
}

// 3. Stored sub-objects of the Coin model read as that struct.
money.ValueModels = []evm.ValueModel{{
    Model: coinModel, Type: cell.Type,
    Props: [2]uint64{amount.Code(), code.Code()},
}}
models = evm.WithoutRecords(models, coinModel)
```

- **Cell storage.** A `values.CellStruct` keeps a two-field struct in one cell,
  so a copy allocates nothing. Equal values must have equal cells: `==` and map
  keys compare the cell. Put only a real pointer in `P`, for example a pointer
  into a static table that stands for a small code.
- **Host boundary.** Other schemas import `native` and use the struct in
  signatures and fields as usual.
- **Value model.** A property whose type is the model reads as the struct, by
  value: `line.price.Valid()`, `line.price.Add(line.paid)`. The VM packs the two
  stored words into the cell. It works on encoded rows and on rows backed by a
  Go struct alike, and allocates nothing. A missing sub-object reads as the
  zero value, so test the value, not a pointer.
- **Copies.** The value is a copy of the stored fields. A script can change
  its own copy; it cannot assign to `line.price.Amount` or take `&line.price`.
- **Limits.** Each field must be a scalar property that fits one word. Editable
  rows do not expose such a property yet, and a list or map of the model does
  not compile. `evm.WithoutRecords` removes the model's row types, which
  scripts no longer reach.
- **Inside host structs.** A native struct can have a field of the compact
  type. The field keeps its Go layout there; reads, writes and copies of the
  parent convert through `LoadNative` and `StoreNative`.

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
