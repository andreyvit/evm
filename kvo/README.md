# KVO

KVO gives you typed data records with a single API across every storage format.
You declare a model once as a list of typed properties with stable numeric
codes. After that, reading and editing a record works the same way whether its
data lives in a Go struct, an encoded binary row, or decoded memory—and reading
a single property from a row decodes only that property, not the whole row.

KVO separates three concerns: what a record means, how it is stored, and who
reads it. Go code, [EVM](../README.md) scripts, and [GoX](../gox/README.md)
templates all work on the same records with the same types. Because `kvo` never
imports the VM, you can use the data model on its own.

`kvo` does not include a database. A database such as
[edb](https://github.com/andreyvit/edb) stores the rows, hands out records, and
decides when edits are saved.

## A model in a page

Each property has a stable numeric code, a name, and a type. Declaring it
returns a typed reference so every read and write uses the property's Go type:

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

A `kvo.Record` is a 16-byte handle that views data in place:

```go
fromStruct := Customers.Record(reflect.ValueOf(&customer))
fromRow := Customers.Encoded(row2.Open(rowBytes))

earned := fromRow.Get(Customer_Earned) // int64, read directly from rowBytes

edit := fromRow.Edit()
edit.Set(Customer_Earned, earned+100)
updated := edit.Record() // builds a new row; rowBytes is unchanged
```

Editing a struct record writes directly to the struct; editing an encoded row
builds a new row. Code that takes a `kvo.Record` does not need to know which
representation it received.

For a complete runnable walkthrough—one model over a Go struct, a Fast2 row,
and a MessagePack row, read and edited from both Go and a script—see
[examples/kvo-basic](../examples/kvo-basic) and the
[root README](../README.md#3-kvo-read-and-edit-typed-records).

## Concepts

| Term | Meaning |
| --- | --- |
| **Schema** | Holds properties and models. |
| **Property** | A stable numeric code, a qualified name such as `customer:points_earned`, and a type. The code is its identity in every stored row—never renumber or reuse one. |
| **Model** | The list of properties that belong to one record type. |
| **Record** | `kvo.Record`, a 16-byte handle to one value of a model. It borrows the data it views. |
| **Editor** | `record.Edit()`: typed writes to a struct, or a batch of changes that encodes a new row. |
| **Binding** | Ties a model to a Go struct type and constructs records from structs and rows. |

Always declare schemas, properties, models, and bindings at package scope or
during startup. Their metadata lives for the lifetime of the process, so
building them per request leaks memory.

## Representations

| Representation | Package | Status |
| --- | --- | --- |
| Go struct | `kvo` | Read and written in place through `nativebind` field access. |
| Fast2 row | `kvo/codec/row2` | Current. Full-width scalar slots and a key directory; favors access speed. |
| Compact2 row | `kvo/codec/row2` | Current. Short integers and a trailing catalog; favors compact size. |
| MessagePack row | `kvo/codec/msgpack` | Supported legacy format. Views scan the encoded map; edits rewrite the changed path and preserve unknown fields. |
| Fast1 row | `kvo/codec/fast1` | Supported legacy format with its own API. |
| Decoded memory | `kvo` | `record.Fastest()` on a row whose model has no Go struct: indexed, owned values. |

Fast2 and Compact2 live in the same package because they share scalar semantics
and container framing. You can even mix them inside one row:
`b.Prop(child, kvo.Compact)` stores a child record compactly inside a Fast2
row. Layout is purely storage metadata and never changes a property's type.

Rows do not migrate themselves. Any reader that knows the model can read all of
these formats; which format a table writes is configured by the database.

## Lifetimes

A record borrows its underlying bytes, so a row read inside a database
transaction is valid only for that transaction:

- Call `record.Own()` to keep the data after its source is gone.
- Call `record.Clone()` to make an independent copy you can safely edit.

EVM scripts get lifetime management automatically—the compiler inserts copies
whenever a script keeps a record past its scope. Native Go code must call
`Own()` or `Clone()` explicitly.

## Packages

| Package | Responsibility |
| --- | --- |
| `kvo` | Models, typed properties, record and collection views, editors, Go bindings, and their generator. |
| `kvo/codec/row2` | Fast2 and Compact2 wire formats, views, writers, and editors. |
| `kvo/codec/msgpack` | MessagePack views, copy-on-write edits, and struct encoder/decoder. |
| `kvo/codec/fast1` | Original word-map encoding with its own views and edits. |
| `values` | Shared runtime values, types, shapes, and scalar conversions. |
| `nativebind` | Reflected and generated Go struct field access. |

## Further reading

- [Reading and editing records](_docs/access.md): typed properties, maps and
  child editors, borrowed lists, decoding once, MessagePack edits, JSON,
  lifetimes, and choosing an access strategy.
- [Records in scripts](../_docs/embedding.md#records-in-scripts) and
  [explicit record methods](../evmbind/README.md#explicit-record-methods).
- Benchmarks: [typed access](_docs/bench-typed-access.md),
  [access strategies](_docs/bench-record-access.md), and
  [list traversal](../_docs/list-cells.md).
