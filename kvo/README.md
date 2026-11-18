# KVO

KVO is typed data records. You declare a model once, as a list of typed
properties with stable numeric codes. A record of that model is then read and
edited through one API, whether its data is a Go struct, an encoded row or
decoded memory. Reading a property from a row decodes that property, not the
row.

KVO keeps three things apart: what a record means, how it is stored, and who
reads it. Go code, [EVM](../README.md) scripts and [GoX](../gox/README.md)
templates read the same records with the same types. `kvo` does not import the
VM, so an application can use the data model alone.

This package has no database. A database such as
[edb](https://github.com/andreyvit/edb) stores the rows, hands out records and
decides when an edit is saved.

## A model in a page

A property has a code, a name and a type. Declaring it returns a typed
reference; reads and writes through the reference use the property's Go type.

```go
var (
	Schema       = kvo.NewSchema()
	CustomerType = kvo.NewEntityType(Schema, "customer", reflect.TypeFor[Customer]())

	Customer_Name   = kvo.NewStringProp[string](Schema, 1, "customer:name", kvo.TMachineString)
	Customer_Earned = kvo.NewSignedProp[int64](Schema, 3, "customer:points_earned", kvo.TInt64)

	CustomerModel = kvo.NewModel(Schema, CustomerType, func(b *kvo.ModelBuilder) {
		b.Prop(Customer_Name)
		b.Prop(Customer_Earned)
	})

	Customers = kvo.Bind(reflect.TypeFor[Customer](), CustomerModel)
)
```

A record is a 16-byte handle that views data in place:

```go
fromStruct := Customers.Record(reflect.ValueOf(&customer))
fromRow := Customers.Encoded(row2.Open(rowBytes))

earned := fromRow.Get(Customer_Earned) // int64, read from the row's bytes

edit := fromRow.Edit()
edit.Set(Customer_Earned, earned+100)
updated := edit.Record() // a new row; rowBytes is unchanged
```

Editing a struct record writes to the struct. Editing a row builds a new row.
Code that takes a `kvo.Record` does not know which it has.

[examples/kvo-basic](../examples/kvo-basic) is the runnable version: one model
over a struct, a Fast2 row and a MessagePack row, read and edited from Go and
from a script. The [README](../README.md#kvo-read-and-edit-a-record) walks
through it and lists the API one line per capability.

## Concepts

| Term | Meaning |
| --- | --- |
| Schema | Holds properties and models. |
| Property | A stable numeric code, a name such as `customer:points_earned`, and a type. The code is its identity in every stored row: never renumber or reuse one. |
| Model | The properties of one record type. |
| Record | `kvo.Record`, a handle to one value of a model. It borrows the data it views. |
| Editor | `record.Edit()`: typed writes to a struct, or a batch of changes that encodes a new row. |
| Binding | Ties a model to a Go struct type and makes records from structs and rows. |

Declare schemas, properties, models and bindings at package scope or at
startup. Their metadata is never freed, so building one per request leaks.

## Representations

| Data | Package | Status |
| --- | --- | --- |
| Go struct | `kvo` | Read and written in place through `nativebind` field access. |
| Fast2 row | `kvo/codec/row2` | Current. Full-width scalar slots and a key directory: favors access speed. |
| Compact2 row | `kvo/codec/row2` | Current. Short integers and a trailing catalog: favors size. |
| MessagePack row | `kvo/codec/msgpack` | Supported legacy format. Views scan the encoded map; an edit rewrites the changed path and keeps unknown fields. |
| Fast1 row | `kvo/codec/fast1` | Supported legacy format with its own API. |
| Decoded memory | `kvo` | `record.Fastest()` on a row whose model has no Go struct: indexed, owned values. |

Fast2 and Compact2 share one package because they share scalar semantics and
container framing, and a Fast2 row can hold Compact2 children:
`b.Prop(child, kvo.Compact)` stores that child compactly. The layout is storage
metadata; it does not change the property's type.

Rows do not migrate themselves. A reader that knows the model reads any of
these formats; which format a table writes is the database's setting.

## Lifetimes

A record views its bytes. A row read inside a database transaction is valid
for that transaction. `record.Own()` keeps the data past its source, and
`record.Clone()` makes an independent copy to edit. A script gets this for
free: the EVM compiler copies a record that the script keeps past its scope.
Go code must do it by hand.

## Packages

| Package | Responsibility |
| --- | --- |
| `kvo` | Models, typed properties, record and collection views, editors, Go bindings and their generator |
| `kvo/codec/row2` | Fast2 and Compact2: wire format, views, writers and edits |
| `kvo/codec/msgpack` | MessagePack views, copy-on-write edits, struct encoder and decoder |
| `kvo/codec/fast1` | The original word-map encoding, with its own views and edits |
| `values` | Shared cells, types, shapes and scalar conversions |
| `nativebind` | Reflected and generated Go field access |

## More

- [Access guide](_docs/access.md): typed properties, maps and child editors,
  borrowed lists, decoding once, MessagePack edits, JSON, lifetimes, and how to
  choose an access strategy.
- [Records in scripts](../_docs/embedding.md#records-in-scripts) and
  [record methods](../evmbind/README.md#explicit-record-methods).
- Measurements: [typed access](_docs/bench-typed-access.md),
  [access strategies](_docs/bench-record-access.md),
  [list traversal](../_docs/list-cells.md).
