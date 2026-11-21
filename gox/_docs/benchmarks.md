# GoX execution benchmarks

This report measures GoX rendering performance against cached Go
`html/template` across three anonymized, production-derived template suites,
along with targeted benchmarks for compact values and direct value conversion.

- **Direct value conversion (2026-11-10):** Removing JSON serialize-and-reparse
  before rendering cut `BenchmarkValueRendering` from 176.6 µs / 3,928
  allocations down to 84.4 µs / 2,054 allocations. See
  [conversion methodology](../../_docs/benchmarks.md#direct-value-conversion-2026-11-10).
- **Application corpus:** All names, copy, selectors, view models, URLs, and
  sample data are anonymized, while preserving the real loops, branches,
  helpers, and component patterns that drive rendering cost.

| Scene | Small / large | Patterns exercised |
| --- | --- | --- |
| **Workshop** | 12 / 48 activity cards | Four card variants, forms, checkboxes, optional icons, formatted amounts, state classes, a shared action component with two captured slots, JSON in `<script>` |
| **Journal** | 12 / 48 records | Four local rendering closures, table/list layouts, signed amounts, expiry states, copy controls, SVG, JSON attributes |
| **Comparison** | 4 plans × 8 features / 8 × 16 | Nested comparison matrix + responsive cards, per-cell helper calls, current-plan classes, SVG, CSS interpolation |

Benchmark names use `/12` and `/48` for small and large scenes (Comparison uses
them to pick matrix size). The timed Journal renders the table layout; parity
tests also cover the compact layout.

## Methodology

`BenchmarkApplication` compiles and links once, builds the view models, and
then times complete rendering to an immutable string:

- **GoX** uses its pooled VM and builder.
- **Go reference** uses cached `html/template` with typed helper scopes (not
  dynamic maps), a reused `bytes.Buffer`, and a copied output string.
- Both output strings escape into the same benchmark sink.
- Compilation, linking, model construction, EVM argument packing, and the first
  warm-up render are outside the timer. Both engines run their own formatting,
  escaping, component calls, and output allocation (host queries, localization,
  network I/O, and view-model preparation are excluded).

Every benchmark verifies byte-for-byte output parity before timing.
`TestApplicationTemplates` also compares hostile text, unsafe URL schemes,
missing icons, boolean attributes, and alternate layouts against
`html/template`.

## Compact values (2026-11-16)

Measured on Apple M5 Max, `darwin/arm64`, Go 1.27.3, `GOMAXPROCS=18`. Five
300 ms samples per case, alternating before (`bf28885`) and after (`a463b83`)
executables (both including the same focused benchmark). Compilation, linking,
warm-up, and parity checks stay outside the timer.

| Workload | Before | After | Before bytes / allocs | After bytes / allocs |
| --- | ---: | ---: | ---: | ---: |
| `Content`/`Optional`, 96 component calls | 66.3 µs | 38.4 µs | 39,086 / 1,201 | 10,636 / 337 |
| Dynamic value rendering | 83.6 µs | 84.5 µs | 86,252 / 2,054 | 86,244 / 2,054 |

On the focused `Content`/`Optional[string]` workload, compact value cells,
direct constructors, and register-backed non-escaping locals cut render time by
42%, allocations by 72%, and allocated bytes by 73%. Dynamic value conversion
stayed flat within run-to-run noise.

The original application corpus barely uses `Content`/`Optional`, so its
allocation counts stayed unchanged and timings shifted by at most 3%. GoX
continues to beat `html/template` across all six scenes:

| Scene | GoX (after) | `html/template` | GoX speedup | GoX / template allocs |
| --- | ---: | ---: | ---: | ---: |
| Workshop, 12 | 111.0 µs | 130.0 µs | 1.17× | 530 / 2,230 |
| Workshop, 48 | 431.4 µs | 529.8 µs | 1.23× | 2,143 / 8,766 |
| Journal, 12 | 62.7 µs | 69.5 µs | 1.11× | 248 / 1,071 |
| Journal, 48 | 231.0 µs | 263.4 µs | 1.14× | 947 / 4,145 |
| Comparison, small | 52.0 µs | 70.6 µs | 1.36× | 124 / 1,136 |
| Comparison, large | 159.7 µs | 207.0 µs | 1.30× | 228 / 3,244 |

Keep in mind that these results reflect this corpus—native helper cost,
formatting, and dynamic conversion still matter in real apps, and unsupported
optional payloads keep their fallback layout. Raw samples:
[before](benchmarks/2026-11-16-before.txt),
[after](benchmarks/2026-11-16-after.txt).

```sh
go test ./gox -run '^TestApplicationTemplates$' \
  -bench 'Benchmark(Application|ContentOptional|ValueRendering)$' \
  -benchmem -benchtime=300ms -count=5
```

## Main optimization pass (2026-11-06)

Measured on Apple M5 Max, `darwin/arm64`, Go 1.27.3, `GOMAXPROCS=18`. Medians
of eight 200 ms samples per case, alternating before and after executables
sequentially. "Before" is GoX at the start of the optimization pass running the
same fixtures.

| Scene | GoX before | GoX after | `html/template` | GoX speedup | GoX vs. `html/template` |
| --- | ---: | ---: | ---: | ---: | ---: |
| Workshop, 12 cards | 154.7 µs | 89.7 µs | 128.7 µs | 1.72× | 1.43× |
| Workshop, 48 cards | 606.1 µs | 351.0 µs | 496.2 µs | 1.73× | 1.41× |
| Journal, 12 rows | 72.8 µs | 49.2 µs | 67.4 µs | 1.48× | 1.37× |
| Journal, 48 rows | 274.6 µs | 183.6 µs | 260.2 µs | 1.50× | 1.42× |
| Comparison, 4 × 8 | 79.8 µs | 39.5 µs | 65.7 µs | 2.02× | 1.66× |
| Comparison, 8 × 16 | 247.9 µs | 120.6 µs | 197.6 µs | 2.06× | 1.64× |

Across all six scenes, GoX rendered in **27–40% less time** than cached
`html/template`, while allocating **47–60% fewer bytes** and **67–88% fewer
objects** than before (the final output string remains unavoidable in this API):

| Scene | GoX before (B / allocs) | GoX after (B / allocs) | `html/template` (B / allocs) |
| --- | ---: | ---: | ---: |
| Workshop, 12 | 87,691 / 1,719 | 35,002 / 522 | 68,932 / 2,230 |
| Workshop, 48 | 349,409 / 6,853 | 139,464 / 2,106 | 272,008 / 8,766 |
| Journal, 12 | 35,231 / 741 | 16,393 / 246 | 35,689 / 1,071 |
| Journal, 48 | 135,927 / 2,876 | 61,747 / 933 | 138,757 / 4,145 |
| Comparison, 4 × 8 | 44,720 / 771 | 21,875 / 125 | 48,417 / 1,136 |
| Comparison, 8 × 16 | 128,962 / 1,971 | 68,034 / 229 | 153,981 / 3,244 |

Raw samples: [before](../testdata/bench/results/gox-before.txt),
[after](../testdata/bench/results/gox-after.txt).

### What drove the speedup

- Pre-planning static attributes and opening tags once (keeping tags mutable
  until child output starts; helpers invalidate cached prefixes).
- Batching consecutive compiler-generated builder calls and folding static
  subtrees into a single write while keeping expressions, calls, and control
  flow as barriers.
- Reusing attribute storage and collecting class fragments without repeated
  string concatenation, clearing pooled references on both success and failure.
- Appending ordinary escaped strings directly into bounded output buffers and
  using typed string/boolean pipelines to skip interface boxing (trusted values
  keep the generic path).
- Extending EVM direct host calls to void functions and methods so tag ops and
  builder helpers read registers directly, and folding forwarding wrappers.

String paths reuse Go's replacement table, URL safety check, and URL processor.
Differential tests cover random bytes, invalid UTF-8, contextual filters, and
output limits, with all context checks, strict nil handling, cancellation, and
allocation budgets active.

## EVM controls

Running `BenchmarkEVM`, `BenchmarkSmallHost`, and `BenchmarkOptimizer` across
22 workloads (eight alternating 150 ms samples each) showed median shifts of
−2.6% to +2.3% with identical allocation counts and bytes (the +2.3% case was
recursive Fibonacci, which does not use the new host-call forms).

`BenchmarkHostShapes` isolates the new void direct calls over 128 calls with
pointer and string arguments (five 200 ms samples, zero allocations):

- Direct void functions: **1.28 µs** vs. 2.39 µs on the general ABI.
- Direct void pointer methods: **1.29 µs** vs. 2.39 µs on the general ABI.

Raw samples: [EVM before](../testdata/bench/results/evm-before.txt),
[EVM after](../testdata/bench/results/evm-after.txt),
[void calls](../testdata/bench/results/void-calls.txt).

## Reproduce and profile

```sh
go test ./gox -run TestApplicationTemplates
go test ./gox -run '^$' -bench BenchmarkApplication -benchmem -benchtime=200ms -count=8
go test ./bench -run '^$' -bench '^(BenchmarkEVM|BenchmarkSmallHost|BenchmarkOptimizer)$' -benchmem -benchtime=150ms -count=8
go test ./bench -run '^$' -bench '^BenchmarkHostShapes/(psv|method.sv)$' -benchmem -benchtime=200ms -count=5
```

When comparing two revisions, build each with `go test -c` and alternate the
two binaries sequentially rather than running them in parallel. To profile a
large scene:

```sh
go test ./gox -run '^$' -bench '^BenchmarkApplication/workshop/48/GoX$' -benchtime=3s -cpuprofile=/tmp/gox.cpu -memprofile=/tmp/gox.mem -o /tmp/gox.test
go tool pprof -top /tmp/gox.cpu
go tool pprof -top -alloc_objects /tmp/gox.mem
```

Use `BenchmarkApplication` (not the tiny 100-row `BenchmarkRender` diagnostic)
for engineering decisions—the original implementation won `BenchmarkRender`
while losing on all three application shapes. Closure capture allocations,
dynamic attributes, and formatting remain good targets for future profiling.
