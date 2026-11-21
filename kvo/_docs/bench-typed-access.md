# Typed access and collection representation

Measured November 9, 2026 on Apple M5 Max, `macOS arm64`, Go 1.27.3
(implementation: EDB `8faf318`). Each figure is the median of five 150 ms
samples; [raw output](../typed-access-bench.txt) retains every result. All
variants report **0 B/op and 0 allocs/op** once metadata is initialized.

## Scalar property representation

Each benchmark operation reads two integer properties and subtracts them across
256 distinct source records using shared runtime property codes. The raw
baseline is `GetValue(code).AsInt()`, matching the older runtime-code API.

| Storage | Raw `Value` (ns) | Descriptor `Get` (ns) | Typed code `kvo.Get` (ns) | Typed code `property.Get` (ns) |
| --- | ---: | ---: | ---: | ---: |
| Fast2 | 11.28 | 15.75 | 11.65 | 11.10 |
| Compact2 | 59.50 | 62.71 | 59.44 | 59.19 |
| MessagePack | 378.50 | 383.20 | 383.50 | 382.20 |
| Generated | 5.60 | 9.60 | 6.67 | 5.64 |
| Reflected | 16.92 | 20.66 | 17.85 | 16.77 |
| Memory | 5.54 | 9.30 | 6.36 | 5.60 |

The chosen scalar API uses an 8-byte typed property code with direct Go
conversions:

- `property.Get(record)` matches the raw path closely.
- `kvo.Get(property)` adds at most ~1 ns across two accesses on the cheapest
  decoded representations.
- The callback-based descriptor costs roughly 4 ns more on those paths, so it
  is reserved for custom conversions and object views rather than used as the
  default scalar carrier.
- Value conversion uses no reflection; native struct field access still uses
  whichever host binding (generated or reflected) is attached.

## Collection handle versus initialized cursor

`Get` returns a 16-byte `List[T]` or `RecordList` wrapping a tagged
`values.Value`. The control alternative returns the original 312-byte
initialized scalar cursor. Neither allocates per value: the cell points
directly at encoded bytes or an existing native collection header, sharing
static element metadata registered once per property and layout (outside
steady-state timing).

The handle-only columns assign the returned value to a global sink. The 16-byte
view defers initializing traversal state until you iterate, whereas the larger
cursor initializes it up front—an intentional API difference, not just copying
16 bytes instead of 312.

Three-string lists:

| Storage | Return cell (ns) | Return large cursor (ns) | Traverse raw (ns) | Traverse large typed cursor (ns) | Cell + typed cursor (ns) | Cell + `range` (ns) |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Fast2 | 10.50 | 59.50 | 58.30 | 83.24 | 86.72 | 90.09 |
| Compact2 | 13.31 | 66.97 | 58.59 | 85.45 | 89.85 | 93.44 |
| MessagePack | 29.70 | 67.53 | 100.40 | 129.40 | 131.40 | 135.20 |
| Native | 11.41 | 43.74 | 64.81 | 93.50 | 97.09 | 101.60 |
| Memory | 7.58 | 56.20 | 55.49 | 80.53 | 84.64 | 89.75 |

The 16-byte view is the right default: returning or storing it is much cheaper,
while full manual traversal costs only 2–4 ns more than the large typed cursor.
Typed traversal is still ~25–40 ns slower than the raw `Value` iterator on
short lists because of typed conversion callbacks and cursor bookkeeping, so
`ListValue().Iter()` stays available for loops where those nanoseconds matter.
All compared traversal paths allocate zero; the live customer benchmark
measures the end-to-end application effect. Raw output also includes empty and
32-element lists.

API notes:

- `List.Len()` returns total elements; `Cursor.Len()` returns remaining
  elements.
- `Cursor()` keeps traversal state on the stack. `Iter()` is single-pass and
  resumes after a `break` if you range over the same sequence again; call
  `Iter()` again for an independent pass.
- `Slice()` allocates a new slice whose strings and child records still borrow
  source storage. Immediate `range` is allocation-free; an escaping iterator
  closure may allocate like any Go closure.

## Reproduce

```sh
go test ./kvo -run '^TestTypedCollectionSize$' -v
go test ./kvo -run '^$' -bench 'BenchmarkTyped(Property|List)$' \
  -benchtime=150ms -count=5
```

Functional tests cover MessagePack, Fast2, Compact2, native structs, and
decoded memory; typed editing; wrong-model rejection; sequence resume and
exhaustion; zero views; and concurrent cold metadata registration. Full EDB
tests, `-race`, and `checkptr=1` pass (`checkptr=2` forces extra heap escapes
and trips exact-allocation assertions, with no invalid-pointer diagnostics).
