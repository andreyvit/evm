# Scalar scratch-register experiment

This directory contains the harnesses used to evaluate whether keeping hot loop
variables in local Go registers speeds up EVM loops. Hand-written schedules
exist only in temporary benchmark overlays for `Price`, `Mandelbrot`, and
`Sieve`; the production linker never inspects fixture names, hashes, or
hardcoded schedules.

For the recorded numbers—including routines the linker declined—see
[Scalar scratch registers (2026-11-04)](../../../_docs/benchmarks.md#scalar-scratch-registers-2026-11-04).

## Compare the automatic linker against the original evaluator

Run from the repository root so both binaries execute the exact same tests and
inputs:

```sh
trial=$(mktemp -d)
mkdir -p "$trial/base-src" "$trial/origin" "$trial/ship"
git archive 8a35218 | tar -x -C "$trial/base-src"
cp bench/math_test.go bench/gonum_test.go "$trial/base-src/evm/bench/"
cp -R bench/internal/mathwork "$trial/base-src/evm/bench/internal/"
(cd "$trial/base-src" && go test -c -o "$trial/origin/bench" ./bench)
go test -c -o "$trial/ship/bench" ./bench
measure=bench/compare/scratch/measure.py
python3 "$measure" "$trial" origin,ship Price,Mandelbrot,Fib,Sieve,Scan
python3 "$measure" "$trial" origin,ship Math/Filter/8,Math/Filter/512,MathBuild/Compile,MathBuild/Link
python3 "$measure" "$trial" origin,ship KVO/ReadCustomers,KVO/WriteCustomers,AccountState/EVM/ReadCustomers/SetIndexer
```

### Upstream Gonum corpus (optional)

You can also test unmodified functions from a pinned Gonum checkout without
adding any dependency to `go.mod`. The first cohort has ten scalar reductions
(dot products, norms, distances, sums); the second has five destination-writing
transforms. Run both cohorts, including functions the linker declines:

```sh
go mod download gonum.org/v1/gonum@v0.16.0
export EVM_GONUM_DIR="$(go env GOMODCACHE)/gonum.org/v1/gonum@v0.16.0"
go test ./bench -run 'Test(Math|Gonum)Kernels' -v
go test ./bench -run '^$' -bench '^Benchmark(Gonum|Math)' -cpu 1 -benchmem
python3 "$measure" "$trial" origin,ship Gonum/DotUnitary,Gonum/Sum,Gonum/L1Dist,GonumTransform/AxpyUnitaryTo
```

Each kernel compiles verbatim from Gonum's source (for example,
[dot.go at v0.16.0](https://github.com/gonum/gonum/blob/v0.16.0/internal/asm/f64/dot.go)),
while the local harness supplies inputs and checks every output cell before and
after timing.

### Lua comparison

The native Lua harness checks all four inputs per workload (and verifies trace
compilation when LuaJIT tracing is enabled). Remember that native `Scan`
measures C callbacks, whereas Go benchmarks measure Go callbacks:

```sh
native=bench/compare/native/run.sh
LUA_PREFIX="$(brew --prefix lua)" "$native" --build "$trial/lua"
LUA_PREFIX="$(brew --prefix luajit)" "$native" --build "$trial/jit"
python3 "$measure" "$trial" ship,lua,jit-off,jit Price,Mandelbrot,Fib,Sieve,Scan
```

## Recreate the hand-scheduled variants

`build.py` generates opcode definitions, operand checks, the dispatcher, and
disassembly via `go -overlay`, verifying all workload results after each build:

- **Float variants (`f4`):** Destructive (`dest`, where destination is also the
  first input), three-operand (`three`), and mixed (`mixed`, at most one
  ordinary frame register in any operand slot).
- **Integer variants (`u2`, `u4`):** Separate signed/unsigned division,
  comparisons, and right shifts.
- Schedules stop at function calls. Pass `--slim` to emit only the handlers
  used by the trial (isolating switch-size overhead).

```sh
experiment=bench/compare/scratch
export SCRATCH_EDB="$trial/base-src"
python3 "$experiment/build.py" "$trial/none-dest" none dest
for form in dest three mixed; do
    python3 "$experiment/build.py" "$trial/f4-$form" f4 "$form"
done
for bank in u2 u4; do
    python3 "$experiment/build.py" "$trial/$bank-mixed" "$bank" mixed
done
python3 "$measure" "$trial" none-dest,f4-dest,f4-three,f4-mixed Price,Mandelbrot,Scan
python3 "$measure" "$trial" none-dest,u2-mixed,u4-mixed Sieve,Fib,Scan
```

Early hand schedules expanded existing multiply-add and loop superinstructions.
To separate fusion loss from scratch-register overhead, rerun with
`--island --fused` and `--island --fused --plain` (ordinary frame operands).
The production linker preserves the intermediate float rounding of those
superinstructions.

## Inspect instruction counts and assembly

Count instructions and inspect binary sizes in separate builds so timing runs
never include counter overhead:

```sh
python3 "$experiment/build.py" "$trial/counted" f4 mixed --counts
(cd bench && "$trial/counted/bench" -test.run '^TestScratchCounts$' -test.v)
python3 "$experiment/count.py" "$trial/counted-current"
go tool nm -size "$trial/ship/bench" | grep -E '\(\*VM\)\.execute$|\(\*floatLoop\)\.run$'
go tool objdump -s '\(\*floatLoop\)\.run$' "$trial/ship/bench"
```

The counter reports workload, scenario, ordinary dispatches, private steps,
folded loop-control words, scratch loads, final stores, scratch moves, and
extra frame spills. Original metered instructions equal
`ordinary dispatches + private steps + folded words - extra spills`. Scratch
loads and final stores are setup/teardown with zero guest instruction cost, and
any stack spills chosen by the Go compiler are separate from explicit frame
spills in the plan.
