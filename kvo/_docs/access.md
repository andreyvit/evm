# Reading and editing records

A `kvo.Record` reads and edits one value of a model. The data behind it is a
Fast2, Compact2 or MessagePack row, a Go struct, or decoded memory, and the
same calls work on all five. How you access a record is a choice separate from
how it is stored: no access strategy changes the stored format.

This guide goes from the typed API that ordinary code uses, through decoding
strategies and lifetimes, down to the codec level. The
[KVO overview](../README.md) introduces the terms. [Using edb](#using-edb)
collects what applies when a database hands out the records.

## Conventions

**Errors.** Operations panic on an error. The `Try` forms return it:
`TrySet`, `TrySetValue`, `TrySetAs`, `TryToJSON`, `TryEncode`, `TryDecode`,
`TryDecodeProperty`, `TryValidate`, `TryGenerate`. The codec packages follow
suit: `row2.Open` returns a view, `row2.TryOpen` a view and an error.

**Setup.** Build schemas, properties, models and bindings at package scope or
during initialization. `kvo.Bind(goType, model)` and
`kvo.Project(goType, model, nil)` return a cached plan, and a binding and a
projection of one pair are distinct plans. A plan with a custom field map,
`WithGenerated` or `WithNativeSchema` is separate: build it once and reuse it.
Representation IDs and their metadata are never reclaimed, so a binding built
per row or per request leaks.

## Typed properties

Declare a scalar property with `NewSignedProp[T]`, `NewUnsignedProp[T]`,
`NewStringProp[T]`, `NewBoolProp[T]`, `NewFloat32Prop[T]`, `NewFloat64Prop[T]`
or `NewBytesProp[T]`. The matching `Bind…Prop` function gives typed access to
a property declared elsewhere. Registration checks the Go type. Reads and
writes convert directly, without reflection, and a named integer or string
type keeps its Go type.

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

A scalar reference holds only its eight-byte code. `PropRef[T]` is the shared
descriptor behind custom conversions and record views:

- `NewScalarProp` and `BindProp` take a `values.Codec[T]` for a custom scalar.
- `NewRecordProp` returns the child as a `Record`, without decoding it.
- `NewRecordListProp` returns a `RecordList` whose items are `Record` values.
  It needs no Go type for the element.

Only the conversion is free of reflection by construction. Access to a Go
struct follows the binding attached to the record, generated or reflective.

## Lists

`List[T]` and `RecordList` each hold one tagged `values.Value`: a pointer the
garbage collector sees, a representation ID and a 32-bit byte length. For a Go
slice the pointer is the existing slice header. Nothing is boxed per value.
Metadata is registered once per property and layout, on first use. After
that, access and an immediate `range` allocate nothing for scalar and record
elements.

| Call | Result |
| --- | --- |
| `Len()` | Number of elements. |
| `Iter()` | A single-pass sequence for `range`. Breaking stops it; ranging over the same sequence again resumes. Call `Iter()` again for an independent pass. |
| `Cursor()` | Traversal state on the stack: `Next()` advances, `Len()` reports what remains. |
| `Slice()` | A new slice. Strings, byte spans and child records still borrow the source. |
| `OwnedSlice()` | A new slice with strings and byte spans copied. On a `RecordList` it applies `Own()` to each record; a Go struct keeps its identity. |
| `RecordList.CloneSlice()` | Independent records. |

`Slice`, `OwnedSlice` and `CloneSlice` also exist on a cursor, where they copy
the remaining elements and leave the cursor in place. An iterator closure that
escapes can allocate like any Go closure. The source of the data must outlive
every borrowed view, element and iterator; see
[lifetimes](#lifetimes-and-compatibility).

Assign a list with `tags.SetSlice(edit, items)` or
`edit.Set(tags, tags.FromSlice(items))`. Encoding and editing allocate.

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

A map property takes a key codec and a value codec, as a list takes an element
codec. `kvo.Map[K, V]` is a 16-byte borrowed view. `Get(key)` returns zero for
an absent key; `Lookup(key)` also reports presence. `Keys()` walks the keys
without decoding values.

- **No conversion.** A read borrows the stored bytes. A MessagePack map is
  never transcoded to be read.
- **Allocation.** Lookup and iteration allocate nothing, once warm, on a Go
  map of scalars and on an encoded map of scalars or records. A Go map of
  struct values must copy the selected entry into addressable storage and can
  allocate. A custom record codec can require decoding.
- **Other value kinds.** `NewRecordMapProp` returns `Map[K, Record]`.
  `NewNestedMapCodec` combines map codecs, and its
  `.Prop(schema, code, name)` declares a map of maps. `NewJSONMapProp` returns
  `Map[K, any]` and decodes only the value asked for; an object or array
  value allocates, and `Len` and `Keys` decode none.
- **MessagePack.** Key adapters normalize older key encodings before a lookup
  and apply Go's rule that the last duplicate key wins. A lookup scans the map
  once, so a large map read at random belongs in Fast2. Iteration handles
  duplicate keys without allocating, at quadratic cost.

Zero and absence in an editor:

- A typed map keeps zero entries, nested maps included. `Set(key, zero)`
  keeps the entry and `Delete(key)` removes it.
- On a JSON map, `SetNull(key)` stores a present null, which `Lookup` returns
  as `(nil, true)`.
- `Clear()` leaves an empty collection. `edit.Delete(prop)` removes an encoded
  property, or resets a Go field; whether a Go struct can tell zero from
  absent depends on the model and the field's type.
- `Missing()` tells a missing or nil collection from a present empty one. A Go
  struct serialized with `omitempty` can drop an empty collection. A `Clear`
  on an encoded row keeps it.

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

A child editor is attached to its parent: its changes are part of the parent's
next snapshot or save. A record property and a map entry have `Edit`, `New`
and `EditOrCreate`. A list of records has `Edit(index)` and `New()`. For a map
of maps use `kvo.EditMap(outer, key)`, `NewMap` or `EditOrCreateMap`, which
infer both key types.

Replacing or deleting a child invalidates its editors. Appending to, removing
from or clearing a list of records invalidates the editors of its elements,
because the slice behind it can move.

## How edits apply

An editor of a Go struct, or of decoded memory, writes through at once. An
editor of a MessagePack, Fast2 or Compact2 row collects changes and encodes
when asked.

- `Record`, `Snapshot` and `MessagePack` return encoded output and cache it
  until the next change.
- Reads through a list or map editor see pending changes without rebuilding
  the row. `edit.Get(collectionProp)` takes a read snapshot of that
  collection, so inside a loop that mutates, read through the collection
  editor.
- Changing a collection of a Go struct or of decoded memory can allocate.
- `Delete` removes a property even when the model keeps zero values.
- `kvo.EditEncoded(codecEditor)` wraps a `row2.Editor` the caller owns and
  writes through to it. Ordinary code uses `Record.Edit()`.

Without a typed reference, `Editor.GetValue(code)` and `SetValue(code, value)`
address one property; `SetValue` takes the compact and zero-preservation
policy from the model. `GetPathValue(path)` reads through children.

`FastSetPathValue(path, value)` takes a `[]row2.Step` whose write options are
already computed, and applies them as given, without consulting the model. EVM
computes them at link time. Prepare a step with
`kvo.WriteOptions(model.PropFlags(code))`. `TryFastSetPathValue` returns the
error, and the `WithWriter` forms reuse encoding scratch. `ValuePropRef` gives
raw access to an encoded aggregate; prefer a typed map.

Measurements: [typed access](bench-typed-access.md) and
[batched writes](bench-record-access.md#batched-writes-and-borrowed-dictionaries).

## Access by code

Code that works from numeric codes, such as analytics over a schema built at
run time, reads without typed references:

```go
if r.Missing() { return nil }
name := r.GetOwnedString(nameCode).AsString()
balance := r.Child(row2.Step{Key: profileCode}).GetValue(balanceCode).AsUint()
```

`GetValue` returns a `values.Value`, the carrier EVM uses. Integers are
normalized, a float32 holds float32 bits, and a string is a pointer and a
length.

- `GetValue(code).AsString()` borrows from the record. To keep a string, use
  `GetOwnedString(code).AsString()`: it copies out of an encoded row, and
  returns the string as it is from a Go struct or owned memory.
- `Child` borrows the child's bytes. Use it to reach a few properties below a
  child record.
- `GetValue` on a whole child, map or list returns a byte span holding a row2
  container; the container's trailer says whether it is Fast2 or Compact2. A
  MessagePack row transcodes that one aggregate. A Go struct materializes it.
  Calling it again repeats the work and can allocate, so prefer the typed list
  and map views.

Reading a scalar from a MessagePack row builds no Go struct, map, slice or
string. MessagePack is addressed by scanning: a read walks the encoded map by
the `msgpack` names of the Go fields and skips the values before the one it
wants. Fast2 and Compact2 address a field directly.

`ListValue` reads a list of scalars or strings without building a slice:

```go
it := customer.ListValue(tagsKey).Iter()
for {
    tag, ok := it.Next()
    if !ok { break }
    if tag.AsString() == wanted { /* match */ }
}
```

`ListValue.Len`, `At` and `Iter` work on every representation. Iterate when
reading in order: `At` on MessagePack or Compact2 can scan the elements before
it. Elements with a custom codec can materialize, so this API does not promise
zero allocations for every decoder.

## Embedded structs

`kvo:",inline"` on a struct-valued field promotes its properties into the
containing model. Add the same declarations to the model with
`b.Inline(childModel)`. Codes, local and qualified names and storage policies
carry over, and no container property exists:

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
include the embedded struct's accessors. A MessagePack read stays borrowed
when the embedded struct shares the enclosing wire map. A value field stored
under its own MessagePack key goes through the decoder instead. Inlining a
pointer is rejected. A duplicate code or name panics at model registration.

Without the `kvo` inline tag, an embedded struct value is a child record,
`msgpack:",inline"` or not. The child borrows the enclosing map, reads and
edits resolve its flattened field names, and untouched parent fields are
preserved. Embedded pointers, conflicting names and payloads that rely on
nested embedding aliases keep the standard decoder's behavior.

A Go type with a custom MessagePack scalar encoding can be read directly:
call `kvo.RegisterMessagePackScalar` during initialization, before binding the
type. The decoder it registers must accept every wire form the data has ever
used, nil included. The access plan caches it, so a read performs no registry
lookup. Edits still use the Go encoder. A custom codec that is not registered
is read through the ordinary decoder.

## Lists in scripts

A script writes `for _, tag := range customer.tags`; there is no `.Iter()`.
Go needs `.Iter()` only because its `range` cannot iterate an arbitrary
struct.

When a script only reads a list of scalars or strings locally, through
`range`, indexing, `len` or `cap`, EVM leaves the list in the row. The script
holds the same 16-byte list cell as `List[T]`. The VM owns one reusable
iterator workspace per indexing instruction, and a list expression resolves
its property once. A range is linear and allocates nothing per record. An
adjacent `len(customer.tags)` or index read with a single consumer fuses into
one instruction.

The compiler does this only when it knows every consumer of the list. A list
that is modified, a slice or string that escapes, and a call or write while a
borrowed element is live all get an ordinary materialized slice. The ownership
copies at scope boundaries still happen. The VM checks the element type when
it accepts a cell.

[Paired EVM and Go measurements](../../_docs/list-cells.md).

## Decoding once

Start with direct access. Decode when one record is read many times.

```go
hot := r.Fastest() // a Go struct if bound; indexed decoded memory otherwise
for range passes {
    balance := hot.Child(row2.Step{Key: profileCode}).GetValue(balanceCode).AsUint()
    _ = balance
}
```

`Fastest` decodes the whole record and returns it as a `Record`, so the code
that reads it does not change. Keep the result: calling `r.Fastest()` on the
encoded record again decodes again. On a record already decoded, and on a
missing record, it does nothing. It needs no binding.

With a binding, decoding uses the registered Go type and the MessagePack
decoder or the row2 binding, generated if one is attached. The result is a
complete Go struct: it includes MessagePack fields the model does not expose,
and its cost is that of the whole struct however few properties are read.

```go
hot := r.Fast() // keep a Fast2 view; otherwise the same as Fastest
```

`Fast` returns a Fast2 record unchanged, with its model and binding, and
decodes anything else as `Fastest` does. A Fast2 view still borrows its bytes.
Use `Fastest` when the result must be owned.

A record outside a database gets its binding when the bytes are opened:

```go
b := kvo.Bind(reflect.TypeFor[Account](), accountModel)
a := b.MessagePack(messagePackBytes)
z := b.Encoded(row2.Trusted(row2Bytes))
_ = a.Fastest()
_ = z.Fastest()
```

- With a projection: `kvo.Project(...).MessagePack(bytes)`.
- With a model and no Go struct: `kvo.Encoded(view).WithModel(model).Fastest()`
  owns the bytes, normalizes scalar words and indexes child records eagerly.
  `WithModel` attaches the model, not a Go binding.
- With neither: `kvo.Encoded(view).Fastest()` owns a copy of the bytes. Read a
  field with an explicit encoding, `GetAs(code, row2.Signed)` or `Word`,
  `Float32`, `Data`. `Child` navigates lazily.

In decoded memory a collection stays encoded, because `Get` hands out
collection bytes.

## Choosing a strategy

Measure the complete operation, decoding included.

| Stored as | Direct access | Decoding first |
| --- | --- | --- |
| MessagePack | Good for sparse reads and edits. | Wins when many properties are read repeatedly, and sometimes on the first full pass. |
| Fast2 | Scalar reads are already cheap. | Generated field access and indexed memory are faster per read, and need enough reads to repay the decode. |
| Compact2 | Good for a sparse single pass. | Repays over repeated passes; generated fields lower the threshold a lot. `Fast` decodes here, so keep the original record for sparse access. |

Decoding does not cache collections: `Get` on a Go struct still builds the
collection value each time. Keep the value if you traverse it repeatedly.

`Fastest` is a request to decode fully. It does not predict a workload or pick
the faster path. [Measurements and commands](bench-record-access.md).

## Go structs shared with scripts

`nativebind` implements field access to Go structs. `evmbind` and the record
codec generator both emit typed getters and setters through it, and both fall
back to the same reflective implementation. A model's property codes are
mapped to these operations once, at registration.

```go
binding := kvo.Bind(reflect.TypeFor[Account](), accountModel).
    WithGenerated(&AccountBindings, nativebind.StaticOnly)
// When this type also has an evmbind schema:
binding = binding.WithNativeSchema(hostSchema)
hot := binding.Encoded(view).Fastest()
account := hot.Native().(*Account) // the decoded struct itself
vmArg, err := hostSchema.NativePointer(account)
```

`Projection.WithNativeSchema` works with selected fields and custom property
maps. `hot.NativeType()` is the `values.Type` that the schema exposes. A
script's field access and the host's methods then act on one Go struct, and
KVO sees every change. Methods are called through `evmbind`'s wrappers; KVO
has no method calls of its own. Go code calls methods on the pointer that
`Native()` returns.

- A pointer that comes from the host keeps its identity through field
  addresses, aliases and native calls in a script. A struct passed by value is
  a copy.
- A registered struct or scalar that a script allocates uses Go storage.
  Other script pointers live in VM cells and reach Go through a temporary
  that is copied back after the call.
- `evmbind`'s limits on field types still apply. A KVO binding does not expose
  a field to scripts by itself.
- Generated field accessors must be attached with `WithGenerated`; generating
  the codec functions is not enough. Regenerate when declarations change:
  `Automatic` falls back to reflection for stale code and `StaticOnly`
  rejects it.

## MessagePack edits

An edit to a MessagePack row copies and rewrites the encoded maps along the
changed path. It encodes the new value and keeps the bytes of every other
field: fields the model or the Go type does not know, and unknown extension
values. The input bytes and earlier snapshots stay as they were.

```go
e := a.Edit()
e.Edit(profileProp).Set(balanceProp, uint64(900))
edited := e.MessagePack()
```

`e.MessagePack()` returns the edited row. `e.Snapshot()` returns the modeled
properties in row2 format, which is not a lossless copy of the MessagePack
payload. Replacing a whole child whose Go type has fields outside the model is
rejected: edit its properties.

## JSON

`r.ToJSON(model, kvo.JSONOptions{})` returns detached JSON-compatible data: a
`map[string]any` keyed by local property names. A Go struct and a MessagePack
row are walked directly, without converting to another encoding first.

- A present empty list or map stays empty. A nil list, map or child is null,
  and so is a nil entry in a list of records.
- `TryToJSON` returns conversion errors. `JSONOptions.Charge` lets a host
  meter the result.
- Not everything that converts to JSON encodes as row2: `Encode` rejects a nil
  entry in a list of records.

## Lifetimes and compatibility

A record views its data. Decide at each boundary what must survive it:

| Need | Operation |
| --- | --- |
| The latest state, in a later transaction | Keep the key and load the record again. |
| The data, after its source is gone | `r.Own()` |
| An independent copy to modify | `r.Clone()` |

```go
saved := customer.Own()       // keeps the encoded bytes; a Go struct may be shared
alternate := customer.Clone() // independent, even if customer is a Go struct
edit := alternate.Edit()
edit.Set(pointsRedeemed, edit.Get(pointsRedeemed)+proposedRedemption)
balance := edit.Get(pointsEarned) - edit.Get(pointsRedeemed)
// customer and saved are unchanged, and nothing has been saved anywhere.
```

**Own.** `Own()` is idempotent, and returns a record that is already owned,
or backed by a Go struct, as it is. Own the record before keeping one of its
strings, lists or maps past the source.

**Clone.** `Clone()` always detaches. A clone of an encoded record keeps its
format, its binding and its unknown fields. A clone of a Go struct keeps its
Go type and copies what it reaches: pointers, maps, interfaces, slices with
their capacity, strings, and fields outside the model. Sharing inside the
struct is preserved: a pointer or map reached twice is copied once, as are
identical slice views, while distinct overlapping slice views are copied
separately. A `time.Time` keeps its location. A non-nil function, channel or
unsafe pointer cannot be copied and panics. Cloning a struct uses reflection;
reads are unaffected.

**Views.** Neither call adds a lifetime check to reads. A view borrows bytes
that must not change: treat a returned byte slice as read-only.

- Decoding an encoded record with `Fastest` yields an owned Go struct or
  decoded memory, which outlives the source.
- A record that already views a Go struct stays a view of it; `Fastest` does
  not clone the struct.
- Editing a Go struct or decoded memory changes it in place. Editing an
  encoded view copies bytes. `Snapshot` freezes decoded memory into
  independent row2 bytes.

**Go types with their own encoding.** A type with custom MessagePack, binary
or text methods, embedded fields, struct-as-array layout, aliases, interning
or duplicate field names is read with the decoder's semantics.

- A custom child, or a map with custom key codecs, is decoded when it is read
  or edited. A custom root is decoded when opened.
- JSON and time values use their property codecs. Some `Has` checks, on fixed
  arrays and JSON values for example, decode the property to give Go's
  zero-value answer.
- A recognized custom encoding is never reinterpreted as an ordinary map.
- A handler installed with `msgpack.Register` cannot be found by reflection.
  If it changes the wire layout, put codec methods on a named Go type, or use
  `Projection.Record` over a struct that the handler decoded.

## Names, tags and timestamps

**Names.** A property is declared once, with a stable code and a type. Its
qualified name, such as `customer:full_name`, is unique in the schema. A model
exposes the local name `full_name`, and lookups also accept
`customer:full_name` and `customer_full_name`. A script selects
`row.full_name` or `row.customer_full_name` on a record of a known model; both
compile to the code. A script value of type `any` has no record selectors.

A duplicate code, a duplicate local name or a conflicting alias panics when
the schema is built. A code is persistent: never renumber one or reuse a
retired one. A property can belong to several models when its meaning and type
are the same.

**Tags.** Register a Go struct with the entity type and tag its fields
`kvo:"full_name"` or `kvo:"customer:full_name"`; `kvo:"-"` excludes a field.
An `edb` tag serves when there is no `kvo` tag. Names and tags are resolved
when the binding is built, not on access. MessagePack wire names come from the
separate `msgpack` tags.

`model.RegisterNativeType(goType)` attaches a Go struct after the model is
declared, by its `kvo` tags. Pass a field map when tags are impractical:

```go
model.RegisterNativeType(reflect.TypeFor[Row](), map[kvo.PropCode]string{
    kvo.CodeOf(Row_Name): "Title",
    kvo.CodeOf(Row_Tags): "Tags",
})
```

The map names an exported field for every property of the model. Other fields
stay unexposed. Leave such a struct out of `NewEntityType`, which requires
tags. Call `RegisterNativeType` during single-threaded setup, before building
bindings; registering a type twice panics.

**Timestamps.** `kvo.NewTimeProp(schema, code, name)` declares a property of
logical type `kvo.TTimestamp` that reads as a `time.Time`. A list of times
uses `values.TimeCodec()`.

- Fast2 and Compact2 store a timestamp as integer Unix nanoseconds plus one;
  zero is Go's zero time. A date outside the signed nanosecond range, and the
  reserved −1 ns instant, use two integers: 64-bit Unix seconds and 32-bit
  nanoseconds.
- Reads keep nanoseconds and the full date range, normalize to UTC and carry
  no monotonic reading.
- MessagePack timestamps use MessagePack's 32, 64 and 96-bit forms and are
  read directly. RFC 3339 strings in row2 rows are readable; writes use
  integers.
- `kvo.TTime`, declared with `NewLegacyTimeProp`, keeps the offset-microsecond
  encoding for Fast1 data.
- Timestamp access allocates nothing, wide dates included. Building a wide
  date from a `time.Time` needs one 12-byte payload.
- JSON output is RFC 3339, a presentation format. Scripts see `time.Time`
  with `IsZero`, `Before`, `After`, `Equal` and the Unix accessors.

## Codec level

Raw values are built with `values.Int`, `values.Uint`, `values.Float32`,
`values.Float`, `values.String` and `values.Bytes`. A `values.Bytes` span is a
pointer and a byte length; it is not the mutable slice representation EVM
uses. The declared type gives a value its meaning.

A modeled `Record` chooses the wire conversion. A raw `row2.View` or `Writer`
needs the encoding stated:

```go
n := view.Get(code, row2.Signed).AsInt()
writer.Put(code, "", values.Int(n), row2.Signed)
```

`row2.Field` holds raw codec metadata for traversal without a schema.
`RawField`, `RawString`, `RawAt` and `Iterator.Next` return fields.
`Decode(encoding)` turns one into a value, `Writer.PutRaw` copies an unknown
field without reinterpreting it, and `Field.Container` opens an encoded child.
No wire marker and no deferred decoding reaches a `values.Value`.

Generated record bindings carry the ABI tag `record3`. Code generated for
another tag is stale. The tag covers Go APIs, not the stored formats.

## Using edb

`kvo` has no database. The points below describe
[edb](https://github.com/andreyvit/edb), which stores rows and hands out
records; its own documentation is the reference for these calls.

**Reading.** `tx.GetRecord(table, key)` and a cursor's `Record()` return
records with the table's binding attached, for every storage format, including
a scan that meets both MessagePack and row2 rows. Pass the model after the
name in `edb.DefineTable`. MessagePack is the default storage and
`b.SetRecordStorage` selects Fast2 or Compact2. `Record` and `GetRecord`
return only the record; their `Try` forms also return `ValueMeta` and an
error.

**Scans.** An index scan reuses its tuple and data-lookup cursors, so
advancing and reading can be allocation-free once they exist. Opening a
transaction and building its cursors has a fixed cost. For a join, pass the
cursor's `RawKey()` to `GetRecordByKeyRaw`. A host that already holds typed
key storage can call `GetRecordByKeyVal` or `TryGetRecordByKeyVal` with a
reflected reference to it, which avoids boxing a key per lookup.

**Lifetimes.** A record read in a transaction borrows the transaction's bytes.
To read the latest state later, keep the primary key or an `edb.Ref` and load
again.

**Keys.** The Go row APIs fill in the Go primary key. `Fastest` cannot restore
a key that the stored payload omits: keep the lookup key or take it from the
cursor.

**Shared Go structs.** Call `b.SetNativeSchema(hostSchema)` in the table
definition, and `GetRecord` and cursor records carry that binding.

**Editing a stored row.**

```go
// Inside a write transaction:
edit := tx.OpenRecordEdit(accounts, key)
if edit == nil { return nil }
edit.Editor().Edit(profileProp).Set(balanceProp, uint64(900))
tx.SaveRecord(edit)
```

`SaveRecord` keeps the row's format, checks that the row has not changed since
it was opened, updates indexes and revision metadata, and runs notifications.
An edit never writes into Bolt's memory. A property-based `SetIndexer` and
record notifications read the edited MessagePack directly; a notification that
asks for a Go struct decodes at that point. `NewRecordEdit` builds a new
MessagePack row from a Go value, since there is no payload to preserve. A
persisted edit belongs to `RecordEdit`: `r.Fastest().Edit()` alone saves
nothing. Schema migrators use the same editor over encoded records or decoded
structs, so a migration does not need full decoding.
