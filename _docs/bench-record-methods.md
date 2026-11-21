# Explicit record method calls

Measured on Apple M5 Max, `darwin/arm64`, Go 1.27.3:

```sh
go test ./evmbind -run '^$' -bench '^BenchmarkRecordMethods$' \
    -benchmem -benchtime=500ms -count=3
```

The benchmarked helper reads `earned`, `redeemed`, and `expired` properties and
returns `earned - redeemed - expired`. Each benchmark operation runs 256
calculations, and the table reports median **nanoseconds per calculation**
(amortizing VM entry). Compilation, binding, encoding, materialization, and VM
warm-up are outside the timer. These are property-access and call benchmarks,
not database scans.

| Record representation | Native Go helper | EVM registered helper | EVM inline arithmetic |
| --- | ---: | ---: | ---: |
| Fast2 | 14.58 | 28.14 | 34.55 |
| Compact2 | 28.40 | 42.24 | 47.62 |
| MessagePack | 116.60 | 132.30 | 138.70 |
| Native struct, generated fields | 9.63 | 23.91 | 30.36 |
| Native struct, reflected fields | 27.37 | 41.49 | 46.39 |
| Memory | 9.72 | 23.27 | 28.79 |

Every measured path reports **0 B/op and 0 allocs/op**, including the full
warmed EVM invocation. Tests also assert zero allocations across 1,000
registered method calls in a single invocation for all six representations.

A few clarifications:

- The **EVM registered helper** column always uses generated method bindings.
- **Reflected fields** refers to native struct property access, not reflective
  host function invocation (the reflective fallback is optional and does
  allocate).
- String results are copied at the method boundary and are outside the
  allocation-free scalar claim.

Compared with three separate EVM property reads and inline arithmetic, a
registered helper saves roughly 5–7 ns per calculation. Compared with calling
the Go helper directly, the EVM loop and host-call boundary add roughly
14–16 ns. Registration resolves the method at compile and link time while
keeping the record in its existing representation—there is no per-call
method-name lookup, full decode, boxing, or implicit conversion to a native
struct.
