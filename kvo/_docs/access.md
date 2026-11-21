# Reading and editing records

A `kvo.Record` reads and edits one value of a model, whether the backing data
is a Fast2, Compact2, or MessagePack row, a Go struct, or decoded memory. The
same calls work on all five, and how you access a record is independent of how
it is stored—no access strategy ever changes the stored format.

This guide walks from the typed API that everyday code uses, through decoding
strategies and lifetimes, down to the codec layer. If you are new to KVO's
vocabulary, start with the [KVO overview](../README.md). If your records come
from a database, see [Using edb](#using-edb) at the end.

## Conventions

- **Errors:** Ordinary operations panic on error. When you want to handle
  errors explicitly, use the `Try` variants: `TrySet`, `TrySetValue`,
  `TrySetAs`, `TryToJSON`, `TryEncode`, `TryDecode`, `TryDecodeProperty`,
  `TryValidate`, and `TryGenerate`. Codec packages follow the same convention
  (`row2.Open` vs. `row2.TryOpen`).
- **Setup:** Build schemas, properties, models, and bindings at package scope
  or during startup. `kvo.Bind(goType, model)` and
  `kvo.Project(goType, model, nil)` return a cached plan (bindings and
  projections of the same pair are distinct plans). Plans with a custom field
  map, `WithGenerated`, or `WithNativeSchema` are built fresh each time, so
  create them once and reuse them. Representation IDs and their metadata are
  never reclaimed—building a binding per row or per request leaks memory.

## Typed properties

Declare scalar properties with `NewSignedProp[T]`, `NewUnsignedProp[T]`,
`NewStringProp[T]`, `NewBoolProp[T]`, `NewFloat32Prop[T]`, `NewFloat64Prop[T]`,
or `NewBytesProp[T]` (or use the matching `Bind…Prop` function for a property
declared elsewhere). Registration validates the Go type, reads and writes
convert directly without reflection, and named integer or string types keep
their Go type.

```go
points := kvo.NewSignedProp[int64](schema, 1, "customer:points", kvo.TInt64)
tags := kvo.NewListProp(schema, 2, "customer:tags",
    kvo.ListOf(kvo.TMachineString), values.StringCodec[string]())

n := r.Get(points) // int64, not values.Value
n = points.Get(r)  // the same read; the shortest path for a scalar
edit.Set(points, n+1)
tags.SetSlice(edit, []string{"vip"})
for tag := range r.Get(tags).Iter() { _ = tag }
list := r.Get(tags) // kvo.List[string], 16 bytes
_ = list.Len()
copyOfElements := list.Slice()
_ = copyOfElements
```

A scalar reference holds only its 8-byte property code. For custom conversions
and nested records, `PropRef[T]` is the shared descriptor:

- `NewScalarProp` and `BindProp` take a `values.Codec[T]` for a custom scalar.
- `NewRecordProp` returns a child `Record` without decoding it.
- `NewRecordListProp` returns a `RecordList` of `Record` values and needs no
  Go element type.

Only value conversion is reflection-free by construction; reading fields from a
Go struct still uses whatever binding is attached to the record (generated or
reflective).

## Lists

`List[T]` and `RecordList` are 16-byte handles wrapping a single tagged
`values.Value` (a GC-visible pointer, a representation ID, and a 32-bit byte
length). For a Go slice, the pointer is the existing slice header, so nothing
is boxed per value. Metadata is registered once per property and layout on
first use; after that, reading and ranging over scalar or record lists
allocates nothing.

| Call | What it does |
| --- | --- |
| `Len()` | Returns the number of elements. |
| `Iter()` | Returns a single-pass sequence for `range`. Breaking pauses it and ranging again resumes; call `Iter()` again for a fresh pass. |
| `Cursor()` | Keeps traversal state on the stack: `Next()` advances, `Len()` reports remaining elements. |
| `Slice()` | Allocates a new Go slice. Strings, byte spans, and child records still borrow the source. |
| `OwnedSlice()` | Allocates a new slice and copies strings and byte spans. On a `RecordList`, calls `Own()` on each record (Go structs keep their identity). |
| `RecordList.CloneSlice()` | Returns independent cloned records. |

`Cursor` also provides `Slice`, `OwnedSlice`, and `CloneSlice`, copying the
remaining elements without advancing the cursor. As in any Go code, an iterator
closure that escapes can allocate. Always keep the underlying source alive for
as long as any borrowed view, element, or iterator is in use (see
[lifetimes](#lifetimes-and-compatibility)).

To replace a list in an editor, use `tags.SetSlice(edit, items)` or
`edit.Set(tags, tags.FromSlice(items))` (encoding and editing allocate).

## Maps

```go
appIDs := kvo.NewMapProp(schema, 3, "integration:app_ids",
    kvo.MapOf(kvo.TUint32, kvo.TMachineString),
    values.UnsignedCodec[IntegrationID](), values.StringCodec[string]())

ids := customer.Get(appIDs) // kvo.Map[IntegrationID, string], 16 bytes
externalID, found := ids.Lookup(integrationID)
for integration, externalID := range ids.Iter() { _, _ = integration, externalID }
_ = ids.Len()
_, _ = externalID, found

edit := customer.Edit()
edit.Map(appIDs).Set(integrationID, "external-123")
edit.Map(appIDs).Delete(oldIntegrationID)
edit.List(tags).AddUnique("vip")
edit.List(tags).Remove("inactive")
```

A map property takes a key codec and a value codec, just like a list takes an
element codec. `kvo.Map[K, V]` is a 16-byte borrowed view: `Get(key)` returns
the zero value when absent, `Lookup(key)` also returns whether the key was
present, and `Keys()` walks keys without decoding values.

- **No conversion:** Reads borrow stored bytes directly; MessagePack maps are
  never transcoded just to be read.
- **Allocations:** Once warm, lookup and iteration allocate nothing on Go maps
  of scalars and on encoded maps of scalars or records. A Go map of struct
  values must copy the selected entry into addressable storage and can
  allocate; custom record codecs can also require decoding.
- **Other value kinds:** `NewRecordMapProp` returns `Map[K, Record]`.
  `NewNestedMapCodec(...).Prop(schema, code, name)` declares a map of maps.
  `NewJSONMapProp` returns `Map[K, any]` and decodes only the requested value
  (objects and arrays allocate, while `Len` and `Keys` decode nothing).
- **MessagePack:** Key adapters normalize legacy key encodings before lookup
  and follow Go's last-duplicate-key-wins rule. Lookups scan the map linearly,
  so large maps with random lookups belong in Fast2. Iteration handles
  duplicate keys without allocating, at quadratic cost.

### Zero values and absence in editors

- Typed maps preserve zero-valued entries (including nested maps):
  `Set(key, zero)` keeps the entry, while `Delete(key)` removes it.
- On a JSON map, `SetNull(key)` stores an explicit `null` that `Lookup` returns
  as `(nil, true)`.
- `Clear()` leaves an empty collection. `edit.Delete(prop)` removes an encoded
  property or resets a Go field (whether a Go struct distinguishes zero from
  absent depends on the model and field type).
- `Missing()` distinguishes a missing or `nil` collection from a present empty
  one. A Go struct serialized with `omitempty` may drop an empty collection,
  whereas `Clear()` on an encoded row keeps it.

## Child records

```go
profile := edit.EditOrCreate(profileProp) // Edit returns nil if absent; New requires absence
profile.Set(displayNameProp, "Alice")

subscription := edit.Map(subscriptionsProp).Edit(subscriptionID)
if subscription != nil { subscription.Set(enabledProp, true) }

venue := edit.List(venuesProp).Edit(0)
venue.Set(enabledProp, true)
fresh := edit.List(venuesProp).New() // appends and returns an attached record editor
fresh.Set(enabledProp, true)
```

Child editors stay attached to their parent, so their mutations are included in
the parent's next snapshot or save:

- Record properties and map entries offer `Edit`, `New`, and `EditOrCreate`.
- Lists of records offer `Edit(index)` and `New()` (which appends).
- Maps of maps use `kvo.EditMap(outer, key)`, `kvo.NewMap`, and
  `kvo.EditOrCreateMap` (inferring both key types).

Replacing or deleting a child invalidates its child editors. Appending to,
removing from, or clearing a list of records invalidates element editors too,
because the underlying slice may relocate.

## How edits apply

An editor over a Go struct or decoded memory writes through immediately. An
editor over a MessagePack, Fast2, or Compact2 row batches changes and encodes
only when you ask for output:

- `Record()`, `Snapshot()`, and `MessagePack()` return encoded output and cache
  it until the next mutation.
- Reads through a list or map editor reflect pending edits without rebuilding
  the row. Calling `edit.Get(collectionProp)` takes a read snapshot of that
  collection, so inside a mutating loop, keep and read through the collection
  editor instead.
- Mutating a collection on a Go struct or decoded memory can allocate.
- `Delete` removes a property even when the model preserves zero values.
- `kvo.EditEncoded(codecEditor)` wraps a caller-owned `row2.Editor` and writes
  through to it; everyday code should use `Record.Edit()`.

Without typed property references, use `Editor.GetValue(code)` and
`SetValue(code, value)` (which reads the compact and zero-preservation flags
from the model), or `GetPathValue(path)` to read through children.

For hot paths, `FastSetPathValue(path, value)` accepts precomputed
`[]row2.Step` write options (built with `kvo.WriteOptions(model.PropFlags(code))`,
which EVM precomputes at link time) without consulting the model.
`TryFastSetPathValue` returns errors, and the `WithWriter` variants reuse
encoding scratch. `ValuePropRef` exposes raw encoded aggregates; prefer typed
maps when you can.

See measurements in [typed access](bench-typed-access.md) and
[batched writes](bench-record-access.md#batched-writes-and-borrowed-dictionaries).

## Access by code

Code working purely from numeric property codes—such as analytics over a
runtime-constructed schema—can read records without typed references:

```go
if r.Missing() { return nil }
name := r.GetOwnedString(nameCode).AsString()
balance := r.Child(row2.Step{Key: profileCode}).GetValue(balanceCode).AsUint()
```

`GetValue` returns a `values.Value` (the same carrier EVM uses): integers are
normalized, `float32` holds 32-bit IEEE bits, and strings hold a pointer and
length.

- `GetValue(code).AsString()` borrows from the record. When you need to keep a
  string past the record's lifetime, use `GetOwnedString(code).AsString()`,
  which copies out of an encoded row and returns the string directly from a Go
  struct or owned memory.
- `Child` borrows the child's bytes—use it when reading a handful of nested
  properties.
- Calling `GetValue` on a whole child record, map, or list returns a byte span
  containing a `row2` container (its trailer identifies Fast2 vs. Compact2). On
  a MessagePack row this transcodes that aggregate, and on a Go struct it
  materializes it—repeating it repeats the work and may allocate, so prefer
  typed list and map views.

Reading a scalar from a MessagePack row builds no Go struct, map, slice, or
string: it scans the encoded map by the struct's `msgpack` field names and
skips earlier entries. Fast2 and Compact2 address fields directly.

To read a scalar or string list without building a slice, use `ListValue`:

```go
it := customer.ListValue(tagsKey).Iter()
for {
    tag, ok := it.Next()
    if !ok { break }
    if tag.AsString() == wanted { /* match */ }
}
```

`ListValue.Len`, `At`, and `Iter` work across every representation. Prefer
`Iter` for sequential reads, since `At` on MessagePack or Compact2 may scan
preceding elements. Custom element codecs can still materialize, so zero
allocations are not guaranteed for every decoder.

## Embedded structs

Putting `kvo:",inline"` on a struct-valued field promotes its properties into
the enclosing model. Mirror that in the model builder with
`b.Inline(childModel)`—property codes, local and qualified names, and storage
policies carry over, and no wrapper property is created:

```go
type CustomerState struct {
    CustomerStatValues `kvo:",inline" msgpack:",inline"`
}
// In the CustomerState model builder:
b.Inline(customerStatValuesModel)
// Read the promoted property directly:
points := state.Get(PointsEarned)
```

Field paths and offsets are resolved at registration, and generated bindings
include accessors for the embedded struct. MessagePack reads stay borrowed when
the embedded struct shares the enclosing wire map, while a value field stored
under its own MessagePack key falls back to the decoder. Inlining a pointer is
rejected, and duplicate codes or names panic at model registration.

Without `kvo:",inline"`, an embedded struct value is treated as a child record
(regardless of `msgpack:",inline"`). The child still borrows the enclosing map,
reads and edits resolve flattened field names, and untouched parent fields stay
intact. Embedded pointers, conflicting names, and nested embedding aliases keep
the standard decoder's behavior.

To read a Go type with a custom MessagePack scalar encoding directly without
falling back to the full decoder, call `kvo.RegisterMessagePackScalar` during
initialization before binding the type. Your decoder must accept every wire
form the data has ever used (including `nil`). The access plan caches it so
reads do no registry lookup, while edits still use the Go encoder.

## Lists in scripts

In an EVM script, you simply write `for _, tag := range customer.tags` (no
`.Iter()` needed—Go only needs `.Iter()` because `range` cannot iterate an
arbitrary struct).

When the compiler proves that a script only reads a scalar or string list
locally (via `range`, indexing, `len`, or `cap`), EVM leaves the list in the
row and carries the same 16-byte list cell as `List[T]`. The VM keeps a
reusable iterator workspace per indexing site and resolves the property once,
so ranging is linear with zero per-record allocations. Single-consumer `len` or
index reads fuse into one instruction.

If a list is modified, if a slice or string escapes, or if a call or write occurs
while a borrowed element is live, the compiler materializes an ordinary slice
and applies normal scope ownership copies. The VM validates the element type
when accepting a cell. See [paired EVM and Go measurements](../../_docs/list-cells.md).

## Decoding once

Start with direct access; decode once when you read the same record many times:

```go
hot := r.Fastest() // a Go struct if bound; indexed decoded memory otherwise
for range passes {
    balance := hot.Child(row2.Step{Key: profileCode}).GetValue(balanceCode).AsUint()
    _ = balance
}
```

`Fastest()` decodes the whole record and returns a `Record`, so your reading
code stays identical. Save and reuse the result—calling `r.Fastest()` again on
the encoded record decodes it again. On an already-decoded or missing record,
it is a no-op, and it works even without a Go binding.

With a binding, `Fastest()` decodes into the registered Go struct (using
generated bindings if attached). Because it builds the entire struct—including
MessagePack fields outside the model—you pay the full decode cost regardless of
how many properties you read.

```go
hot := r.Fast() // keeps a Fast2 view as-is; otherwise behaves like Fastest
```

`Fast()` leaves a Fast2 record untouched (with its model and binding) and
decodes everything else via `Fastest()`. Note that a kept Fast2 view still
borrows its bytes; use `Fastest()` whenever you need an owned result.

Outside a database, attach a binding when opening raw bytes:

```go
b := kvo.Bind(reflect.TypeFor[Account](), accountModel)
a := b.MessagePack(messagePackBytes)
z := b.Encoded(row2.Trusted(row2Bytes))
_ = a.Fastest()
_ = z.Fastest()
```

- **With a projection:** `kvo.Project(...).MessagePack(bytes)`.
- **With a model and no Go struct:** `kvo.Encoded(view).WithModel(model).Fastest()`
  owns the bytes, normalizes scalar words, and eagerly indexes child records
  (`WithModel` attaches the model, not a Go binding).
- **With neither:** `kvo.Encoded(view).Fastest()` owns a copy of the bytes.
  Read fields with an explicit encoding (`GetAs(code, row2.Signed)`, `Word`,
  `Float32`, `Data`), and navigate children lazily with `Child`.

In decoded memory, collections remain encoded because `Get` hands out
collection bytes.

## Choosing a strategy

Always measure the complete operation, including decoding:

| Stored as | Direct access | Decoding first (`Fastest` / `Fast`) |
| --- | --- | --- |
| **MessagePack** | Good for sparse reads and targeted edits. | Wins when reading many properties repeatedly, and sometimes on the first full pass. |
| **Fast2** | Scalar reads are already fast. | Generated field access and indexed memory are faster per read, once you have enough reads to repay decoding. |
| **Compact2** | Good for a sparse single pass. | Pays off over repeated passes; generated fields lower the crossover sharply. Note that `Fast()` decodes Compact2, so keep the original record for sparse reads. |

Decoding does not cache collections: calling `Get` on a Go struct builds the
collection value each time, so hold onto the returned `List` or `Map` if you
traverse it repeatedly.

`Fastest()` is an explicit request to decode, not a heuristic. See
[measurements and reproduction commands](bench-record-access.md).

## Go structs shared with scripts

`nativebind` implements Go struct field access for both `evmbind` and KVO's
record codec generator, and both fall back to the same reflection path. Property
codes map to those field operations once at registration.

```go
binding := kvo.Bind(reflect.TypeFor[Account](), accountModel).
    WithGenerated(&AccountBindings, nativebind.StaticOnly)
// When this type also has an evmbind schema:
binding = binding.WithNativeSchema(hostSchema)
hot := binding.Encoded(view).Fastest()
account := hot.Native().(*Account) // the decoded struct itself
vmArg, err := hostSchema.NativePointer(account)
```

`Projection.WithNativeSchema` works the same way for selected fields and custom
property maps, and `hot.NativeType()` returns the schema's `values.Type`.
Script field access, host methods, and KVO then all operate on the exact same
Go struct. Methods are called through `evmbind` wrappers (KVO has no method
dispatch of its own), while Go code calls methods directly on the pointer
returned by `Native()`.

- Host pointers preserve their identity across field addresses, aliases, and
  native calls in scripts; structs passed by value are copied.
- Script allocations of registered structs and scalars use Go storage; other
  script pointers live in VM cells and cross to Go through a temporary with
  copy-back.
- `evmbind`'s field-type restrictions still apply—a KVO binding does not expose
  a field to scripts on its own.
- Attach generated field accessors with `WithGenerated` (generating codec
  functions alone is not enough) and regenerate after declaration changes
  (`Automatic` falls back to reflection on stale code; `StaticOnly` rejects it).

## MessagePack edits

Editing a MessagePack row copies and rewrites only the encoded maps along the
changed path. It encodes the new value and preserves the exact bytes of every
untouched field—including fields unknown to the model or Go struct, and unknown
extension values. Input bytes and earlier snapshots are never mutated.

```go
e := a.Edit()
e.Edit(profileProp).Set(balanceProp, uint64(900))
edited := e.MessagePack()
```

`e.MessagePack()` returns the edited MessagePack row. `e.Snapshot()` encodes
only the modeled properties in `row2` format, which is *not* a lossless copy of
a MessagePack payload. Replacing an entire child whose Go type has unmodeled
fields is rejected—edit its individual properties instead.

## JSON

`r.ToJSON(model, kvo.JSONOptions{})` returns a detached `map[string]any` keyed
by local property names. Go structs and MessagePack rows are traversed directly
without converting to an intermediate format.

- Present empty lists and maps stay empty; `nil` lists, maps, children, and
  `nil` entries in a list of records become `null`.
- Use `TryToJSON` to handle conversion errors, and `JSONOptions.Charge` to
  meter allocations.
- Not every JSON-convertible record can be encoded as `row2` (for example,
  `Encode` rejects `nil` entries in a list of records).

## Lifetimes and compatibility

Because a record views its underlying data, decide at each scope boundary what
needs to outlive it:

| What you need | What to do |
| --- | --- |
| The latest state in a later transaction | Keep the key and load the record again. |
| The data after its source is gone | `r.Own()` |
| An independent copy to edit | `r.Clone()` |

```go
saved := customer.Own()       // keeps the encoded bytes; a Go struct may still be shared
alternate := customer.Clone() // independent copy, even if customer is a Go struct
edit := alternate.Edit()
edit.Set(pointsRedeemed, edit.Get(pointsRedeemed)+proposedRedemption)
balance := edit.Get(pointsEarned) - edit.Get(pointsRedeemed)
// customer and saved are unchanged, and nothing has been saved anywhere.
```

- **`Own()`** is idempotent and returns an already-owned record—or one backed
  by a Go struct—unchanged. Call `Own()` before keeping a record's strings,
  lists, or maps past its source.
- **`Clone()`** always produces an independent copy:
  - Cloning an encoded record preserves its format, binding, and unknown
    fields.
  - Cloning a Go struct preserves its Go type and deep-copies pointers, maps,
    interfaces, slices (with capacity), strings, and fields outside the model.
    Internal sharing is preserved (a pointer, map, or identical slice view
    reached twice is copied once, while distinct overlapping slice views copy
    separately), and `time.Time` keeps its location. Non-nil functions,
    channels, and `unsafe.Pointer` values cannot be cloned and panic. Cloning a
    struct uses reflection; reads are unaffected.
- **Views:** Neither `Own` nor `Clone` adds runtime lifetime checks to reads.
  Treat any borrowed byte slice as read-only.
  - `Fastest()` on an encoded record returns an owned Go struct or decoded
    memory that outlives the source, while `Fastest()` on a Go struct keeps
    viewing that struct without cloning it.
  - Editing a Go struct or decoded memory mutates it in place; editing an
    encoded view copies bytes. `Snapshot()` freezes decoded memory into
    independent `row2` bytes.

### Go types with custom encodings

Types with custom MessagePack, binary, or text marshalers—as well as embedded
fields, struct-as-array layouts, aliases, interning, or duplicate field
names—are read using the decoder's semantics:

- Custom children and maps with custom key codecs decode when read or edited; a
  custom root decodes when opened.
- JSON and time values use their property codecs. Some `Has` checks (such as
  fixed arrays and JSON values) decode the property to match Go's zero-value
  semantics.
- Recognized custom encodings are never reinterpreted as plain maps.
- Handlers registered via `msgpack.Register` cannot be discovered by
  reflection. If a handler changes the wire layout, put codec methods on a
  named Go type or wrap a decoded struct with `Projection.Record`.

## Names, tags, and timestamps

### Names

Every property is declared once with a stable numeric code and a type. Its
qualified name (such as `customer:full_name`) is unique across the schema. A
model exposes the local name (`full_name`), and lookups also accept
`customer:full_name` and `customer_full_name`. Scripts select `row.full_name`
or `row.customer_full_name` on a typed model record, compiling straight to the
property code (values of type `any` have no record selectors).

Duplicate codes, duplicate local names, and conflicting aliases panic when the
schema is built. Codes are persistent—never renumber one or reuse a retired
code. A single property may belong to multiple models when its meaning and type
are identical.

### Struct tags and explicit field maps

Register a Go struct with an entity type and tag its fields `kvo:"full_name"`
or `kvo:"customer:full_name"` (`kvo:"-"` excludes a field; `edb` tags are used
as a fallback when no `kvo` tag is present). Tags are resolved when the binding
is built, never on access. MessagePack wire names come from separate `msgpack`
tags.

You can also attach a Go struct after model declaration with
`model.RegisterNativeType(goType)` (using its `kvo` tags), or pass an explicit
field map when tags are impractical:

```go
model.RegisterNativeType(reflect.TypeFor[Row](), map[kvo.PropCode]string{
    kvo.CodeOf(Row_Name): "Title",
    kvo.CodeOf(Row_Tags): "Tags",
})
```

The map must name an exported field for every property of the model; other
fields stay unexposed. Leave un-tagged structs out of `NewEntityType` (which
requires tags). Call `RegisterNativeType` during single-threaded setup before
building bindings—registering the same type twice panics.

### Timestamps

`kvo.NewTimeProp(schema, code, name)` declares a `kvo.TTimestamp` property that
reads as `time.Time` (a list of times uses `values.TimeCodec()`):

- Fast2 and Compact2 store timestamps as integer Unix nanoseconds plus one
  (`0` represents Go's zero time). Dates outside the signed 64-bit nanosecond
  range—and the reserved `-1 ns` instant—store 64-bit Unix seconds plus 32-bit
  nanoseconds.
- Reads preserve nanosecond precision and the full date range, normalize to
  UTC, and drop monotonic readings.
- MessagePack 32-, 64-, and 96-bit timestamps are read directly. RFC 3339
  strings in `row2` rows remain readable, while writes always use integers.
- `kvo.TTime` (`NewLegacyTimeProp`) preserves the older offset-microsecond
  encoding for Fast1 data.
- Reading timestamps allocates nothing (even for wide dates); constructing a
  wide date from `time.Time` allocates one 12-byte payload.
- JSON output formats timestamps as RFC 3339. Scripts see `time.Time` with
  `IsZero`, `Before`, `After`, `Equal`, and the Unix accessors.

## Codec level

Build raw values with `values.Int`, `values.Uint`, `values.Float32`,
`values.Float`, `values.String`, and `values.Bytes` (note that `values.Bytes`
is an immutable pointer-and-length span, not EVM's mutable slice). The declared
type gives the value its meaning.

While a modeled `Record` selects wire conversions automatically, a raw
`row2.View` or `Writer` requires an explicit encoding:

```go
n := view.Get(code, row2.Signed).AsInt()
writer.Put(code, "", values.Int(n), row2.Signed)
```

For schema-less traversal, `row2.Field` holds raw codec metadata returned by
`RawField`, `RawString`, `RawAt`, and `Iterator.Next`. Convert it with
`Decode(encoding)`, copy unknown fields verbatim with `Writer.PutRaw`, or open
nested containers with `Field.Container`. Wire markers and deferred decoding
never leak into `values.Value`.

Generated record bindings carry the ABI tag `record3` (covering Go APIs, not
stored formats); bindings generated for another tag are rejected as stale.

## Using edb

`kvo` itself has no database. The notes below summarize how
[edb](https://github.com/andreyvit/edb) stores rows and hands out records;
consult edb's own docs as the reference.

- **Reading:** `tx.GetRecord(table, key)` and `cursor.Record()` return records
  with the table's binding attached across every storage format—even during a
  scan mixing MessagePack and `row2` rows. Pass the model after the table name
  in `edb.DefineTable` (MessagePack is default; `b.SetRecordStorage` chooses
  Fast2 or Compact2). The `Try` variants also return `ValueMeta` and an error.
- **Scans:** Index scans reuse their tuple and data-lookup cursors so advancing
  and reading can be allocation-free after setup (opening a transaction and its
  cursors has a fixed cost). For joins, pass `cursor.RawKey()` to
  `GetRecordByKeyRaw`, or use `GetRecordByKeyVal` / `TryGetRecordByKeyVal` with
  typed key storage to avoid boxing keys per lookup.
- **Lifetimes:** Records read inside a transaction borrow the transaction's
  bytes. Keep the primary key or an `edb.Ref` if you need to reload the latest
  state later.
- **Primary keys:** Go row APIs populate the primary key field, whereas
  `Fastest()` cannot recover a key omitted from the stored payload—keep the
  lookup key or read it from the cursor.
- **Shared Go structs:** Call `b.SetNativeSchema(hostSchema)` in the table
  definition so `GetRecord` and cursor records carry that schema binding.
- **Editing a stored row:**

```go
// Inside a write transaction:
edit := tx.OpenRecordEdit(accounts, key)
if edit == nil { return nil }
edit.Editor().Edit(profileProp).Set(balanceProp, uint64(900))
tx.SaveRecord(edit)
```

`SaveRecord` preserves the row's format, checks for concurrent modifications,
updates indexes and revision metadata, and fires notifications. Edits never
mutate Bolt's memory directly. Property-based `SetIndexer` and record
notifications read edited MessagePack directly, decoding to a Go struct only
when a notification explicitly requests one. `NewRecordEdit` builds a new
MessagePack row from a Go value when there is no prior payload to preserve.
Only a `RecordEdit` can be saved (`r.Fastest().Edit()` alone persists nothing).
Schema migrators use the same editor over encoded records or decoded structs,
avoiding full decoding during migrations.
