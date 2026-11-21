# Shared KVO list values

When the compiler proves that a script only reads a scalar or string list
locally, EVM carries that list in the exact same 16-byte `kvo.ListCell` used by
native Go's `kvo.List[T]`. The value holds a GC-visible pointer, a representation
ID, and an auxiliary length. Shared metadata describes the encoding and element
type, so there is no per-list heap allocation.

In a script, list reads look like ordinary Go:

```go
for _, tag := range customer.tags {
    if tag == wanted {
        matches++
    }
}
n := len(customer.tags)
firstMatches := customer.tags[0] == wanted
```

In native Go, the equivalent uses `for tag := range customer.Get(Customer_Tags).Iter()`
(Go cannot `range` directly over a struct). The 16-byte `List[T]` handle also
exposes `Len()`, `Cursor()`, `Slice()`, and `OwnedSlice()`. `Slice()`
deliberately materializes a Go slice whose strings still borrow the source,
while `OwnedSlice()` copies the strings as well.

Inside the VM, the list is resolved on acquisition and its traversal state
lives in a reusable workspace per indexing site. That workspace caches an
expanded 32-byte `ListValue` view alongside its iterator—the transported value
itself stays 16 bytes. When a single `len` or index expression immediately
consumes the list, the compiler fuses it with acquisition to save an
instruction and a register while preserving evaluation order.

This is an internal storage unification rather than a new guest slice ABI.
Mutable, escaping, or otherwise unproven uses still follow the existing
materialization and ownership rules, and borrowed strings from a list still
obey scope ownership. Model-level access metadata is reused across script
compilations, and new representations are registered once on first use.

## Before-and-after measurements

Measured November 9, 2026 on Apple M5 Max, `macOS arm64`, Go 1.27.3, with one
logical CPU. Baseline: EDB `18e9cf0` (the benchmark added right before
implementation). Candidate: the shared list representation accompanying
this report. Both binaries used identical benchmark source, toolchain, and
hardware, run in six paired rounds alternating order with no overlapping builds
or tests.

[`BenchmarkRecordListCell`](../evmcompiler/record_lists_bench_test.go) cycles
through 64 distinct records. Each invocation runs 16 acquisitions plus the
requested range, length, first-index, or reverse traversal over 3- or
32-string lists, verifying the result every time. Each sample runs for 125 ms
after warming metadata and VM state. Medians below are per VM invocation
(covering all 16 repetitions). Raw samples: [list-cells.tsv](../bench/list-cells.tsv).

| List size | Backing | Before range (ns) | After range (ns) | Change |
| --- | --- | ---: | ---: | ---: |
| 3 | Fast2 | 2,428 | 2,354 | -3.1% |
| 3 | Compact2 | 2,634 | 2,562 | -2.7% |
| 3 | MessagePack | 3,494 | 3,099 | -11.3% |
| 3 | Native struct | 2,623 | 2,495 | -4.9% |
| 3 | Memory | 2,333 | 2,296 | -1.6% |
| 32 | Fast2 | 11,870 | 10,948 | -7.8% |
| 32 | Compact2 | 11,790 | 10,831 | -8.1% |
| 32 | MessagePack | 21,639 | 19,084 | -11.8% |
| 32 | Native struct | 16,996 | 15,903 | -6.4% |
| 32 | Memory | 11,709 | 10,837 | -7.4% |

All 40 EVM workload medians improved: length by 5–13%, first-index access by
1–46%, and reverse traversal by 1–8%. Treat the smallest shifts as noise rather
than guaranteed speedups. The six native Go control workloads stayed flat or
improved slightly (0–2.4%): native typed range went from 90.07 to 89.96 ns on
Fast2 and from 137.75 to 134.40 ns on MessagePack. All 552 microbenchmark
samples reported **0 bytes and 0 heap allocations**.

These numbers measure the complete implementation including adjacent-consumer
fusion. An earlier prototype added redundant representation validation and
iterator setup and regressed several workloads, so it was discarded. Now
validation runs only when a list value or cache generation changes, sequential reads
reuse the iterator, and reverse traversal resets it from the cached view.

## Whole-scan and unrelated controls

The `back3` application comparison runs against a reindexed disposable dev
snapshot containing 462,902 customers, 381,014 stats rows, 1,615 tag matches,
and a 1,400-point sum. Every path loads stats for every customer before
filtering. Six paired rounds alternate binaries; each sample times three full
scans, with the reference calculation and warm-up outside the timer.
Transaction and cursor setup are inside the timer, and the public path also
includes invocation and session setup.

| Complete scan | Before (ms) | After (ms) | Allocations (before / after) | Bytes (before / after) |
| --- | ---: | ---: | ---: | ---: |
| Native typed KVO control | 574.34 | 569.56 | 44 / 44 | 2,770 / 2,770 |
| EVM original adapter | 600.76 | 598.43 | 44 / 44 | 2,770 / 2,770 |
| EVM public Scan/Key/Get | 803.93 | 798.54 | 57 / 57 | 4,106 / 4,106 |

These sub-1% differences fall within run-to-run variation: full-scan time is
effectively unchanged, and there are still **zero per-row allocations** (only
the fixed transaction and invocation allocations remain). Bytes per operation
are slightly lower than earlier single-iteration reports because these samples
average three scans. Both customer and stats rows here are stored as
MessagePack; traditional full decoding is unaffected and was not remeasured.

[All initial controls and full-scan samples](../bench/list-cells-validation.tsv)
also include 100 ms scalar record read, write, and interface workloads. A noisy
interface control and a +2.8% Fast2 scalar-read median prompted a longer
follow-up (six more alternating pairs, one second per workload). Those medians
settled at:

- Interface access: 23,942 → 23,771 ns (-0.7%)
- Fast2 scalar reads: 52,152.5 → 52,465.5 ns (+0.6%)
- Compact2 reads: 61,134.5 → 61,039.5 ns (-0.2%)

Allocations were unchanged ([longer control samples](../bench/list-cells-controls.tsv)),
and short write controls differed by +0.5–1.1%, within normal variation.

`BenchmarkSmallHost/*/Direct` could not serve as a control because the
unmodified baseline rejects its direct-method binding signature. It was
excluded before the paired validation run, and this report makes no claim about
it.

## Reproduce

Build binaries from the baseline commit and the candidate in separate checkouts
(or with a Go source overlay), then alternate their order for six rounds:

```sh
go test -c -o /tmp/evm-list.test ./evmcompiler
/tmp/evm-list.test -test.run='^$' -test.bench='^BenchmarkRecordListCell$' \
  -test.benchtime=125ms -test.cpu=1

go test -c -o /tmp/go-list.test ./kvo
/tmp/go-list.test -test.run='^$' \
  -test.bench='^BenchmarkTypedList$/^3$/(Fast2|MessagePack)/(CellHandle|CellCursor|CellRange)$' \
  -test.benchtime=125ms -test.cpu=1
```

Compilation, registration, and owned copies are outside the zero-allocation
claim. Separate tests verify empty and `nil` lists, incompatible list element types,
multiple and nested lists, bounds checks, evaluation order, and materialization
across invalidating calls.

Validation: `go test ./...`, `go test -race ./...`, and
`go test -gcflags=all=-d=checkptr=1 ./...` all passed. `back3`'s full
`plugins/tenantenv` and `fire/business/superadmin/suscripts` suites—along with
the live `TestCustomerSnapshotAllocations` and
`TestCustomerSnapshotPublicAllocations` gates—also passed, keeping the same
44 / 44 / 57 full-scan allocation counts.
