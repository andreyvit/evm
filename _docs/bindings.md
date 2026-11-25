# Bindings: hand-written declarations and Go-backed storage

A binding is the Go code behind anything a script can name. This guide covers
two topics that the [`evmbind` guide](../evmbind/README.md) leaves out: how to
declare a package by hand, and how scripts interact with records backed by Go's
own memory layout. It finishes with measurements for direct calls and binding
reuse.

## Three ways to bind

| What you have | What to use | How it connects |
| --- | --- | --- |
| A Go API package | `evmbind.New` with generated `Options.Static` bindings | Compile with `schema.Package()`, link with `schema.Bind(host)`. |
| A Go helper over a KVO model | `evmbind.RecordMethod`, `GenerateMethods`, `RegisterMethods` | The helper becomes a method on the model's script type. |
| A Go method of a model struct | `Options.Models` with `Options.Methods`, then `Schema.AddModelMethods` | The method runs on the model's row; the model package declares it. |
| Neither, or you want full control | `evm.Package` with `Types`, `Functions`, and `Methods` | You write each signature and call handler directly. |

All three paths produce the same declarations. Host functions and record
methods share one signature classifier and one scalar-wrapper generator, so
generated calls are always direct, typed Go calls with no reflection.

## Hand-written declarations

An `evm.ExternalFunction` has a name, a signature, and one of two call forms:

- **`Call` (`func(*evm.VM) error`)** works
  for any signature. It reads inputs with `vm.CallArgument(i)` and writes
  outputs with `vm.SetCallResult(i, value)`.
- **`Direct` (`evm.DirectBinding`)** works for small signatures. The VM passes
  operands straight from registers in a single instruction, avoiding an
  argument window.

A direct function takes up to two arguments; a direct pointer method takes its
receiver plus up to one argument. Both return zero or one result. A record
receiver occupies the first argument as a 16-byte pair, leaving room for one
additional argument. Any larger signature uses `Call`. Function values,
interface methods, and deferred calls behave identically regardless of which
form you provide.

For a one-word-in, one-word-out function, use `evm.DirectUnary`:

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

`DirectUnary` selects the fastest word-only instruction and automatically
provides a `Call` adapter for indirect calls (an explicit `Call` overrides that
adapter, as with any `DirectBinding`). Build bindings during setup and leave
them immutable afterward.

### Declaring types by hand

Each type is described by a `values.Shape`, which defines its VM cells (and
optionally its Go memory layout):

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

Use `customerPtr.Type` in signatures. `New`, `SetField`, and `GetField` operate
on VM cells; a pointer to a real Go struct needs a shape with a native layout,
which `evmbind` builds from the Go type.

## Package functions bound by value

`evmbind.Options.Functions` maps a source function name to a Go function value
(either a package function or a closure). `evmbind.New` reads only the
signature and referenced types from source and never executes a function body.
The bound function receives only the declared arguments—no host receiver and no
`*evm.VM`.

`evmbind.Generate` accepts the same options, and its generated wrappers call
the registered function value with the same argument and result semantics as
the reflective path. Because a function bound by value and a host method of the
same name produce different fingerprints, generated wrappers for one are
considered stale for the other: `Automatic` falls back to reflection and
`StaticOnly` fails. If both are present, the function value wins.

## Records backed by Go structs

Setting `evm.RecordType.NativeAllocation` tells a model which Go struct type to
allocate when a script creates a value of that model. Hosts opt in per model;
leaving it `nil` keeps generic record storage. Keep this setting identical
between compilation and linking.

Two principles govern everything below:

1. **Go semantics apply:** Struct copies are shallow, pointers keep their Go
   identity, and slices keep their backing arrays.
2. **No hidden conversions:** Bindings never decode an encoded record behind
   your back, cache a converted copy, or copy mutations back.

### Pointer fields

A host struct field `Customer *T` (where `T` is a registered model struct)
appears to scripts as a read-only record. Reading it returns the original
pointer; assigning another Go-backed record or `nil` updates the Go pointer
field directly. Scripts can copy or allocate the containing struct normally,
and generated and reflective bindings share the same field layout.

To store an encoded record in such a field, convert it to the field's Go type
first (see [explicit copies](#explicit-copies)).

### Slices

A host `[]*T` appears as a slice of read-only records and keeps its Go backing
array, length, and capacity. Element assignment, `copy`, `clear`, and `append`
follow ordinary Go rules: having read-only record fields does not make the
slice itself read-only. Subslices share the backing array through a small view
descriptor.

- A slice crosses the host boundary with its original backing intact, including
  when the host retains it or calls back into the script.
- Different Go pointer types can back slices of the same model. Storage travels
  with each slice value, and storing into a field or passing a native argument
  requires matching backing.
- A slice built in VM cells cannot be passed directly to a native slice
  parameter; use `copy` or `append` to move compatible elements between the
  two.
- A slice of pointers—and a decoded list of records—can still hold encoded
  records, whereas a Go-backed typed collection holds only matching Go
  pointers.
- When a script creates a slice via a literal, `make`, or `append` to `nil`,
  `NativeAllocation` determines its backing storage.

Maps of models are not supported.

### Values

Dereferencing a pointer (`saved := *p`) takes a shallow Go struct copy: scalar
fields become independent, while pointers, slices, and maps stay shared. Fields
not exposed by the model are copied as well and participate in equality and
hashing just as in Go. A struct that Go cannot compare cannot be compared in a
script either.

A record value has real, addressable Go storage wherever it lives—in a local,
a package variable, a script struct field, or an array element:

- `&saved` keeps its Go address through a record-pointer binding, and
  reassigning `saved` writes to that storage.
- A zero value reads zero for every property; dereferencing a `nil` pointer
  fails.
- Reading a property from a function result or map lookup works without an
  addressable temporary.

Scripts cannot:

- Assign to a record's properties (records are read-only; use an editor).
- Overwrite a whole record through a pointer (`*p = ...`), regardless of where
  the pointer came from. Assigning a record variable—or a struct or array
  containing one—is allowed.
- Define a new named type from a model (`type R db.Customer`) or a pointer to
  one, because that would lose the model. Use an alias (`type R = db.Customer`)
  instead.
- Dereference an encoded record into a Go-backed value without converting it
  first.

Native code receiving a `*T` treats it as an ordinary Go pointer with no origin
or ownership checks. Signatures preserve the distinction between value and
pointer (`Copy(*row)` passes `T`, `Echo(row)` passes `*T`), and by-value
parameters, results, and callbacks perform shallow copies (including hidden
fields). A zero value is valid, while `nil` is not. By-value declarations
require `T` to be the model's `NativeAllocation`; other layouts cross by
pointer.

Model values can also cross `any`, where a model binding takes precedence over
an ordinary struct binding. A host struct field can hold a model value when its
Go type matches `NativeAllocation` (assigning copies it, and taking its address
points to the real field). Slices of model values share their backing arrays
across calls. Maps of model values and formatting a record value with `fmt` are
not supported.

### Explicit copies

After setting `NativeAllocation`, call `evm.RegisterNativeCopies(&pkg)` before
publishing the package. Read-only records of that model gain a `NativeCopy()`
method that returns a new Go-backed record pointer (or `nil` for a `nil`
receiver):

```go
native := encoded.NativeCopy()
rows := []*db.Document{native}
value := *native
```

What `NativeCopy()` does depends on the receiver's backing:

- **Encoded record:** Decodes it into a new Go struct.
- **Already backed by the selected Go type:** Takes a shallow struct copy;
  reference fields stay shared and fields outside the model survive.
- **MessagePack row bound to that Go type:** Preserves stored fields that the
  model does not know about.
- **Backed by a different Go type:** Copies only the modeled fields.

Nothing is cached and the original is untouched. The VM charges the allocation
of the root struct; allocations inside the record decoder are host work and are
not fully charged.

Ordinary reads never decode. A `[]any` can hold encoded records directly and
recover them with a type assertion, whereas placing encoded records into a
Go-backed typed collection costs one `NativeCopy` per element.

## Selecting fields

`evmbind.Options.Fields` maps a struct name to the exact list of fields scripts
may see. Omitting a struct exposes all of its exported fields; passing an empty
list exposes none:

```go
Fields: map[string][]string{"Page": {"Title", "Count"}}
```

Any field added to `Page` later stays hidden until you add it to the list.
Embedded fields are selected by their declared name. Unknown or duplicate names
fail schema construction, and you cannot specify both `Fields` and `OmitFields`
for the same type.

Field selection changes declarations, not memory layout: a pointer still points
to the whole struct, struct copies retain hidden fields and copy them on
assignment, fresh native storage starts with hidden fields zeroed, and
assigning a fresh literal over existing native storage preserves the
destination's hidden fields. A model's record type takes its fields from the
model and cannot be overridden here. Structs with more than one blank (`_`)
padding field cannot be selected and return an error.

## Type aliases

Source bindings preserve Go type aliases, including aliases of imported and
built-in types such as `type Key = string`. In hand-written packages,
`evm.Package.Aliases` gives an existing VM type another name without adding a
runtime type, layout, allocation, or conversion.

- The alias target must be declared in the same environment.
- Inside pointers, collections, and signatures, aliases resolve to their
  target; printed declarations show `type X = Y`.
- Merging packages drops identical duplicate aliases and rejects conflicting
  exported names.
- An alias supports everything its target supports. Generic aliases without
  type arguments and aliases of EVM function handles are not supported.

## Time

`time.Time` in a source binding maps directly to the VM's `time.Time`, with
Go's unexported fields kept hidden:

- A `time.Time` crossing `any` preserves its time zone and monotonic reading.
  `==` and map keys compare the full Go value, `Equal` compares instants, and
  formatting uses the value's offset.
- Reading a `time.Time` field takes a snapshot, while a field address or
  `*time.Time` argument keeps the original Go pointer. Script-allocated time
  pointers use Go storage, and slices and maps keep their Go backing and key
  equality.
- A UTC instant in the nanosecond range allocates nothing. Any other Go time
  uses one immutable 24-byte copy, and a UTC instant outside the nanosecond
  range uses a 12-byte payload.
- Storing a boxed Go time—or zero—into a Go field copies without allocating.
  Storing a compact UTC instant allocates one charged 24-byte temporary for the
  field setter.
- Nothing is cached or written back. KVO time properties read as UTC instants,
  and timestamp serialization normalizes to UTC.

## Empty structs and unnamed pointers

`struct{}` and `*struct{}` work in source bindings, including set maps
(`map[string]struct{}`) and slices. Unnamed pointer types—pointers to
primitives, timestamps, and empty structs—share one identity across packages by
their element type. Go collections keep their backing storage, and collections
created by a script can be passed back to Go. Anonymous structs with fields are
not supported.

## Metadata lifetime

`kvo.Bind(goType, model)` and `kvo.Project(goType, model, nil)` return a cached
plan for that `(goType, model)` pair, so calling them again allocates no plan
and no new representation ID (bindings and projections of the same pair remain
distinct plans). Plans with an explicit field map, `WithGenerated`, or
`WithNativeSchema` are built fresh each time—construct them once during setup
and reuse them, just like schemas, models, and typed properties.

Records and collections look up metadata in descriptor tables indexed by
representation ID: one atomic slice load and one index. Registration, growth,
and publication share a single locked path. Representation IDs and their
metadata live for the lifetime of the process. Both tables share one global ID
space, so each has unused slots.

On `arm64`, a record entry is 48 bytes, a collection entry is 64 bytes, and
each table's lock and publication header is 16 bytes. A record layout publishes
eight representation entries on first use (these figures exclude spare array
capacity, shapes, and binding plans). `Record`, `ListCell`, and the typed list
handles are each 16 bytes.

## Measurements

Measured on Apple M5 Max, Go 1.27.3, `darwin/arm64`. Each number is the median
of three 200 ms samples with warm metadata. Both benchmark binaries were built
up front and run alternately in three rounds on an otherwise idle machine.
Times cover each benchmark's complete operation; differences of a few percent
are within noise.

"Before" is the code without direct void calls and without the cached default
plan; "after" includes both (revisions `4b0e318` and `cb72134` in the `edb`
repository, where this code was developed).

| Workload | Before | After | Allocations before → after | Bytes before → after |
| --- | ---: | ---: | ---: | ---: |
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

Key takeaways:

- Void functions took 57% less time and void methods 67% less.
- Field access, list access, and record methods stayed within a few percent
  with identical allocation counts. Struct construction was 2.4% slower in this
  small sample.
- The word-only specialization earns its keep: 1.992 µs versus 3.300 µs for
  the same function through the ordinary `DirectFunc`.
- The single remaining allocation in the void-method row is the Go struct the
  benchmark creates per run, not the 256 calls.
- The `Bind` row warms one default binding and measures repeated `Bind` calls
  for the same type and model; it does not measure first-time construction or
  custom configurations.

### Reproduce

Run each command on its own on an idle machine:

```sh
go test ./evmbind -run '^$' -bench '^BenchmarkGeneratedHost$' -benchmem -benchtime=200ms -count=3
go test ./evmbind -run '^$' -bench '^BenchmarkRecordMethods/Fast2/' -benchmem -benchtime=200ms -count=3
go test ./kvo -run '^$' -bench '^BenchmarkRecordHandle$/^(Fast2|Generated)$/^(Read|OpenRead)$' -benchmem -benchtime=200ms -count=3
go test ./kvo -run '^$' -bench '^BenchmarkTypedList$/^(3|32)$/^Fast2$/^CellHandle$' -benchmem -benchtime=200ms -count=3
go test ./kvo -run '^$' -bench '^BenchmarkBindingReuse$' -benchmem -benchtime=200ms -count=3
go test ./bench -run '^$' -bench '^BenchmarkUnaryBinding$' -benchmem -benchtime=200ms -count=3
go test ./evmcompiler -run '^$' -bench '^BenchmarkCompileAndInitialize/Run$' -benchmem -benchtime=200ms -count=3
```

The paired samples are in
[bindings-consolidation.tsv](../bench/bindings-consolidation.tsv). Test
coverage spans generated, general, and reflective calls, void mutation,
indirect and deferred calls, cancellation, ownership boundaries, concurrent
registration and reuse, nominal shape identity, aggregate copies, and linked
metadata snapshots.
