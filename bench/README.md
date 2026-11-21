# EVM benchmarks

EVM separates execution, allocation, and build-cost benchmarks so each number
measures one thing cleanly. Compilation, linking, and warm-up always stay
outside execution timers. Benchmarks that run scripts against real EDB tables
live in the `edb` repository under `internal/evmbench` (those include
transaction and cursor work—always check each report's sync and indexer
settings before comparing).

## Quick start

```sh
go test ./bench
go test ./bench -run '^$' -bench . -benchmem -cpu 1 -count 5 -benchtime=500ms
```

**Good benchmarking hygiene:**

- Run competing binaries sequentially with identical inputs and alternate their
  order across rounds.
- Keep the machine idle—never benchmark alongside other tests or builds.
- Report slowdowns and unchanged control workloads alongside speedups.
- Remember scope: a zero-allocation property read does not mean a
  zero-allocation database transaction.

## Reports

- [Execution and interpreter comparisons](../_docs/benchmarks.md)
- [Host bindings and direct calls](../_docs/bindings.md#measurements)
- [Explicit record methods](../_docs/bench-record-methods.md)
- [Borrowed KVO list cells](../_docs/list-cells.md)
- [KVO access strategies and decoding](../kvo/_docs/bench-record-access.md)
- [GoX template benchmarks](../gox/_docs/benchmarks.md)
- [Scratch-register experiment and reproduction](compare/scratch/README.md)

Every result belongs to its recorded commit, hardware, and workload. Historical
adapter or codec names in older reports do not describe the current public API.

## Comparing other interpreters

Pure-Go Lua comparators (`Lunar` and `GopherLua`) live in their own Go module
so they add no dependencies to `evm`:

```sh
(cd bench/compare/golua && go test -run '^$' -bench . -benchmem -cpu 1 -count 5)
```

For native C Lua and LuaJIT on macOS (via Homebrew):

```sh
LUA_PREFIX="$(brew --prefix lua)" bench/compare/native/run.sh
LUA_PREFIX="$(brew --prefix luajit)" bench/compare/native/run.sh
```

The native runner builds a temporary executable and times 20,000 invocations
per sample (override with `SAMPLES` and `ITERATIONS`). Keep in mind that C
callbacks and Go callbacks have different crossing costs—always report that
boundary alongside the numbers.
