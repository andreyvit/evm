# EVM execution benchmarks

Every section below is a dated measurement tied to a specific commit, machine,
workload, and timing boundary—never a blanket guarantee. To run the suites
yourself, see [bench/README.md](../bench/README.md).

## Direct value conversion, 2026-11-10

Measured on Apple M5 Max, Go 1.27.3 (medians of three 200 ms samples, rounded),
comparing commit `d6abdc5` against direct value conversion without JSON
serialize-and-reparse round trips:

| Workload | JSON round trip | Direct conversion | Heap allocations |
| --- | ---: | ---: | ---: |
| Record export: 32 nested objects, strings, and integer fields | 32.3 µs / 56.9 KB | 11.5 µs / 33.0 KB | 971 → 509 |
| GoX: nested dynamic values in text, attributes, and scripts (×16) | 176.6 µs / 161.3 KB | 84.4 µs / 86.1 KB | 3,928 → 2,054 |

These synthetic fixtures isolate conversion cost (compile, link, and fixture
setup stay outside the timer); templates dominated by scalar reads will see a
smaller effect.

- **Record export** now goes `Record → owned Go projection → VM values`
  (sharing the projection with native `Record.ToJSON` and calling custom
  JSON/text marshalers only at custom leaves), eliminating the JSON buffer and
  second Go tree. Final VM maps/slices and the intermediate projection still
  allocate.
- **GoX rendering** goes `VM values → owned Go JSON data → rendering`, sharing
  traversal with the JSON encoder and metering output allocations.

```sh
go test . ./gox -run '^$' \
  -bench 'Benchmark(RecordJSON|ValueRendering)$' -benchmem -benchtime=200ms -count=3
```

Regression tests verify parity with the old JSON round trip across 64-bit
integer precision, signed/unsigned dynamic types, `nil` collections, custom
marshalers and errors, malformed UTF-8, escaping, size limits, cancellation,
allocation limits, and ownership after overwriting the original encoded bytes.
The GoX fixture is verified against `html/template`.

## JSON dynamic values, 2026-11-07

Unboxed interface headers (described in [values/README.md](../values/README.md))
compared against `10daf42e5bcf887735ac66779362598fece5db63` on Apple M5 Max,
`macOS arm64`, Go 1.27.3, single CPU. Eight rounds alternated binary order and
rotated workloads (300 ms per sample). Times are medians; the paired change
column is the median of within-round ratios, so it need not match the ratio of
medians. [Every sample](../bench/json-values.tsv).

The fixture holds 128 customer objects with nested profiles, label arrays,
booleans, nulls, strings, signed integers, full-width unsigned integers, and
floats, rotating across four inputs and checking checksums against native Go
(and full JSON output in tests). Parsing and encoding are unchanged, isolating
the value representation.

| JSON workload | Before (µs) | After (µs) | Paired change | Bytes/op (before → after) | Allocs/op (before → after) |
| --- | ---: | ---: | ---: | ---: | ---: |
| Build and read | 205.42 | 188.83 | -9.4% | 248,800 → 203,632 | 5,520 → 4,237 |
| Read existing objects | 37.77 | 37.76 | +0.2% | 0 → 0 | 0 → 0 |
| Update and read | 91.04 | 72.55 | -21.1% | 24,608 → 0 | 769 → 0 |
| Decode a KVO JSON property and read | 530.30 | 504.75 | -4.8% | 509,944 → 464,777 | 11,867 → 10,584 |
| Rebox integer/string 1,024 times | 70.33 | 38.86 | -44.9% | 65,600 → 0 | 2,050 → 0 |
| Encode existing objects | 307.43 | 307.97 | 0.0% | 157,609 → 157,610 | 4,766 → 4,766 |

Build-and-read cuts total allocated bytes by 18.2% and decode-and-read by 8.9%
(total allocations, not peak RSS; maps, backing arrays, and the decoder's
intermediate native JSON tree still allocate). Sharing the type-table pointer
also trims 16 bytes from each VM map object.

An initial prototype slowed pointer and function interface loops by 6–7%. The
final version resolves pointer assertion IDs at link time, decodes concrete
assertions once, and keeps direct pointer packing and function boxing without
adding opcodes or changing main dispatch.

| Independent control | Paired change | Allocation change |
| --- | ---: | --- |
| Pointer-valued `any` | +0.6% | None (0 allocs) |
| Boxed function `any` | +0.3% | None |
| External scan | -0.5% | None |
| Fibonacci | -2.7% | None |
| Sieve | -0.1% | None |
| Pricing | +0.3% | None |
| Mandelbrot | -0.4% | None |
| Queue | -1.0% | None |
| KVO read | +0.1% | None |
| KVO write | -0.2% | None |
| GoX workshop, 48 rows | +1.6% | 2,106 → 2,048 allocs |
| GoX journal, 48 rows | -0.3% | 933 → 900 allocs |
| GoX comparison, 48 rows | -0.4% | None |

GoX controls used six paired 250 ms samples; workshop timing was noisy
(-17.7% to +5.6%), making the +1.6% median inconclusive.

Reproduce from the repository root:

```sh
trial=$(mktemp -d)
mkdir -p "$trial/base-src" "$trial/base" "$trial/unboxed"
git archive 10daf42e5bcf887735ac66779362598fece5db63 | tar -x -C "$trial/base-src"
cp bench/json_test.go bench/any_test.go "$trial/base-src/evm/bench/"
cp -R bench/internal/jsonwork "$trial/base-src/evm/bench/internal/"
go -C "$trial/base-src" test -c -o "$trial/base/bench" ./bench
go test -c -o "$trial/unboxed/bench" ./bench
python3 bench/compare/scratch/measure.py "$trial" base,unboxed \
  JSON/BuildRead,JSON/Read,JSON/Update,JSON/DecodeRead,JSON/Rebox,JSON/Encode,AnyControl/Pointer,AnyControl/Function,Scan,Fib,Sieve,Price,Mandelbrot,Optimizer/Queue,KVO/ReadCustomers,KVO/WriteCustomers \
  --rounds 8 --ms 300
```

Full EDB tests, `-race`, and `-gcflags=all=-d=checkptr=2` passed for the
runtime and GoX (along with downstream application tests in `Bubblehouse`).

## Cross-interpreter workloads

The five cross-interpreter benchmarks time one complete script invocation on a
warm VM, rotating across four inputs and checking every return value during
timing. Database workloads time a complete transaction.

- **Price:** Values a portfolio of 32 fixed-rate loans (equal principal over 120
  months, 3,840 payments total) under a supplied discount rate with no host
  calls, returning portfolio value in cents (checked against an independent
  decimal calculation).
- **Scan:** Reads 128 sensors, calling the host twice per sensor (reading and
  threshold) and summing any excess—256 real callback crossings with no I/O.
- **Fib, Sieve, Mandelbrot:** Described in [Interpreter kernels](#interpreter-kernels) below.

## Compiler and frame optimizations, 2026-11-04

Compares EDB `f819d12` (which already had specialized external calls and the
two-register float-loop evaluator) with `493ee9f`, using the production
compiler and identical checked inputs. See
[Optimizer and results](EVMBook.md#109-benchmarks-methodology-and-trade-offs)
and [individual measurements](../bench/optimizer.tsv).

```sh
trial=$(mktemp -d)
mkdir -p "$trial/base-src" "$trial/base" "$trial/ship"
git archive f819d12 | tar -x -C "$trial/base-src"
cp bench/optimizer_test.go "$trial/base-src/evm/bench/"
cp -R bench/internal/optwork "$trial/base-src/evm/bench/internal/"
(cd "$trial/base-src" && go test -c -o "$trial/base/bench" ./bench)
go test -c -o "$trial/ship/bench" ./bench
measure=bench/compare/scratch/measure.py
python3 "$measure" "$trial" base,ship Scan,Fib,Sieve,Optimizer/Queue --rounds 8 --ms 240
python3 "$measure" "$trial" base,ship KVO/ReadCustomers,KVO/WriteCustomers,AccountState/EVM/ReadCustomers/SetIndexer --rounds 8 --ms 240
```

Series recorded in `optimizer.tsv`:

- **`complete` / `extra`:** The original five workloads, seven integer kernels,
  three float kernels, external calls, KVO, indexed Bolt transactions, build
  time, and unmodified Gonum v0.16.0 kernels (`EVM_GONUM_DIR`).
- **`engines`:** EVM vs. PUC Lua 5.5.1, LuaJIT 2.1.1792588164 (JIT off and on),
  Lunar v0.1.3, GopherLua v1.1.2, and Yaegi v0.16.1:

```sh
mkdir -p "$trial/lunar" "$trial/gopher" "$trial/yaegi"
(cd bench/compare/golua && go test -c -o "$trial/lunar/bench")
cp "$trial/lunar/bench" "$trial/gopher/bench"
(cd bench/compare/yaegi && go test -c -o "$trial/yaegi/bench")
LUA_PREFIX="$(brew --prefix lua)" bench/compare/native/run.sh --build "$trial/lua"
LUA_PREFIX="$(brew --prefix luajit)" bench/compare/native/run.sh --build "$trial/jit"
python3 "$measure" "$trial" ship,lua,jit-off,jit,lunar,gopher,yaegi Price,Scan,Fib,Sieve,Mandelbrot --rounds 6 --ms 250
```

- **Targeted ablations:** `raw`, `packed`, `inline`, and `frame` (raw-frame
  clear elision, packed local slices, scalar leaf inlining, warm-call setup);
  `cse` and `pool` (expression reuse, loop-invariant constant materialization);
  `slots` and `result` (block-wide scratch reuse vs. result-slot rule); `fixed`
  (fixed-trip unrolling); `child`, `hoist`, `hb`, `len`, and `sites` (sibling
  KVO reads, loop-bound hoisting, predicate/branch fusion, length lowering,
  cursor-site bitmap); `readproof` (reference, `sites`, `latch`, `reads`);
  `writes` (ten 600 ms database write pairs); and `txcount` (twelve 40-iteration
  pairs comparing indexed EVM and native Go writers).

## Scalar scratch registers, 2026-11-04

Compares EDB `8a35218` and `40ff912` on Apple M5 Max, `macOS arm64`, Go 1.27.3,
single CPU. Successive rounds reverse pair order and rotate workloads. Times
are median microseconds per invocation; percentage changes are medians of
**within-round** differences (small isolated changes of a few percent can
reflect Go code layout or machine noise).

### Coverage outside the development workloads

The linker recognized 6 of 12 local math kernels (both executors ran identical
guest source, zero Go heap allocations when warm, six paired 180 ms samples per
length):

| Kernel | Linked | 8 iters change | 512 iters before (µs) | After (µs) | 512 iters change |
| --- | :---: | ---: | ---: | ---: | ---: |
| Filter | yes | -17.6% | 4.48 | 2.53 | -43.4% |
| Oscillator | yes | -25.2% | 8.42 | 4.74 | -43.8% |
| Midpoint | yes | +8.9% | 5.09 | 4.92 | -3.5% |
| Chebyshev | — | -0.8% | 5.01 | 5.03 | +0.1% |
| Horner | yes | -16.4% | 3.64 | 1.81 | -50.2% |
| Dot | yes | +0.2% | 2.40 | 1.90 | -20.5% |
| Kahan | — | +2.3% | 7.89 | 7.88 | +0.2% |
| Variance | — | -0.7% | 6.58 | 6.55 | -0.5% |
| Calibrate | yes | -0.1% | 3.02 | 2.61 | -14.0% |
| Autocorrelation | — | +1.0% | 4.56 | 4.74 | +3.5% |
| SoftClip | — | -2.2% | 6.06 | 5.80 | -4.6% |
| Newton | — | -5.8% | 0.15 | 0.14 | -4.6% |

`Newton` exits early on convergence. `Midpoint` mirrors four intermediates to
the frame per iteration, so its tiny 8-iteration case spent more on setup and
stores than it saved in dispatch.

Testing 15 unmodified Gonum v0.16.0 routines (length 512; after adding one
range-loop back-edge rule on the first 10 reductions, 5 more transforms were
tested with the linker frozen, selecting 3):

| Gonum v0.16.0 | Linked | Before (µs) | After (µs) | Time change |
| --- | :---: | ---: | ---: | ---: |
| `DotUnitary` | yes | 3.87 | 2.12 | -46.4% |
| `DotInc` | — | 2.69 | 2.78 | +2.8% |
| `L1Norm` | — | 7.63 | 7.54 | -0.8% |
| `L1NormInc` | — | 3.92 | 3.86 | -1.2% |
| `L1Dist` | — | 8.75 | 8.90 | +1.8% |
| `LinfDist` | — | 15.64 | 15.47 | -1.2% |
| `Sum` | yes | 3.18 | 1.39 | -56.2% |
| `L2NormUnitary` | — | 16.42 | 16.44 | 0.0% |
| `L2NormInc` | — | 9.03 | 9.01 | 0.0% |
| `L2DistanceUnitary` | — | 17.63 | 17.25 | -1.7% |
| `AxpyUnitaryTo` | yes | 4.78 | 4.10 | -14.5% |
| `ScalUnitaryTo` | yes | 3.74 | 2.01 | -46.2% |
| `DivTo` | yes | 4.30 | 3.30 | -23.1% |
| `CumSum` | — | 4.79 | 4.91 | +3.3% |
| `CumProd` | — | 4.80 | 4.75 | -0.9% |

The pass intentionally declines strided loads, neighbor accesses (`CumSum`,
`CumProd`), and loops with calls or branches (`L2Norm`, `L1Dist`).

### Original workloads, database controls, and build cost

Eight paired 220 ms samples (512 records for KVO runs, 4,096 for indexed
account-state; timings include storage and index work):

| Workload | Before (µs) | After (µs) | Time change |
| --- | ---: | ---: | ---: |
| Price | 24.09 | 19.97 | -17.0% |
| Mandelbrot | 217.50 | 177.86 | -18.4% |
| Fibonacci | 45.77 | 45.53 | -0.4% |
| Sieve | 99.87 | 102.50 | +2.7% |
| External scan | 3.29 | 3.27 | -0.4% |
| Raw unary scan | 1.81 | 1.73 | -4.2% |
| Scalar predicate | 2.62 | 2.64 | +0.7% |
| Scoped cursor | 7.78 | 7.80 | +0.4% |
| Memory KVO read | 46.74 | 46.35 | -0.7% |
| Memory KVO write | 539.53 | 534.52 | -0.3% |
| Bolt KVO read | 47.67 | 47.45 | +0.2% |
| Bolt KVO write | 709.56 | 692.00 | -0.9% |
| Indexed account-state read | 387.93 | 392.67 | +1.1% |
| Indexed account-state write | 13,856.05 | 13,918.23 | +0.5% |
| Compile 12 math kernels | 603.02 | 615.46 | +0.4% |
| Link 12 math kernels | 7.01 | 10.75 | +51.6% |

Runtime allocations were unchanged (0 for `Price`, `Scan`, `Mandelbrot`; one
81,920-byte slice for `Sieve`; 15 for memory KVO read; 24 for Bolt and indexed
reads). One-time linking of the 12 kernels grew from 24,168 B (101 allocs) to
31,744 B (152 allocs) to build the loop plans.

### Four Lua and EVM configurations

Six rotating rounds (`PUC Lua 5.5.1`, `LuaJIT 2.1.1792588164`; tracing enabled
only in the last column):

| Workload | EVM (µs) | PUC Lua (µs) | LuaJIT off (µs) | LuaJIT on (µs) |
| --- | ---: | ---: | ---: | ---: |
| Price | 19.94 | 27.74 | 24.08 | 4.66 |
| Mandelbrot | 176.25 | 196.64 | 405.84 | 21.59 |
| Fib | 45.37 | 20.67 | 12.96 | 3.13 |
| Sieve | 101.93 | 93.15 | 108.42 | 11.85 |
| Scan | 3.27 | 2.94 | 2.51 | 2.06 |

### Why two float scratch locals won (and why main-switch scratch lost)

Adding scratch opcodes directly to the main interpreter switch bloated
`(*VM).execute` and slowed down even scripts that never used scratch registers:

| Main-switch float variant | Price (µs) | Mandelbrot (µs) | External scan (µs) | `execute` text (B) |
| --- | ---: | ---: | ---: | ---: |
| Original | 23.84 | 216.95 | 3.30 | 17,184 |
| Four floats, destructive | 90.12 | 589.38 | 4.10 | 18,576 |
| Three operands | 83.56 | 494.17 | 4.12 | 20,112 |
| Three operands + mixed | 53.20 | 364.52 | 4.44 | 21,088 |

| Main-switch integer variant | Sieve (µs) | Fib (µs) | External scan (µs) | `execute` text (B) |
| --- | ---: | ---: | ---: | ---: |
| Original | 101.72 | 45.88 | 3.30 | 17,184 |
| Two integer locals | 167.54 | 61.29 | 4.13 | 18,304 |
| Four integer locals | 173.91 | 63.91 | 4.22 | 41,808 |

Moving float loops into a separate helper kept `execute` untouched (17,248 B).
Varying only the number of scratch locals in that helper showed clear
diminishing returns vs. code size:

| Scratch locals in arithmetic helper | Price (µs) | Mandelbrot (µs) | Helper text (B) |
| ---: | ---: | ---: | ---: |
| 0 | 22.41 | 197.70 | 1,040 |
| 1 | 20.72 | 174.43 | 4,976 |
| **2 (retained)** | **17.98** | **155.42** | **15,248** |
| 3 | 16.68 | 144.20 | 35,936 |
| 4 | 15.92 | 138.01 | 72,176 |

The retained two-local helper (including moves, conversions, and slice loads
and stores) is 16,672 B. Untimed counters on scenario 0:

| Entry and schedule | Ordinary dispatch | Private steps | Folded | Scratch loads | Stores | Moves | Extra spills |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Price, original | 19,798 | 0 | 0 | 0 | 0 | 0 | 0 |
| Price, 4 floats destructive | 50,582 | 0 | 0 | 15,424 | 3,840 | 3,840 | 0 |
| Price, 4 floats three-operand | 46,742 | 0 | 0 | 15,424 | 3,840 | 0 | 0 |
| Price, 4 floats mixed | 27,606 | 0 | 0 | 96 | 32 | 0 | 0 |
| Price, retained | 1,133 | 14,932 | 3,733 | 212 | 212 | 0 | 0 |
| Mandelbrot, original | 118,265 | 0 | 0 | 0 | 0 | 0 | 0 |
| Mandelbrot, retained | 18,342 | 87,724 | 12,199 | 1,896 | 1,896 | 0 | 0 |
| Sieve, original and retained | 68,938 | 0 | 0 | 0 | 0 | 0 | 0 |

[Reproduction commands](../bench/compare/scratch/README.md) ·
[Raw samples](../bench/scratch.tsv) ·
[Worked plan & arm64 dispatch in the book](EVMBook.md#106-float-loop-acceleration-keeping-two-floats-in-go-locals)

## Specialized external calls, 2026-11-04

EDB `6bda256`, Apple M5 Max, `macOS arm64`, Go 1.27.3. Six alternating rounds
comparing general vs. direct bindings on the same binary, script, and data
(256 entries per invocation, single CPU, 250 ms samples):

| Workload | General (µs) | Direct (µs) | Paired change | Go allocs (general → direct) |
| --- | ---: | ---: | ---: | ---: |
| Generated scalar predicate | 12.43 | 3.98 | −68.0% | 256 → 0 |
| Generated tag check on a Go struct | 66.44 | 7.19 | −89.1% | 2,049 → 1 |
| Handwritten scalar predicate | 2.81 | 2.63 | −6.2% | 0 → 0 |
| Pointer getter and balance method | 6.19 | 4.88 | −21.2% | 0 → 0 |
| Pointer getter and tag check | 7.32 | 5.88 | −19.6% | 0 → 0 |
| Cached context and string getter | 7.20 | 6.04 | −16.2% | 0 → 0 |
| Cursor over scoped KVO rows | 8.16 | 7.85 | −3.9% | 0 → 0 |

In the isolated signature sweep (`BenchmarkHostShapes`, 128 calls, 75 ms
samples), all 39 function forms and 12 method forms improved by 11.2–35.5% with
zero Go allocations after warm-up.

```sh
go test ./bench -run '^$' -bench '^BenchmarkHostShapes$' -benchtime=75ms -benchmem -cpu 1
go test ./bench -run '^$' -bench '^BenchmarkSmallHost$' -benchtime=250ms -benchmem -cpu 1
go test ./evmbind -run '^$' -bench '^BenchmarkGeneratedHost$' -benchtime=250ms -benchmem -cpu 1
```

Regression controls comparing `34f9fcc` vs. `6bda256` (eight alternating 400 ms
rounds, unchanged allocation counts; [per-round samples](../bench/host-calls.tsv)):

| Existing script | Before (µs) | After (µs) | Paired change |
| --- | ---: | ---: | ---: |
| Price | 23.85 | 23.93 | +0.5% |
| Sensor scan | 3.35 | 3.33 | −1.0% |
| Sensor scan, raw unary | 1.77 | 1.74 | −1.4% |
| Recursive Fibonacci | 46.28 | 46.13 | +0.2% |
| Sieve | 102.75 | 104.04 | +0.5% |
| Mandelbrot | 215.45 | 216.47 | +0.7% |

| Existing database path | Rows | Read change | Write change |
| --- | ---: | ---: | ---: |
| Fast, memory | 512 | −2.4% | −2.3% |
| Compact, memory | 512 | −1.8% | −2.7% |
| Fast, Bolt | 512 | −3.6% | −2.2% |
| Compact, Bolt | 512 | −2.1% | −1.0% |
| Indexed KVO table | 4,096 | −3.7% | −2.5% |
| Indexed MessagePack table | 4,096 | −0.1% | −0.1% |

## Interpreter comparison, 2026-11-04

Eight rotating rounds on Apple M5 Max, `macOS arm64`, Go 1.27.3. Medians in µs
for one checked invocation (`EVM before`: `096ba40` over `2bf855c`; `EVM`:
`e3a6829`; Yaegi v0.16.1; Lunar v0.1.3; GopherLua v1.1.2; PUC Lua 5.5.1;
LuaJIT 2.1.1792588164). [Individual samples](../bench/interpreter-kernels.tsv).

| Workload | EVM before | EVM | Yaegi | Lunar | GopherLua | PUC Lua | LuaJIT off | LuaJIT |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Price | 36.68 | 28.07 | 184.0 | 53.75 | 277.0 | 29.62 | 27.02 | 5.00 |
| Scan | 4.43 | 3.84 | 40.99 | 9.57 | 17.67 | 2.89 | 2.74 | 2.29 |
| Fib | 90.89 | 53.43 | 733.9 | 60.31 | 130.1 | 21.26 | 16.09 | 3.43 |
| Sieve | 396.7 | 118.7 | 606.0 | 298.0 | 834.9 | 101.8 | 124.5 | 13.67 |
| Mandelbrot | 437.1 | 254.6 | 2,563.7 | 465.9 | 1,743.7 | 222.0 | 445.0 | 24.56 |

With both sensors registered via `DirectUnary`, `Scan` dropped to **2.00 µs**.
EVM allocated zero on `Price`, `Scan`, `Fib`, and `Mandelbrot` after warm-up
(and one 81,920-byte slice on `Sieve`). Note that EVM enforced instruction and
allocation budgets while Lua ran without quotas, and native `Scan` calls C
callbacks while Go engines call Go callbacks.

### Database controls on the same revision

Eight alternating 450 ms rounds (`-cpu 1`, Bolt sync disabled; 512 rows for the
first eight, 4,096 for the last four):

| Table workload | Before (µs) | After (µs) | Paired change | Allocs (before → after) |
| --- | ---: | ---: | ---: | ---: |
| `KVO/ReadCustomers` | 53.47 | 53.58 | -3.3% | 15 → 15 |
| `KVO/WriteCustomers` | 627.0 | 622.9 | -0.9% | 4,799 → 4,799 |
| `KVOCompact/ReadCustomers` | 61.92 | 62.22 | -0.5% | 15 → 15 |
| `KVOCompact/WriteCustomers` | 804.3 | 822.5 | +2.4% | 5,216 → 5,216 |
| `KVOBolt/ReadCustomers` | 58.62 | 56.89 | -1.9% | 24 → 24 |
| `KVOBolt/WriteCustomers` | 827.2 | 858.8 | +2.9% | 8,977 → 8,977 |
| `KVOCompactBolt/ReadCustomers` | 65.16 | 65.98 | -0.4% | 24 → 24 |
| `KVOCompactBolt/WriteCustomers` | 981.0 | 1,079.8 | +1.8% | 9,384 → 9,384 |
| 4,096 / `EVM/ReadCustomers` | 431.4 | 458.5 | +3.1% | 24 → 24 |
| 4,096 / `EVM/WriteCustomers` | 15,268.8 | 15,993.8 | -5.3% | 92,747 → 92,751 |
| 4,096 / `MessagePackEVM/ReadCustomers` | 7,469.5 | 7,265.9 | -1.4% | 77,889 → 77,889 |
| 4,096 / `MessagePackEVM/WriteCustomers` | 31,063.4 | 31,168.2 | +1.8% | 285,896 → 285,894 |

Rerunning the three largest apparent slowdowns for ten 900 ms rounds gave
+0.7% (indexed KVO read), +1.8% (Fast Bolt write), and +0.6% (Compact memory
write), all flipping sign across pairs—within run noise, with a 1–2% shift
unresolved.

## Historical milestones

### Draft KVO rows

Six rotating 400 ms rounds on Apple M5 Max, `darwin/arm64`, Go 1.27.3
(baseline `b8f7487`; 512 customers, 410 updates per write transaction, Bolt
sync disabled):

| Median per 512 customers | Legacy word map | Fast | Compact |
| --- | ---: | ---: | ---: |
| Scan, memory | 42.47 µs | 41.22 µs | 50.64 µs |
| Update, memory | 321.88 µs | 259.32 µs | 399.43 µs |
| Scan, Bolt | 45.88 µs | 44.10 µs | 51.30 µs |
| Update, Bolt | 410.52 µs | 341.84 µs | 486.04 µs |
| Go heap bytes per memory update | 413,444 | 204,928 | 187,832 |
| Go allocations per memory update | 2,543 | 2,133 | 2,550 |
| Go heap bytes per Bolt update | 454,329 | 259,751 | 266,468 |
| Go allocations per Bolt update | 4,265 | 3,834 | 4,235 |
| Encoded bytes per customer (excl. DB key) | 100.8 | 54.2 | 15.1 |

In isolated Go lookup (`BenchmarkCustomerRowCodec`, 512 rows, 0 allocs): 5.52 µs
legacy, 10.32 µs Fast, 21.10 µs Compact. All three scans took 16 allocations in
memory and 22 on Bolt.

### Instruction fusion

Six paired 400 ms rounds on Apple M5 Max, Go 1.27.3 (baseline `8ead9f6`;
[samples](../bench/fusion.tsv)):

| Workload | Before | Fused | Go allocs (before → after) |
| --- | ---: | ---: | ---: |
| Price, 3,840 payments | 40.97 µs | 32.44 µs | 0 → 0 |
| Scan, 256 external calls | 4.45 µs | 4.08 µs | 0 → 0 |
| Read 512 KVO customers, memory | 47.09 µs | 44.42 µs | 16 → 16 |
| Update 410 KVO customers, memory | 345.85 µs | 348.24 µs | 2,545 → 2,543 |

Fusion reduced `Price` from 10 to 7 dispatched words per payment and each
`Scan` call from 3 to 1.

### First optimization pass

Six paired rounds on Apple M5 Max, Go 1.27.3 (baseline EDB `e01e7fb`; 450 ms
samples for memory, 350 ms for Bolt; [samples](../bench/optimized.tsv)):

| Workload | Time (before → after) | Speedup | Bytes (before → after) | Allocs (before → after) |
| --- | ---: | ---: | ---: | ---: |
| Price, 3,840 payments | 105.03 → 46.25 µs | 2.27× | 0 → 0 | 0 → 0 |
| Scan, 256 external calls | 8.95 → 4.54 µs | 1.97× | 0 → 0 | 0 → 0 |
| ReadCustomers, memory | 105.99 → 44.37 µs | 2.39× | 1,856 → 1,400 | 22 → 16 |
| WriteCustomers, memory | 685.77 → 329.07 µs | 2.08× | 839,634 → 413,845 | 13,591 → 2,545 |
| ReadCustomers, Bolt | 107.25 → 44.63 µs | 2.40× | 2,256 → 1,768 | 28 → 22 |
| WriteCustomers, Bolt | 724.69 → 418.82 µs | 1.73× | 847,852 → 454,735 | 13,674 → 4,267 |

Passing `row.Record()` directly to `KVPut` instead of `row.Record().Pack()`
accounted for 410 allocations (~9.8 kB) per write transaction.

### Early Lua comparisons (2026-11-03)

Six rotating rounds on Apple M5 Max, Go 1.27.3 ([samples](../bench/lua-comparison.tsv);
original five-sample baseline in [baseline.tsv](../bench/baseline.tsv)):

| Runtime | Price, 3,840 payments (µs) | Scan, 256 calls (µs) |
| --- | ---: | ---: |
| EVM before (`e01e7fb`) | 105.60 | 9.15 |
| EVM optimized (`8e14177`) | 47.21 | 4.53 |
| Lunar 0.1.3 | 48.15 | 8.57 |
| GopherLua 1.1.2 | 233.12 | 15.74 |
| PUC Lua 5.5.1 | 27.31 | 2.88 |
| LuaJIT 2.1.1792588164 (JIT on) | 4.64 | 2.10 |

## Run the suites

From the repository root:

```sh
go test ./bench -run '^$' -bench . -cpu 1 -count 5 -benchtime=500ms
(cd bench/compare/golua && go test -run '^$' -bench . -cpu 1 -count 5 -benchtime=500ms)
LUA_PREFIX="$(brew --prefix lua)" bench/compare/native/run.sh
LUA_PREFIX="$(brew --prefix luajit)" bench/compare/native/run.sh
```

Omit `-bench` to run the correctness checks alone.

## KVO customer scans & indexed account-state table

> **Note:** The indexed account-state figures below predate direct MessagePack
> access (2026-11-07). For current MessagePack and decode-once numbers, see
> [kvo/_docs/bench-record-access.md](../kvo/_docs/bench-record-access.md).

`BenchmarkKVO` (Fast) and `BenchmarkKVOCompact` (Compact) run the
[customer script](https://github.com/andreyvit/edb/blob/main/internal/evmbench/testdata/customers.go)
over 512 records in memory and on Bolt (410 active updates per write
transaction; original baseline was 104.28 µs / 22 allocs read, 682.43 µs /
13,591 allocs write; in the optimized memory run, 16-row and 512-row reads have
identical allocation counts).

`BenchmarkAccountState` uses 4,096 synthetic Bolt rows with one secondary index
on nested balance (3,277 active updates per write transaction; medians of five
250 ms samples on Apple M5 Max, Go 1.27.3, Bolt sync disabled):

| Stored format | Access path | Read (ms) | Read allocs | Write (ms) | Write allocs |
| --- | --- | ---: | ---: | ---: | ---: |
| MessagePack | Go structs | 5.932 | 77,882 | 24.44 | 249,563 |
| MessagePack | Go property codes | 6.267 | 77,882 | 26.19 | 285,855 |
| MessagePack | EVM property selectors | 6.886 | 77,890 | 28.39 | 286,012 |
| Fast | Go property codes (record) | 0.226 | 16 | 13.23 | 111,304 |
| Fast | Go property codes (encoded view) | 0.182 | 16 | 12.65 | 94,870 |
| Fast | EVM property selectors | 0.429 | 24 | 14.63 | 110,600 |
| Fast | Go structs, reflected binding | 4.313 | 66,821 | 22.93 | 266,721 |
| Fast | Go structs, generated binding | 2.491 | 22,726 | 17.40 | 150,181 |

### Production-shaped replay (576 rows)

With `EDB_ACCOUNT_STATE_JSON` pointing to 576 anonymized shapes from three
stores (208 active writes, six alternating 250 ms pairs vs. `fd53085`):

| Packed KVO access | Read µs (before → after) | Read allocs | Write µs (before → after) | Write allocs |
| --- | ---: | ---: | ---: | ---: |
| Go encoded view | 18.5 → 17.4 | 16 → 16 | 669 → 663 | 5,767 → 4,846 |
| Go record view | 23.3 → 22.7 | 16 → 16 | 703 → 706 | 6,808 → 5,887 |
| Go struct, reflection | 279 → 227 | 3,270 → 2,118 | 1,195 → 849 | 12,957 → 6,697 |
| Go struct, generated | 195 → 144 | 1,877 → 725 | 982 → 704 | 9,428 → 3,730 |
| EVM | 50.5 → 47.2 | 24 → 24 | 856 → 841 | 6,855 → 5,937 |

### Same writer, different indexer (576 rows, 204 active)

Comparing struct vs. KV index callbacks across six rotating 250 ms rounds:

| Writer | Struct index (µs) | KV index (µs) | Heap kB (struct → KV) | Allocs (struct → KV) |
| --- | ---: | ---: | ---: | ---: |
| KVO encoded view | 664 | 597 | 561 → 447 | 5,628 → 4,750 |
| KVO record | 695 | 668 | 615 → 501 | 6,649 → 5,770 |
| KVO EVM | 835 | 823 | 668 → 554 | 6,700 → 5,820 |
| Go struct on KVO, reflected | 821 | 821 | 496 → 496 | 6,472 → 6,472 |
| Go struct on KVO, generated | 636 | 646 | 447 → 447 | 3,637 → 3,637 |
| Go struct on MessagePack | 1,157 | 1,165 | 476 → 476 | 6,206 → 6,206 |
| MessagePack record | 1,556 | 1,562 | 807 → 807 | 11,475 → 11,475 |
| MessagePack EVM | 1,770 | 1,814 | 860 → 860 | 11,526 → 11,526 |

Full index rebuild over all 576 KVO rows (six 350 ms samples): 824 µs /
1,091 kB / 7,192 allocs with the struct callback vs. 673 µs / 911 kB /
6,516 allocs with the KV (or packed-only) callback.

```sh
# in the edb repository
go test ./internal/evmbench -run '^$' -bench '^Benchmark(KVO(Compact)?(Bolt)?|CustomerRowCodec)$' -benchmem -cpu 1 -count 5 -benchtime=500ms
go test ./internal/evmbench -run '^$' -bench '^BenchmarkAccountState$/.*/^WriteCustomers$' -benchmem -cpu 1 -benchtime=250ms -count=6
go test ./internal/evmbench -run '^$' -bench '^BenchmarkAccountStateReindex$' -benchmem -cpu 1 -benchtime=350ms -count=6
```

## Interpreter kernels

All Go engines compile the same source and all Lua engines load the same Lua
file, rotating across scenarios 0–3 and verifying every return value against
`testdata/expected.txt`:

| Entry | What one invocation does | Upstream algorithm |
| --- | --- | --- |
| **Fib** | Recursively computes F(14)–F(17) with no memoization. | [Yaegi recursive fixture](https://github.com/traefik/yaegi/blob/3fbebb36621c03b2981ccee03246ea40455bd792/_test/fib.go) |
| **Sieve** | Fills 5,000–5,093 flags and strikes multiples, including slice/table allocation. | [Are We Fast Yet sieve](https://github.com/smarr/are-we-fast-yet/blob/74306fec151070fd07157cefeacf19e7e0bcdc89/benchmarks/Lua/sieve.lua) (used in [Lunar](https://github.com/mmcdole/lunar/tree/v0.1.3)) |
| **Mandelbrot** | Counts interior pixels across four 24–30 px grids (49 iterations, no bitmap I/O). | [RaptorJIT Lua kernel](https://github.com/raptorjit/raptorjit/blob/1d83bb4ec7581cc1715725af1514297ca3f3ac64/testsuite/bench/mandelbrot.lua) |

Run individually or all engines in rotation:

```sh
(cd bench/compare/yaegi && go test -run '^$' -bench . -benchmem -cpu 1)
WORKLOADS='Fib Sieve Mandelbrot' ITERATIONS=1000 LUA_PREFIX="$(brew --prefix lua)" bench/compare/native/run.sh
LUAJIT_MODE=off LUA_PREFIX="$(brew --prefix luajit)" WORKLOADS=Fib bench/compare/native/run.sh

python3 bench/compare/kernels.py \
  --lua-prefix "$(brew --prefix lua)" \
  --luajit-prefix "$(brew --prefix luajit)" > /tmp/evm-kernels.tsv
```

Pass `--baseline-bin /absolute/path/to/old-bench.test` to include an earlier
EVM build, or `--rounds 1` for a quick smoke run.
