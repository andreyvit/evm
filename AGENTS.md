# Repository guide

EVM runs a supported subset of Go inside a Go application. GoX compiles
Go-with-HTML templates to EVM programs. KVO gives typed access to records.

## Layout

- Root `evm`: VM, linker, standard library, host calls and record support.
- `evmcompiler`: Go source to bytecode. `evmasm`: assembler for tests and diagnostics.
- `evmbind`: host API bindings and their generator. `evmruntime`: guest-visible runtime types.
- `evmstd`: standard packages for scripts; `go run ./internal/genstd` regenerates its declarations, bindings and `API.md`.
- `gox`: Go/markup templates. `cmd/gox`: syntax check and formatter. `_editors`: editor support.
- `kvo`: models, typed properties, records, collections, editors and Go bindings.
- `values` / `nativebind`: shared runtime values and native field operations.
- `kvo/codec/row2`: Fast2/Compact2. `fast1` and `msgpack`: supported legacy codecs.
- `examples`: runnable examples, one directory each, part of this module with no dependencies of their own.
- `bench`: benchmarks. `bench/compare`: other engines, each in its own module.
- `internal`: generators and test fixtures.

Each package imports only packages below it; see Packages in `README.md`.
Never import `github.com/andreyvit/edb`: edb depends on this module. Tests that
need a real database belong in edb.

Keep tests beside code. Keep package entry docs in `README.md`; other Markdown
belongs in root `_docs/` or `<component>/_docs/`. Use descriptive filenames.
Docs state current contracts; dates belong on measurements and audit evidence.

## Tests

```sh
go test ./...
go test -run TestName ./...
go test -coverprofile=cover.out ./...
go tool cover -html=cover.out -o cover.html
```

If cache permissions block coverage, prefix Go commands with
`env GOCACHE="$PWD/.gocache"`.

Rebuild book examples with `go run ./EVMBook-rebuild.go`.

## Generated code

Tests compare checked-in bindings with fresh generator output byte for byte and
fail with "regenerate ... bindings". Fingerprints cover import paths and bound
declarations, so both invalidate them.

- Model fixtures: `go generate ./internal/... ./evmbind/internal/methodfixture ./nativebind/internal/fixture`.
- `evmbind/internal/*bindings` with a `generate.go`: run `go run generate.go` in that directory.
- Other binding directories have no generator program; write the bytes the failing test computes.

## Code

Run `gofmt`. Use idiomatic Go names. Use named enum types/constants for modes,
states and kinds, preferably with `String()`. Preserve numeric wire values when
replacing discriminators.
