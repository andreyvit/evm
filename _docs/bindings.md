# Bindings: hand-written declarations and Go-backed storage

A binding is the Go code behind something a script can name. This file covers
two things the [`evmbind` guide](../evmbind/README.md) leaves out: how to
declare a package by hand, and what a script sees when a value is stored in
Go's own memory layout. It ends with the measurements for direct calls.

## Three ways to bind

| You have | Use | Result |
| --- | --- | --- |
| A Go API package | `evmbind.New`, with generated `Options.Static` bindings | Compile with `schema.Package()`, link with `schema.Bind(host)`. |
| A Go helper over a KVO model | `evmbind.RecordMethod`, `GenerateMethods`, `RegisterMethods` | The helper becomes a method of the model's script type. |
| Neither, or full control | `evm.Package` with `Types`, `Functions` and `Methods` | You write each signature and each call. |

All three end in the same declarations. Host functions and record methods
share one classification of signatures and one generator of scalar wrappers,
so a generated call is a typed Go call with no reflection on any path.

## Hand-written declarations

An `evm.ExternalFunction` has a name, a signature and one of two
implementations:

- `Call`, a `func(*evm.Call) error`, serves any signature. It reads
  `c.Argument(i)` and writes `c.SetResult(i, value)`.
- `Direct`, an `evm.DirectBinding`, serves a small signature. The VM passes
  the operands from registers in one instruction, with no argument window.

A direct function takes up to two arguments. A direct pointer method takes its
receiver and up to one argument. Both return zero or one result. A record
receiver occupies the first argument as a 16-byte pair, which leaves room for
one more. Any other signature uses `Call`. Function values, interface methods
and deferred calls behave the same whichever implementation a function has.

A function of one word to one word has a constructor:

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

`DirectUnary` selects the fastest word-only instruction and also supplies an
ordinary `Call` adapter for indirect uses. An explicit `Call` overrides the
adapter, as with any `DirectBinding`. Build bindings during setup and do not
change them afterwards.

A type is declared by its `values.Shape`. A shape describes VM cells, and
optionally a Go memory layout:

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

Use `customerPtr.Type` in signatures. `New`, `SetField` and `GetField` work on
VM cells. A pointer to a real Go struct needs a shape with a native layout,
which `evmbind` builds from the Go type.

## Package functions bound by value

`evmbind.Options.Functions` binds a source function name to a Go function
value: a package function or a closure. `evmbind.New` reads the signature and
the types it mentions from the source and never runs a body. The value
receives the declared arguments only, with no host receiver and no `*evm.Call`.

`evmbind.Generate` generates wrappers from the same options. A generated
wrapper calls the registered function value, so arguments and results behave
as in the reflective path. A function bound by value and a host method of the
same name have different fingerprints: generated wrappers for one are stale
for the other. `Automatic` then falls back to reflection and `StaticOnly`
fails. When both exist, the function value wins.

## Records backed by Go structs

`evm.RecordType.NativeAllocation` names the Go struct type that a model uses
when a script allocates a value of it. The host opts in per model; nil keeps
the generic record storage. The rules below hold for a model with this
setting, and for host structs that contain such models. Keep the setting
unchanged between compiling and linking.

Two rules run through all of it. Go semantics apply: a copy is a shallow Go
copy, a pointer keeps its identity, a slice keeps its backing array. And
nothing converts implicitly: bindings never decode an encoded record, cache a
converted one, or copy changes back.

### Pointer fields

A host struct field `Customer *T`, where `T` is the struct of a registered
model, appears to scripts as a read-only record. Reading it returns the
original pointer. Assigning another Go-backed record, or nil, writes the Go
pointer field. The containing struct can be copied or allocated by a script as
any struct can. Generated and reflective bindings use the same field layout.

To store an encoded record in such a field, convert it to the field's Go type
first (see [explicit copies](#explicit-copies)).

### Slices

A host `[]*T` appears as a slice of read-only records. It keeps its Go backing
array, length and capacity. Element assignment, `copy`, `clear` and `append`
follow Go: read-only record fields do not make the slice read-only. A subslice
keeps the backing array through a small view descriptor.

- A slice crosses the host boundary with its original backing, also when the
  host retains it or calls back into the script.
- Different Go pointer types can back slices of the same model. Storage
  travels with each slice value, and a store or a native argument needs the
  matching backing.
- A slice the script built in VM cells cannot be passed to a native slice
  parameter. `copy` and `append` move compatible elements between the two
  kinds.
- A slice of pointers, and a decoded list of records, can still hold encoded
  records. A Go-backed typed collection holds only matching Go pointers.
- When a script creates a collection with a literal, `make`, or `append` to
  nil, `NativeAllocation` decides its storage.

Maps of models are not supported.

### Values

`saved := *p` takes a shallow Go struct copy. Scalar fields become
independent; pointer, slice and map fields keep sharing. Fields that the model
does not expose are copied too, and take part in equality and hashing as they
do in Go. A struct that Go cannot compare cannot be compared in a script.

Such a value has real addressable Go storage wherever it lives: a local, a
package variable, a field of a script struct, an array element. `&saved` keeps
its identity through a record-pointer binding, and reassigning the variable
writes that storage. A zero value reads zero properties. A nil pointer fails
on dereference. A property can be read from a function result or a map entry
without an addressable temporary.

What a script cannot do:

- Assign a property. Records are read-only; use an editor.
- Write a whole record through a pointer, `*p = ...`, wherever the pointer
  came from. Assigning a record variable, or a struct or array that contains
  one, is allowed.
- Define a new type from a model, `type R db.Customer`, or from a pointer to
  one. It would lose the model. Write an alias, `type R = db.Customer`.
- Dereference an encoded record into a Go-backed value without converting it.

Native code that receives a pointer treats it as an ordinary Go pointer. No
origin or ownership check applies there.

Signatures keep the difference between value and pointer. `Copy(*row)` passes
a `T`; `Echo(row)` passes a `*T`. Value parameters, results and callbacks use
shallow copies, hidden fields included. A zero value is a valid argument and
nil is not. A by-value declaration needs `T` to be the model's
`NativeAllocation`; other layouts cross by pointer.

Model values also cross `any`, where a model binding takes precedence over an
ordinary struct binding. A host struct field can hold a model value when its
Go type is the declared allocation: assigning it copies, and taking its
address refers to the real field. Slices of model values share their backing
arrays across calls. Maps of model values, and formatting a record value with
`fmt`, are not supported.

### Explicit copies

After setting `NativeAllocation`, call `evm.RegisterNativeCopies(&pkg)` before
publishing the package. Read-only records of that model gain `NativeCopy()`,
which returns a new Go-backed record pointer, or nil for a nil receiver:

```go
native := encoded.NativeCopy()
rows := []*db.Document{native}
value := *native
```

- An encoded record is decoded.
- A record already backed by the selected Go type gets a shallow struct copy:
  reference fields stay shared, and fields outside the model survive.
- A MessagePack row bound to that Go type keeps its stored fields that the
  model does not know.
- A record backed by another Go type keeps only modeled fields.

Nothing is cached and the original is unchanged. The VM charges the allocation
of the root struct. Allocations inside the record decoder are host work and
are not fully charged.

Ordinary reads never decode. A `[]any` can hold encoded records as they are,
and a type assertion recovers the typed record. Putting encoded records into a
Go-backed typed collection costs one `NativeCopy` each.

## Selecting fields

`evmbind.Options.Fields` maps a struct name to the fields scripts may see. No
entry exposes every exported field; an empty list exposes none:

```go
Fields: map[string][]string{"Page": {"Title", "Count"}}
```

A field added to `Page` later stays out of the script API until it is listed.
An embedded field is selected by its declared name. An unknown or repeated
name fails schema construction. Do not give one type both `Fields` and
`OmitFields`.

Selection changes declarations, not storage. A pointer still points at the
whole struct, and a struct copy keeps the hidden fields. A model's record
type takes its fields from the model; selection cannot override that. A struct
with more than one blank (`_`) padding field cannot be selected and reports an
error.

## Type aliases

Source bindings keep Go aliases, including aliases of imported and builtin
types such as `type Key = string`. In a hand-written package,
`evm.Package.Aliases` gives an existing VM type a second name. An alias adds
no runtime type, layout, allocation or conversion.

- The target must be declared in the same environment.
- Inside pointers, collections and signatures, an alias resolves to its
  target. Printed declarations show `type X = Y`.
- When packages are merged, a redundant alias of the same name is dropped and
  a conflicting exported name is an error.
- An alias supports what its target supports. Generic aliases without type
  arguments and aliases of EVM function handles are not supported.

## Time

`time.Time` in a source binding is the VM's `time.Time`. Go's private fields
are not exposed.

- A `time.Time` that crosses through `any` keeps its zone and monotonic
  reading. `==` and map keys compare the complete Go value; `Equal` compares
  instants. Formatting uses the value's offset.
- Reading a `time.Time` field takes a snapshot. A field address or a
  `*time.Time` argument is the original pointer. A time pointer created by a
  script uses Go storage. Slices and maps keep their Go backing and Go key
  equality.
- A UTC value in the nanosecond range allocates nothing. Any other Go time is
  one immutable 24-byte copy. A UTC value outside the nanosecond range uses a
  12-byte instant.
- Storing a boxed Go time into a Go field copies from that immutable payload
  without allocating, and so does storing zero. Storing a compact UTC instant
  needs one charged 24-byte temporary for the field setter.
- Nothing is cached or written back. A KVO time property reads as a UTC
  instant, and timestamp serialization normalizes to one.

## Empty structs and unnamed pointers

`struct{}` and pointers to it work in source bindings, including set maps
(`map[string]struct{}`) and slices. Unnamed pointer types share one identity
across packages by their element type: pointers to primitives, to timestamps
and to empty structs. Go collections keep their backing storage, and a
collection a script creates can be passed to Go. An anonymous struct with
fields is not supported.

## Metadata lifetime

`kvo.Bind(goType, model)` and `kvo.Project(goType, model, nil)` return a
cached plan for that Go type and model. Calling either again allocates no plan
and no representation ID. A binding and a projection of the same pair are
distinct plans. A plan with an explicit field map, `WithGenerated` or
`WithNativeSchema` is built each time it is requested: build it once and keep
it. Schemas, models and typed properties belong in setup for the same reason.

Records and collections find their metadata through descriptor tables indexed
by representation ID. A lookup is one atomic slice load and one index.
Registration, growth and publication share one locked path. An ID and the
metadata it reaches live as long as the process. Both tables index one global
ID space, so each has unused slots.

On arm64 a record entry is 48 bytes, a collection entry 64 bytes, and each
table's lock and publication header 16 bytes. A record layout publishes eight
representation entries, on first use. These figures leave out spare array
capacity, shapes and binding plans, so they are not a total. `Record`,
`ListCell` and the typed list handles are 16 bytes.

## Measurements

Apple M5 Max, Go 1.27.3, darwin/arm64. Each figure is the median of three
200 ms samples with warm metadata. The two benchmark binaries were built
first and run alternately in three rounds on an otherwise idle machine. Times
cover the benchmark's whole operation. Differences of a few percent are within
noise.

"Before" is the code without direct void calls and without the cached default
plan; "after" has both. The revisions are `4b0e318` and `cb72134` in the edb
repository, where this code was developed.

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

- Void functions took 57% less time and void methods 67% less.
- Field access, list access and record methods stayed within a few percent
  with the same allocation counts. Struct construction was 2.4% slower in this
  small sample.
- The word-only specialization earns its place: 1.992 µs against 3.300 µs for
  the same function through the ordinary `DirectFunc`.
- The one allocation left in the void-method row is the Go struct the
  benchmark builds per run, not the 256 calls.
- The `Bind` row warms one default binding and measures repeated `Bind` for
  the same type and model. It does not measure the first construction or a
  custom configuration.

Run each command on its own, with nothing else loading the machine:

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
[bindings-consolidation.tsv](../bench/bindings-consolidation.tsv). The tests
cover generated, general and reflective calls, void mutation, indirect and
deferred calls, cancellation, ownership boundaries, concurrent registration
and reuse, nominal shape identity, aggregate copies and linked metadata
snapshots.
