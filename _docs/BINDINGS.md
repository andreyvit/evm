# Shared binding declarations and registration

Host functions and registered KVO methods share signature classification and
scalar wrapper generation. Both use `DirectBinding`; generated calls stay typed
Go calls, without reflection.

## Choose declarations once

- For a Go API package, use `evmbind.New` and generated `Options.Static` bindings.
  Compile with `schema.Package()` and link with `schema.Bind(host)`.
- For helpers over a KVO model, use `evmbind.RecordMethod`, `GenerateMethods`, and
  `RegisterMethods`. This explicitly maps a helper to its model and script name.
- For manual declarations, use `Package.Types`, `Functions`, and `Methods`.
  Types use `values.Shape` for both VM cells and optional native layouts. A small
  call supplies `DirectBinding`; an arbitrary signature supplies `Call`.

Direct functions support up to two arguments; pointer methods support one explicit
argument. Both support zero or one result. Record receivers occupy the first
function argument as a 16-byte pair, leaving room for one explicit argument.
Unsupported direct signatures use the ordinary ABI. Function values, interface
methods and deferred calls retain their normal behavior.

A word-only function uses the same declaration, with a convenience constructor:

```go
next := evm.ExternalFunction{
    Name: "Next",
    Signature: evm.Signature{
        Params:  []*values.Type{values.Uint64},
        Results: []*values.Type{values.Uint64},
    },
    Direct: evm.DirectUnary(func(n uint64) uint64 { return n + 1 }),
}
```

`DirectUnary` keeps the faster word-only instruction internally. It also supplies
an ordinary ABI adapter; an optional explicit `Call` overrides that adapter as
with any other `DirectBinding`. There is no `ExternalFunction.RawUnary` path.
Treat the returned binding as immutable setup metadata.

For a hand-declared VM struct:

```go
text := &values.Shape{Type: values.StringType, Kind: values.StringKind, Cells: 1}
customer := values.NewStructShape("shop.Customer",
    values.Field{Name: "Name", Shape: text})
customerPtr := values.NewPointerShape(customer)
pkg := evm.Package{
    Path: "shop",
    Types: []evm.TypeDecl{
        {Name: "Customer", Shape: customer},
        {Shape: customerPtr},
    },
}
p := customer.New()
customer.SetField(p, 0, values.String("Ada"))
name := customer.GetField(p, 0).AsString()
```

Use `customerPtr.Type` in signatures. These helpers access VM cells. Native Go
pointers require native layouts. Both use `Shape`; retired struct opcodes 36–39
remain reserved.

## Metadata lifetime and memory

`kvo.Bind(goType, model)` and `kvo.Project(goType, model, nil)` return
cached default plans by Go type/model identity. Repeated calls allocate neither
plans nor representation IDs. Full bindings and projections remain distinct.
Explicit field maps and `WithGenerated`/`WithNativeSchema` configurations are
independent: construct once and retain them, rather than constructing per row or
transaction. Models, schemas and typed properties also belong in setup.

Record and collection descriptor tables share registration, growth, locking,
and atomic publication. Hot lookup remains one atomic slice load and a flat
representation-ID index, with no additional Shape lookup. IDs and their reachable
metadata are process-lifetime and never reclaimed. The shared global ID space
still produces unused slots in each table; this change deliberately retains the
existing storage/lookup tradeoff.

On arm64, record entries remain 48 bytes, collection entries remain 64 bytes,
and each registry's lock/publication header is 16 bytes. A record layout lazily
publishes eight representation entries. These sizes exclude spare array capacity,
global Shape slots, Shapes and binding plans; they are not a claim of total
metadata cost. `Record`, `ListCell` and typed list handles remain 16 bytes.

## Measurements

Measured on Apple M5 Max, Go 1.27.3, darwin/arm64. Medians of three 200 ms
samples, with warm metadata. Baseline/current benchmark binaries were built first
and alternated in three rounds, with no task test suites running concurrently.
Times include the benchmark's entire operation;
small changes are descriptive, not statistical significance claims.

| Workload | Before | After | Allocations before → after | Bytes before → after |
|---|---:|---:|---:|---:|
| Generated void function, 256 calls | 9.408 µs | 4.045 µs | 256 → **0** | 24,576 → **0** |
| Generated void method, 256 calls | 10.159 µs | 3.370 µs | 257 → **1** | 24,640 → **64** |
| Generated predicate, 256 calls | 4.646 µs | 4.649 µs | 0 → 0 | 0 → 0 |
| Fast2 record method via EVM, 256 calls | 7.182 µs | 7.216 µs | 0 → 0 | 0 → 0 |
| Fast2 record field read | 5.735 ns | 5.721 ns | 0 → 0 | 0 → 0 |
| Fast2 open + field read | 7.463 ns | 7.475 ns | 0 → 0 | 0 → 0 |
| Generated native record field read | 2.961 ns | 2.894 ns | 0 → 0 | 0 → 0 |
| Fast2 typed list handle, 32 elements | 10.64 ns | 10.68 ns | 0 → 0 | 0 → 0 |
| Repeated default `Bind` | 8,123 ns | **11.94 ns** | 156 → **0** | 15,960 → **0** |
| Word-only specialization, 256 calls | 2.058 µs | 1.992 µs | 0 → 0 | 0 → 0 |
| Struct construction + two host calls | 159.3 ns | 163.2 ns | 2 → 2 | 64 → 64 |

Void functions improved 57%; void methods improved 67%. Existing field/list
access and generated record methods stayed within a few percent, with unchanged
allocation counts. Struct construction was 2.4% slower in this small sample.
The word-only specialization is still worth retaining: its current 1.992 µs
compares with 3.300 µs for the ordinary `DirectFunc` implementation.

An initial unpaired rerun slowed unchanged controls as well as changed code by
roughly 40%. The table uses only the interleaved baseline/current runs above;
allocation conclusions were identical in both runs.

`VoidMethod` constructs one native row per run; its remaining allocation belongs
to that row, not to the 256 calls. The repeat-binding benchmark warms one default
binding and then measures repeated `Bind` for the same Go type/model. It does not
measure the first binding construction or custom configurations.

Commands (run separately from other benchmark/test loads):

```sh
go test ./evmbind -run '^$' -bench '^BenchmarkGeneratedHost$' -benchmem -benchtime=200ms -count=3
go test ./evmbind -run '^$' -bench '^BenchmarkRecordMethods/Fast2/' -benchmem -benchtime=200ms -count=3
go test ./kvo -run '^$' -bench '^BenchmarkRecordHandle$/^(Fast2|Generated)$/^(Read|OpenRead)$' -benchmem -benchtime=200ms -count=3
go test ./kvo -run '^$' -bench '^BenchmarkTypedList$/^(3|32)$/^Fast2$/^CellHandle$' -benchmem -benchtime=200ms -count=3
go test ./kvo -run '^$' -bench '^BenchmarkBindingReuse$' -benchmem -benchtime=200ms -count=3
go test ./bench -run '^$' -bench '^BenchmarkUnaryBinding$' -benchmem -benchtime=200ms -count=3
go test ./evmcompiler -run '^$' -bench '^BenchmarkCompileAndInitialize/Run$' -benchmem -benchtime=200ms -count=3
```

The baseline is commit `4b0e318`; the implementation is `cb72134`.
Raw paired samples are in [bindings-consolidation.tsv](../bench/bindings-consolidation.tsv).
Tests cover generated/general/reflected calls,
void mutation, indirect/deferred calls, cancellation, ownership boundaries,
concurrent registration/reuse, nominal shape identity, aggregate copies, unused
scalar lanes, and linked metadata snapshots. The executable book validates all
29 examples and the complete public instruction table.

## Native package functions

Use `Options.Functions` to bind selected source function names to actual Go
function values. `NewTypes` reads their signatures and referenced types from the
embedded source; it does not execute their bodies. The supplied values may be
ordinary package functions or closures. They receive the declared arguments,
without a synthetic host receiver or `*Call` parameter.

`GenerateTypesWithOptions` generates wrappers from the same selection. Generated
ordinary and direct wrappers invoke the registered function value, preserving
native argument and result behavior. Function-backed declarations have distinct
binding fingerprints from host-method-backed declarations, so stale generated
wrappers fail registration. An explicit function binding takes precedence over
a same-named host method.

## Native model pointer fields

A native struct field `Customer *T`, where `T` is a registered model, exposes
an immutable typed row. Reading retains the original pointer. Assigning another
native-backed row or nil updates the actual Go pointer slot. The containing
struct may be copied or allocated by the script with ordinary Go semantics.
Generated and reflected bindings use the same physical field layout.

Assigning an encoded record requires explicit conversion to the field's native
Go type; bindings do not decode or copy it implicitly. Inline model values use
the selected native allocation type and preserve ordinary shallow copies.

## Native model slices

A host `[]*T` whose `T` is a registered model appears as a slice of immutable
model rows. The slice retains its Go backing array, length and capacity.
Element assignment, copy, clear and append follow Go slice semantics; immutable
row fields do not make the slice itself immutable. Native row pointers retain
identity. Stores and native arguments require matching backing. Encoded records
and other native layouts require an explicit conversion; bindings never decode,
cache converted identities or copy mutations back.

Physical storage travels with each native slice value. Different native pointer
types can back the same logical model-row slice. Native subslices retain their
backing array through a small view descriptor.

`RecordType.NativeAllocation` selects the native model type used when scripts
allocate collections with literals, `make` or append to nil. The host opts into
a registered native type; the default nil uses generic record storage.
Pointer rows and decoded record lists can still refer to encoded records.
Native-backed collections require explicit conversion before storing them.
The same allocation choice also determines the storage of row values below.

Matching slices cross the host boundary with their original backing, including
when the host retains them or invokes callbacks. Cell-backed slices cannot be
passed to a native slice parameter. Normal copy/append operations can transfer
compatible elements between cell-backed and native-backed slices.

Native model value slices and model maps are not yet supported.

## Native-backed row values

For a row with `NativeAllocation`, `saved := *p` takes an ordinary shallow Go
struct copy. Scalar fields become independent; pointer, slice and map fields
retain their Go sharing semantics. Fields omitted from the model API are
preserved too and participate in equality and hashing as they do in Go.
Noncomparable native structs remain noncomparable in scripts.

Local variables, globals, and rows inside script structs and arrays have real
native addressable storage. `&saved` retains identity when passed through a row
pointer binding. Reassigning the variable updates that storage. A zero row reads
zero properties; a nil row pointer still fails on dereference. Property reads
also work on function results and map entries without creating an addressable
temporary.

Row properties are read-only. Explicit whole-row writes through `*p` are also
compile errors, regardless of where the pointer came from. Assigning a row
variable or a containing struct/array remains legal. Native code receiving the
pointer retains its ordinary Go behavior; no origin or ownership checks apply.

A new defined type such as `type R db.Customer` is unsupported because it would
lose the model representation. Use `type R = db.Customer` to preserve identity.
This also applies to new defined pointer types based on model rows.

Dereferencing an encoded row into a native-backed value requires explicit
conversion to the selected native type first. No decoding or copyback happens
implicitly. Native signatures preserve value versus pointer parameters:
`Copy(*row)` passes
`T` by value, while `Echo(row)` passes `*T`. Value parameters, results and callbacks
use shallow Go copies, including hidden fields; zero values are supported and
nil is not a value argument. By-value declarations require `T` to match the
model's `NativeAllocation`; other layouts remain available through pointers.
Allocation metadata must stay unchanged between compilation and linking.

Native model values also cross `any` as row values, with model bindings taking
precedence over ordinary native struct bindings. Native struct fields may
store model values when their native type matches
the declared allocation. Field assignment copies the native value; taking its
address refers to the actual native field. Native value slices share their
backing arrays across calls. Guest slice and array allocations use the declared
native type, including append, copy and slicing. Maps of model values and
row-value formatting remain unfinished.

## Selecting native fields

`evmbind.Options.Fields` maps native struct names to the direct fields exposed
to scripts. A missing entry exposes all fields; an empty list exposes none.
For example, `Fields: map[string][]string{"Page": {"Title", "Count"}}` keeps
new native fields outside the script API until explicitly selected. Embedded
fields use their declared field name. Unknown or duplicate selections fail
schema construction. Do not combine `Fields` and `OmitFields` for one type.

Field selection changes declarations, not storage. Native pointers still
point at the original structs, and native struct copies retain hidden fields.
Model rows use their model metadata instead; field selection cannot override
that metadata. Source selection is resolved once when building the schema.

The existing omission machinery cannot describe multiple blank (`_`) padding
fields in one struct. Selecting such a struct currently reports an error.

## Native type aliases

Source bindings preserve ordinary Go aliases, including renamed imports and
builtin aliases such as `type Key = string`. `evm.Package.Aliases` gives an
existing VM type another source name. It introduces no runtime type, layout,
allocation or conversion. The target declaration must be included in the same
environment; a missing target is an error. Consolidated packages omit redundant
same-name aliases, and conflicting exported names remain errors.

Aliases inside pointers, collections and function signatures resolve to the
same types as their targets. Package declaration output uses `type X = Y`.
Uninstantiated generic alias declarations and aliases of EVM function handles
are not supported. Other aliases retain the binding restrictions of their
target type; aliases do not add support for anonymous struct/interface targets.

## Native time values

Native `time.Time` values crossing through `any` retain their timezone and
monotonic clock reading. Go equality (`==`) and map keys compare the complete
Go value; `Equal` still compares instants. Formatting keeps the native offset.

Canonical UTC values in the nanosecond range allocate nothing. Other native
time values use one immutable 24-byte copy; UTC values outside that range keep
the existing 12-byte instant payload. There are no value caches or write-back
steps. Database timestamp serialization explicitly normalizes to a UTC instant
and retains the existing wire format.

Source bindings recognize native `time.Time` fields and signatures as the
existing VM `time.Time`, without exposing Go's private fields. Native field
reads snapshot the value; field addresses and `*time.Time` arguments keep the
original pointer. Guest-created time pointers use native Go storage. Slices
and maps retain native backing and Go key equality.

Writing a boxed native time into a native slot copies from its immutable
payload without another allocation. Zero stores also allocate nothing.
Writing a compact UTC instant needs one charged 24-byte temporary for the
native field setter. This is value assignment, not a retained mirror or
mutation copyback. KVO field reads still return database UTC instants.

Anonymous empty structs (`struct{}`) and pointers to them are supported in
source bindings, including native set maps (`map[string]struct{}`) and slices.
Unnamed pointers share identity across packages by their element type,
including primitive, timestamp and empty-struct pointers. Native collections retain
their backing storage; guest-created collections can pass to native code.
Nonempty anonymous structs remain unsupported by source bindings.

## Explicit native row copies

After selecting `RecordType.NativeAllocation`, the host calls
`evm.RegisterNativeCopies(&pkg)` before publishing the package. This adds
`row.NativeCopy()` to immutable rows with a selected allocation. It returns a
fresh native-backed row pointer, or nil for a nil receiver. For example:

```go
native := encoded.NativeCopy()
rows := []*db.Document{native}
value := *native
```

An encoded row is decoded explicitly. A native row of the selected Go type
uses a shallow struct copy: its reference fields remain shared, and fields
outside the model survive. MessagePack with that same native binding preserves
stored unmodeled fields. Converting another native projection preserves only
modeled fields. No result is cached, and the original row remains unchanged.
The VM charges the root struct allocation; allocations made by the native
record decoder are native host work and are not fully included in that charge.

Ordinary reads remain encoded. Generic `[]any` collections can retain encoded
row handles without decoding; type assertions recover their typed row API.
Native-backed typed collections require matching native pointers, so converting
before insertion is explicit and costs a decode per encoded row.
