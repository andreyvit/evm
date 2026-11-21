# Shared values

Package `values` defines the two-word runtime representation shared by EVM, KVO,
and `nativebind`. Every runtime value travels as a `Value`:

```go
type Value struct {
    R uint64
    P unsafe.Pointer
}
```

At the Go boundary, **pack** means to convert a Go value into an EVM value.
**Unpack** means to convert it back to Go. `values.Codec[T]` pairs a Go `Type`
with `Pack func(T) Value` (Go → EVM value) and `Unpack func(Value) T` (EVM
value → Go); constructors such as `SignedCodec`, `UnsignedCodec`, `BoolCodec`,
`Float32Codec`, `Float64Codec`, `StringCodec`, `BytesCodec`, and `TimeCodec`
supply zero-allocation scalar codecs. A **slot** is a position holding a
value. In the VM’s general storage layout, a **cell** is one addressable
element of a `[]Value`. A flattened struct may occupy several cells. The
shape supplies the type and describes what each cell holds.

A `Value` does not carry its own type or presence bit—the operation that reads
it supplies its `Type`, and presence is tracked separately. A `Type` defines
nominal identity and how the value sits in registers (`Raw`, `Pointer`, or
`Pair`). A `Shape` describes its storage layout.

> **GC invariant:** `P` must always be a real GC-visible pointer or `nil`.
> Never hide pointers in `R`, and never store integer payloads in `P`.

## Payloads

Each kind of value uses `R` and `P` according to its representation:

- **Integers:** Decoded bits in `R`; narrow signed values are sign-extended.
- **Floats:** IEEE-754 bits in `R`; `float32` uses the low 32 bits.
- **Strings and immutable byte spans:** Byte length in `R`, backing pointer in
  `P`. Borrowing allocates no wrapper, so the backing storage must stay valid
  and immutable for as long as the value is live.
- **Mutable slices:** `uint32` length and capacity packed in `R`, backing pointer
  in `P`. Copying the header preserves aliases to the same backing array.
- **Functions:** Program-local reference index plus 1 in `R` (`0` means `nil`).
  Closures keep their immutable capture cells in `P`, and mutable captured
  variables share cells.
- **Time:** Unix nanoseconds (plus a reserved zero sentinel) when they fit in
  64 bits; otherwise a 12-byte integer payload storing Unix seconds and
  nanoseconds. The instant immediately before the Unix epoch uses a shared
  payload to avoid colliding with the zero sentinel.
  - `Time` always stores an instant in UTC.
  - `NativeTime` preserves a complete Go `time.Time`: canonical UTC values use
    the compact encoding above, while values with a location or monotonic clock
    use an immutable 24-byte `time.Time` copy with `R` set to `math.MaxUint64`.
    `AsTime` restores the time zone and monotonic reading from this form, and
    ownership transfer shares the immutable copy.
  - Legacy string timestamps remain readable.
- **KVO records and collections:** Compact `TypedPair` values carrying a
  physical representation ID.

### Typed pairs

A `TypedPair` packs the 4-bit tag `0110`, a 28-bit representation ID, and a
32-bit auxiliary value into `R`, with the payload pointer in `P`. For an
encoded byte span, the auxiliary word holds the byte length.

Register immutable `Shape` metadata once at startup: representation IDs live
for the lifetime of the process and are never reclaimed. Code operating on a
concrete scalar type must never interpret arbitrary numeric bits as tags—the
declared type always wins.

## Interfaces

`TypeTable` packs and unpacks interface values with their exact dynamic types.
Scalars use marker pointers in `P` so they can keep all 64 payload bits in `R`.
Because a scalar's bits can match any bit prefix, always check marker pointers
before interpreting `R` tags.

Only `(R=0, P=nil)` is a `nil` interface. Typed nils, `false`, and numeric zero
are all non-nil interfaces. Always unpack with `Unpack`; `P` is not guaranteed
to point to a heap box.

| `R` prefix | Remaining `R` payload |
| --- | --- |
| `0000` | 60-bit concrete type ID; `P` is the direct pointer or a fallback box |
| `0100` | 60-bit plain `string` length |
| `10` / `11` | 32-bit length and 30-bit spare capacity for the selected `[]byte` or `[]any` type |
| `0010` | 60-bit concrete type ID; `P` is a native Go pointer, including `nil` (pair pointer payload with `R=NativeAddress`) |
| `0001` | 16-bit exact type ID and 44-bit string length |
| `0011` | 16-bit exact type ID, 12-bit spare capacity, and 32-bit length |
| `0101` | 16-bit exact type ID, 22-bit spare capacity, and 22-bit length |
| `0110` | 28-bit representation ID and 32-bit auxiliary value |

Implicit slice tags select exact linked types, and slice capacity is
`length + spare`. Values that do not fit in these compact encodings fall back
to a `Value` box; functions also box inside interfaces. Note that JSON maps and
arrays still allocate their own backing storage even when their interface
header needs no box.

Equality and hashing respect named types, Go comparability rules, `NaN`, and
signed zero. Program-local type IDs and marker pointers belong to one linked
program—never copy them into another program without repacking.

## Aggregate storage

`NewStructShape` and `NewPointerShape` create nominal type identities. Build
and reuse them during setup; the linker snapshots their graph.

- VM structs and arrays live in flattened, GC-scanned cells. An extra trailing
  cell keeps addresses of trailing zero-sized fields inside the allocation.
- Register aggregate values point to immutable snapshots; loads and stores copy
  aggregate cells, while pointer moves preserve aliases.
- `Shape.Stored` returns a borrowed view—snapshot it before exposing it as an
  immutable value.
- `Shape.Store` copies aggregate cells and clears unused scalar halves. Never
  mutate an aggregate snapshot.
- Map keys and values must be snapshots, though any maps, slices, and pointers
  they reference keep their normal Go aliasing.

`Shape` helpers operate on VM cells, **not arbitrary native Go pointers**. For
Go memory layouts and field operations, use `nativebind` (which KVO shares).
Raw codec fields remain codec metadata until converted to a `Value` with their
declared encoding.

See [EVM](../README.md) and the [KVO access guide](../kvo/_docs/access.md).
