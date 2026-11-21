# Choosing a record access strategy

This report compares the cost of keeping records encoded versus decoding them
once, along with retention, cloning, and batched writes. For the API and
ownership contracts behind these numbers, see [Reading and editing records](access.md).

## Retention and independent copies (2026-11-10)

`Own()` retains data past its source; `Clone()` builds an independent copy you
can safely mutate. On mutable native or decoded records, those two operations
intentionally have very different costs.

Measured on the small record-handle fixture (Apple M5 Max, Go 1.27.3, one
100 ms sample per case):

| Representation | `Own` | `Clone` |
| --- | ---: | ---: |
| Fast2, borrowed | 33 ns; 176 B; 1 alloc | 33 ns; 176 B; 1 alloc |
| Compact2, borrowed | 18 ns; 80 B; 1 alloc | 18 ns; 80 B; 1 alloc |
| MessagePack, borrowed | 63 ns; 384 B; 1 alloc | 68 ns; 384 B; 1 alloc |
| Native, generated or reflective access | 1.7 ns; 0 allocs | 761–766 ns; 808 B; 25 allocs |
| Decoded memory | 1.7 ns; 0 allocs | 743 ns; 2,456 B; 18 allocs |

Already-owned encoded data also makes `Own()` a no-op, while `Clone()` copies
it again. Native cloning walks the Go data graph directly without
serialization; decoded-memory cloning snapshots and reconstructs its fields.
Both are explicit boundary costs rather than per-read costs, and zero-allocation
read and list-traversal tests remain in place.

```sh
go test ./kvo -run '^$' -bench '^BenchmarkRecordRetention$' -benchmem
```

See [lifetimes and compatibility](access.md#lifetimes-and-compatibility) before
picking a copy operation. The access-strategy tables below are separate from
cloning and retention and record the native-binding work prior to runtime-value
unification; see the
[shared-value comparison](https://github.com/andreyvit/edb/blob/main/research/shared-values/README.md)
for later before-and-after timings, string allocations, and transaction
benchmarks.

At a glance:

- Generated native access and generic decoded memory beat Fast2 on repeated
  scalar reads, once enough reads repay the initial decode. Keep Fast2 for
  short reads.
- Shared reflected native access is slower than Fast2 in this fixture even
  after decoding.
- `Fast()` keeps a Fast2 view and calls `Fastest()` on everything else;
  `Fastest()` decodes into a Go struct when bound or indexed decoded memory
  when unbound. Both keep the same `Record` API and leave the stored format
  unchanged, and neither tries to guess your workload.

## Measurement procedure

Measured 2026-11-08 on Apple M5 Max, `macOS arm64`, Go 1.27.3. Tables report
medians of five 200 ms runs with default `GOMAXPROCS` (18), single-threaded,
with no other test suite running.

```sh
go test ./kvo -run '^$' \
  -bench '^(BenchmarkDecodedAccess|BenchmarkMaterialization)$' \
  -benchmem -benchtime=200ms -count=5
```

[Raw output](https://github.com/andreyvit/edb/blob/main/research/native-bindings/benchmarks-2026-11-08.txt)
and
[machine-readable medians](https://github.com/andreyvit/edb/blob/main/research/native-bindings/medians-2026-11-08.json)
(each entry is `[ns/op, B/op, allocations/op]`) are committed alongside the
benchmark in [access_bench_test.go](../access_bench_test.go).

The synthetic `statrow.Sample(13)` record has 14 root properties: scalars, a
nested profile, timestamps, lists, and maps. One scalar pass reads seven root
scalars and two nested profile scalars. Generated and reflected strategies use
independent rows with identical values, and registration and encoding stay
outside the timer. These warm-cache measurements exclude database transactions,
I/O, primary-key decoding, index updates, and business logic—benchmark your
complete application operation as well.

## Access after materialization

Decoding is outside the timer here. All paths go through the same `kvo.Record`
API: generated and reflected native fields use `nativebind`, and generic
decoded memory has model metadata with no Go struct binding.

| Representation | One scalar (ns) | Nine scalars (ns) | Scalar edit (ns) |
| --- | ---: | ---: | ---: |
| Fast2 | 5.04 | 60.92 | 13.73 |
| Generated native | 3.90 | 44.58 | 10.63 |
| Shared reflected native | 9.30 | 104.20 | 23.41 |
| Generic decoded memory | 3.28 | 45.34 | 12.51 |

Every cell above is **0 B/op and 0 allocations/op**. The edit benchmark reuses
one editor to repeatedly assign an existing scalar (excluding editor creation,
snapshot encoding, and saving).

Generated native access and generic decoded memory are neck-and-neck on the
nine-field pass (~1.35× faster than Fast2), showing that generic memory is a
fast fallback when you have no Go struct. Reflected field access is roughly
1.7× slower than Fast2.

For context, a direct Go struct field read measures 0.245 ns/op because it
bypasses KVO dispatch and enables compiler optimizations unavailable through a
generic API. KVO field access is fast, not equivalent to raw Go struct access.

## Including one decode per record

Each iteration starts from encoded bytes, optionally materializes a record once,
and then runs 0, 1, 16, or 256 nine-scalar passes. Zero passes isolates the
setup and decode cost. Keep the returned `Record` to amortize it.

| Source / strategy | 0 passes (ns) | 1 pass (ns) | 16 passes (ns) | 256 passes (ns) | B/op | Allocs/op |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Fast2 / direct | 3.9 | 66 | 1,008 | 16,141 | 0 | 0 |
| Fast2 / generated native | 455 | 502 | 1,232 | 12,651 | 448 | 5 |
| Fast2 / reflected native | 762 | 876 | 2,469 | 27,738 | 608 | 17 |
| Fast2 / generic memory | 824 | 960 | 1,690 | 12,389 | 1,288 | 8 |
| Compact2 / direct | 3.9 | 171 | 2,803 | 43,909 | 0 | 0 |
| Compact2 / generated native | 540 | 606 | 1,278 | 11,849 | 448 | 5 |
| Compact2 / reflected native | 893 | 1,019 | 2,634 | 27,582 | 608 | 17 |
| Compact2 / generic memory | 901 | 977 | 2,215 | 12,770 | 1,176 | 8 |
| MessagePack / direct | 3.9 | 1,652 | 26,358 | 419,357 | 0 | 0 |
| MessagePack / generated native | 1,271 | 1,326 | 1,984 | 12,688 | 720 | 21 |
| MessagePack / reflected native | 1,346 | 1,480 | 3,016 | 27,734 | 720 | 21 |

Allocations stay constant across pass counts for these scalar workloads.
MessagePack uses its full-row decoder in both native strategies (generated vs.
reflected applies to the subsequent field reads), while `row2` uses generated
or reflected code for decoding too. Generic memory is measured on unbound
`row2` records; MessagePack already requires a Go binding for its field names
and codecs.

How the formats break down:

- **Fast2:** Direct access wins at 1 and 16 passes. Generated native and
  generic memory pull ahead by 256 passes. Reflected native never catches
  direct Fast2 in this range—a binding alone does not make `Fastest()` faster.
- **Compact2:** Direct access wins at 1 pass; generated native and generic
  memory win by 16 passes. Reflection barely wins there, whereas generated
  fields cut repeated-access cost sharply.
- **MessagePack:** Decoding already wins on the first nine-field pass in this
  fixture. For a single property read, direct access can still win because it
  skips full-row construction and stops early in the encoded map.

The actual crossover depends on record width, field position, nested payload
sizes, codecs, and allocation pressure—treat these pass counts as useful bounds
for this fixture, not universal thresholds.

## Practical guidance

- Start with direct access for sparse reads and targeted edits.
- Use `Fast()` when you want to keep Fast2's indexed bytes and decode
  everything else (remember `Fast()` preserves Fast2's borrowed lifetime).
- Use `Fastest()` when repeated reads justify decoding, or when an encoded
  record must outlive its transaction. On an already-native record, `Fastest()`
  returns that same struct without cloning.
- When decoding to Go structs, attach generated field bindings or a generated
  `evmbind` schema; falling back to reflection can be slower than staying on
  Fast2.
- Editors on generated native structs and decoded memory mutate in place, while
  direct encoded edits copy bytes. Saving still goes through EDB's `RecordEdit`
  or typed-row API.
- Attach `WithModel(model)` for eager nested generic indexing; without it,
  `Fastest()` owns the encoded bytes and you read via `GetAs`. Generic maps and
  lists stay encoded, and whole-aggregate `Get` on native records may encode
  and allocate—cache collection values you traverse repeatedly.
- Database materialization can also decode omitted primary keys, and
  struct-based notifications or migrations can add decoding elsewhere in a
  transaction (record indexers and record notifications stay on the encoded
  read path).

Under the hood, these numbers reflect removing reflection from generated field
operations, using bounded property-to-slot indexes, borrowing immutable native
string bytes, and sorting generic decoded fields without a temporary hash map—
all without introducing a new disk format. Tests cover generated/reflected
parity, pointer identity, editing, ownership, and snapshots.

## Batched writes and borrowed dictionaries

Measured November 10, 2026 on Apple M5 Max, Go 1.27.3 (medians of three 150 ms
samples). Synthetic records include an untouched 8 KiB payload; times include
serialization and exclude database, index, and domain-save work.

Sixteen string assignments across four fields, followed by one serialization:

| Codec | Before | Batched | Bytes before → after | Allocations before → after |
| --- | ---: | ---: | ---: | ---: |
| MessagePack | 14.4 µs | 2.8 µs | 152,565 → 11,097 | 65 → 53 |
| Fast2 | 13.3 µs | 2.1 µs | 151,816 → 10,610 | 18 → 22 |
| Compact2 | 14.0 µs | 3.1 µs | 151,816 → 10,610 | 18 → 22 |

Staging adds a few small metadata allocations in exchange for eliminating
repeated copies of large untouched payloads (single-field writes pay a small
staging cost; this reduces total bytes, not to zero allocations).

Sixteen map assignments across four keys in a 32-entry map:

| Codec | Serialize after every write | Serialize once | Bytes each → once | Allocations each → once |
| --- | ---: | ---: | ---: | ---: |
| MessagePack | 40.4 µs | 6.7 µs | 174,512 → 13,716 | 296 → 101 |
| Fast2 | 51.5 µs | 4.5 µs | 221,393 → 15,797 | 344 → 59 |
| Compact2 | 46.8 µs | 5.1 µs | 221,389 → 15,797 | 344 → 59 |

Both columns use the new API—the difference is when you ask for a snapshot. In
a write loop, keep `edit.Map(prop)` or `edit.List(prop)`, read through that
editor, and snapshot once at the end (`edit.Get(collectionProp)` snapshots the
collection and `edit.Record()` snapshots the parent).

Warmed lookup plus full traversal of the 32-entry map reports **0 B/op and 0
allocs/op** across all three encoded formats: ~308 ns on Fast2, ~370 ns on
Compact2, and ~1,335 ns on MessagePack (down from ~7,026 ns by caching
normalized small-map keys on the stack while keeping last-key-wins semantics).
Native inline struct map values, custom object decoders, and dynamic JSON
entries can still allocate.

```sh
go test ./kvo -run '^$' \
  -bench 'Benchmark(RecordBatchUpdates|DictionaryBatchUpdates)$' \
  -benchmem -benchtime=150ms -count=3
```

The prior field-write baseline is commit `6168c89`. See the
[editing design notes](https://github.com/andreyvit/edb/blob/main/research/_docs/KVO-DESIGN.md)
for implementation and contract coverage.

## Property declaration namespace and raw Fast1 access

Typed property constructors and access types live in a single `kvo`
implementation. The historical forwarding-layer comparison
(`BenchmarkPropertyDeclarationPackage`, removed with the forwarding layer)
measured 6.71 ns via `record.BindSignedProp` versus 6.75 ns via
`kvo.BindSignedProp` (both 0 B/op, 0 allocs/op; M5 Max, Go 1.27.3, three
300 ms samples). Use `BenchmarkTypedProperty` for current access paths.

`fast1.BenchmarkTypedWords` keeps a candidate generic typed-key wrapper only as
a benchmark: on a one-field map, raw access took ~1.5 ns versus ~3.1 ns through
the wrapper (both allocation-free). Because of that overhead, Fast1's raw
word/dynamic-key interface stays numeric—resolve property references once
during registration and call `.Code()` at raw boundaries, while keeping
`record.Get(property)` fully typed.

```sh
go test ./kvo -run '^$' -bench '^BenchmarkTypedProperty$' -benchmem -benchtime=300ms -count=3
go test ./kvo/codec/fast1 -run '^$' -bench '^BenchmarkTypedWords$' -benchmem -benchtime=300ms -count=3
```

## Unified package

The [package merge check](https://github.com/andreyvit/edb/blob/main/research/kvo-package-merge/README.md)
compares the former split packages against the unified `kvo` package across
native reads and EVM scans: all 78 cases kept identical allocation counts and
the record handle stayed 16 bytes.
