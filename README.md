# EVM

Run Go plugins inside a Go application. Compile against the APIs you expose,
link their implementations, then create a VM session. Swap linked programs when
you activate a new plugin; existing sessions retain their program.

EVM supports sequential Go: structs, collections, closures, interfaces,
concrete generic instances, range functions and defer/panic/recover. It excludes
goroutines, channels, `unsafe` and arbitrary native imports. Source is limited
to 1 MiB. Unsupported constructs fail compilation.

The repository holds two more parts:

- [GoX](gox/README.md): typed HTML templates. Write HTML inside Go functions,
  compile through EVM, and recompile while the server keeps running.
- [KVO](kvo/README.md): typed data records. Read and edit typed properties of
  compact binary rows or native Go structs without decoding the whole row.

Requires Go 1.27+.

```sh
go get github.com/andreyvit/evm
```

## Packages

![Package layers: gox on top, then evmbind, evmcompiler and evmasm, evm, kvo, and values at the bottom](_docs/packages.svg)

Each package imports only packages below it. `evm` imports `kvo` for record
support; `kvo`, `nativebind` and `values` never import the VM, so a host can use
the data model without it.

| Package | Imports |
|---|---|
| `gox` | `evm`, `evmbind`, `evmcompiler`, `evmruntime`, `values` |
| `evmbind` | `evm`, `evmcompiler`, `kvo`, `nativebind`, `values` |
| `evmcompiler` | `evm`, `kvo`, `nativebind`, `values` |
| `evmasm` | `evm`, `values` |
| `evm` | `kvo`, `kvo/codec/row2`, `nativebind`, `values` |
| `kvo/codec/fast1` | `kvo` |
| `kvo` | `kvo/codec/msgpack`, `kvo/codec/row2`, `nativebind`, `values` |
| `kvo/codec/row2`, `kvo/codec/msgpack` | `values` |
| `nativebind` | `values` |
| `values`, `evmruntime` | nothing |

## Compile, link, run

```go
module, err := evmcompiler.Compile("bonus.go", `
func Bonus(points int64) int64 {
    if points < 1000 { return 0 }
    return points / 10
}`)
if err != nil { return err }
program, err := evm.Link(module)
if err != nil { return err }
vm := evm.New(program)
var out [1]values.Value
err = vm.Run(ctx, "Bonus", []values.Value{values.Int(1500)}, out[:])
if err != nil { return err }
bonus := out[0].AsInt() // 150
```

A linked program is immutable and shareable. A VM owns registers and globals;
it is neither concurrent nor reentrant. Initialization runs once before the
first entry. Later calls retain globals. `Reset()` clears session state and
makes the next call initialize again. Execution failure invalidates the session
until reset. Results are written only on success; host side effects are not
rolled back by the VM.

Arguments/results use [`values.Value`](values/README.md). Their types come
from the entry signature. Keep program-local interfaces and function references
with their program; repack them through the destination program's APIs when
crossing that boundary.

## Host APIs

Use [`evmbind`](evmbind/README.md) for a Go API package:

```go
module, err := evmcompiler.Compile("plugin.go", source, schema.Package())
if err != nil { return err }
program, err := evm.Link(module, schema.Bind(host))
```

`schema.Package()` supplies declarations; `schema.Bind(host)` supplies their
implementations. Reuse the schema so nominal type identities match. Generated
bindings call Go directly; reflection is the fallback. Native pointers retain
their identity and mutations. The host must synchronize concurrent access.

`Package.Values` exposes host-owned bindings such as `db.Customers`. Each
`ExternalValue` declares its type and a `Read` callback that returns the value.
Scripts can copy it and call its methods. They cannot replace or take the
address of the package binding; a returned pointer still retains its normal
aliasing and mutation behavior. Reads use the host-call ABI and must not
invalidate borrowed records or change transaction scopes. These bindings
appear as variables in generated package documentation.

Manual APIs use `Package.Types`, `Functions` and `Methods`; see
[binding declarations](_docs/BINDINGS.md). Only supplied exports satisfy imports.
A `Call` and its argument/result windows are borrowed for that callback. Read
arguments before overwriting overlapping result slots; never retain the windows.
Host callbacks must honor cancellation themselves.

For runtime native closures, cache `NewNativeFunctionType(signature)` with the
bindings, then use `Call.NativeFunction(type, closure)` to produce a typed
function value. This supports returned iterators, yield functions and bound
host state without changing the linked program. Guest calls, deferred calls and
`Call.RunCallbackArgs` accept these values and share invocation limits. A native
function value belongs to its creating program; another program cannot call it.
Its captured host state can outlive an invocation, so the host must enforce any
borrowed transaction lifetime. Never capture the creating `Call`. The function
holder allocation is metered; native closure bodies and captured allocations
remain the host's responsibility. A nil native function produces a nil value.

For a host callback passed into the initial invocation, use
`Program.NativeFunction(type, closure)` and pass the returned value to `Run`.
This supports streaming row writers and yield callbacks supplied by the host.
The host owns the input-holder allocation; execution uses the VM's invocation
limits and propagates callback errors. Use `Call.NativeFunction` for functions
created during execution so their holder allocations are metered.

To retain a callable beyond its current invocation, use
`Program.FunctionValue(value)` and keep the returned `*FunctionHandle`.
`VM.RunFunction` and `RunFunctionWithBudget` accept guest closures, bound
methods, static native functions and runtime native closures. The handle keeps
captures alive without copying them and rejects invocation in another program.
The receiving VM supplies globals and initialization; mutable captured state
remains shared even across VMs or a VM reset. Synchronize concurrent calls and
validate any captured host resource lifetimes. Signature inspection returns a
copy. `NamedFunctionValue` remains the stricter public, noncapturing entry API.

`TypeDecl.NativeCodec` supplies contextual conversion for native field values
whose representation differs from their VM type. Decode and Encode receive the
current `Call` for program checks and allocation accounting. They must leave its
argument/result window unchanged and must not retain it. Native field access,
address loads/stores and struct snapshots use these converters. Linked programs
copy codec declarations; conflicting declarations for one type fail linking.

A package may also supply Go `Source`. Its shared scalar types and methods can
compile as guest code; see [shared scalar source](#shared-scalar-source).
`PackageSource`, `PackagePaths` and `PackagesSource` print the script-facing API
for tooling or an agent writing a plugin.

Use `evmcompiler.DeclarationOptions.Annotations` to select application-specific
function annotations when printing declarations. The callback receives the
package path, function name, and raw declaration comments, and returns comment
lines (including their `//` prefix). Call `options.PackageSource(...)` to apply
it. Native bindings retain comments in `ExternalFunction.Doc`; source-package
comments are read from their declarations. The default printer adds no
application annotations, and EVM does not interpret them.

## Limits and errors

`VM.Limits` sets session limits. `RunWithBudget` sets invocation limits, including
initialization. Zero fields select defaults or inherit the session limit.
`Budget.Duration` and the context bound elapsed time cooperatively; native calls
must return or observe cancellation. Instruction, depth, register, allocation
and output limits cover VM execution. Allocation accounting is cumulative until
reset and includes transient capacity; it is not live heap size. Native APIs
remain responsible for their own work and allocations.

Script panics and language errors can be recovered by a direct deferred
function. Uncaught errors retain source positions and unwrap their cause.
Budget, cancellation, invalid-value and native callback errors are fatal and skip
script defers. Native Go panics propagate after VM cleanup. The host owns rollback
and resource cleanup.

Bytecode and native bindings are trusted implementation inputs. Link validates
operands and layouts; it cannot prove arbitrary pointers safe. Accept script
source through the compiler, not uploaded raw bytecode. EVM is not a process
isolation boundary.

## Standard library and output

Imports expose selected `fmt`, `log`, `math`, `strings`, `strconv` and `errors`
APIs. Unlisted symbols fail type checking. Formatting supports flags, aggregates,
custom formatting methods and wrapped errors. `errors.As` is not exposed.
`log` has no process-exit or configuration APIs.

`VM.Output func(string) error` receives print/log output; nil uses Go's logger.
Sink errors abort execution. `Limits.OutputBytes` defaults to 1 MiB and caps both
a formatted value and total output per invocation, including initialization.
Library callbacks share the VM's execution limits.

## KVO records

Create a model package once:

```go
models, err := evm.ModelPackage("m", schema)
```

Model declarations may call `ModelBuilder.SetRowName("TenantSettingsRow")` to
set the generated row type name. Without an override, the model's schema name
supplies the usual exported Go name. Mutable types append `Edit`. Duplicate
or invalid names fail package creation. The override does not alter stored
model identity or property codes.


A customer model exposes `m.Customer` and `m.CustomerEdit`. Selectors come from
the model, including local and qualified aliases. The host supplies lookup,
scan and save operations; model registration does not grant database access.

A cursor wrapper can call `call.SetRecordResult(model, cursor.Record())`.
Immutable records travel as 16-byte pairs, including through `any`. Field reads
use their shared representation metadata; they need no read-record arena or
expiry check. Generated and reflected native records use the same script API.
Use [registered record methods](evmbind/README.md#explicit-record-methods) to
expose a Go helper as `customer.PointBalance()`.

Mutable editors require `evm.WithRecords(ctx)` inside the transaction. Register
an editor through `scope.EditRecord`; recover/check it through
`scope.MutableRecord` in the save wrapper. Flush pending materialized collections
before saving. Saving and committing remain host operations.

`ToJSON()` returns detached JSON-compatible data. It traverses records directly,
preserves integer precision and invokes custom JSON/text marshalers at scalar
leaves. It does not serialize and reparse the whole record. `Program.JSONValue`
and `Call.JSONValue` convert VM `any` to an owned Go tree; numbers are
`json.Number`. The call form charges the allocation budget. Their byte limit
counts equivalent escaped JSON output. `EncodeJSON` writes actual JSON bytes.

## Declaring borrowing effects

Host declarations can make ownership analysis more precise:

```go
readEffects := &evm.OwnershipEffects{
    NoRetain: true, NoInvalidate: true,
    Borrowed: []evm.BorrowSource{{Result: 0, Domain: "db.tx", ScopeParameter: 1}},
}
endEffects := &evm.OwnershipEffects{
    NoRetain: true,
    Ends: []evm.ScopeEnd{{Domain: "db.tx", ScopeParameter: 1}},
}
writeEffects := &evm.OwnershipEffects{
    NoRetain: true,
    Callbacks: []evm.ScopedCallback{{Argument: 1, Domain: "db.tx"}},
}
```

Attach these to `ExternalFunction.Ownership` or `ExternalMethod.Ownership`.
Results and callback arguments are zero-based; scope parameters are one-based,
including a receiver, with zero meaning the enclosing scope. Only promise
`NoInvalidate` when the operation cannot close or replace existing borrowed
storage. A cursor advancing to another transaction must not promise it.
`NoRetain` forbids keeping arguments beyond the synchronous call. Effects are
part of the compiled binding contract: Link rejects mismatches.

Register another borrowable host type with `values.NewOwnedType` and its
budgeted ownership callback. Immutable record types provide this automatically.
Unknown calls conservatively retain arguments and invalidate live borrows.
Simple read-only guest helpers are analyzed. Scope-local flat arrays, slices
and maps remain borrowed when all aliases stay local and no call invalidates
the scope. Other containers own their borrowed leaves when inserted; opaque
borrowed types copy at acquisition. `Function.OwnershipSites` explains inserted
operations. Scope matching treats all potentially aliased instances
of a domain conservatively; nested callbacks remain safe through leaf copying.

Read-only KVO handles represent snapshots, not stable database identity. Owning
a snapshot can change its representation/address; use an explicitly carried
primary key for identity or map keys that must survive an ownership boundary.
Explicit Go struct copies (`copy := *row`) allocate VM struct storage and own
their payload; ordinary handle assignment and interface packing do not box.
Returned strings are conservatively copied even when their originating record
was already owned. Materialized collection fields own their contents.

Raw Go access and handwritten bytecode do not get compiler ownership analysis.
Use `kvo.Record.Own()` before retaining encoded bytes beyond a transaction.
Host effects must describe actual invalidation; no runtime lease or per-record
transaction bookkeeping can repair an incorrect declaration.

## Shared scalar source

A source package can bind bodyless function declarations through matching
`ExternalFunction` entries. Generic declarations use `Instantiate`: it receives
the concrete VM type arguments and returns a concrete signature and native
call. The compiler checks that signature against the instantiated Go declaration;
the linker binds it again and checks the import signature. The factory must be
deterministic, reject unsupported type arguments, and return no further factory.

Fixed-receiver generic source methods use `ExternalMethod.Instantiate`, returning
a receiver-free signature. The native call receives the receiver in argument
zero as usual. Inference, explicit type arguments, function and method values,
and method expressions use the same specialized bindings.

`TypeDecl.SourceName` maps a source-declared type to a native shape. For a generic
type, `TypeArgs` selects a concrete instance using existing VM type identities.
For example, `SourceName: "Index", TypeArgs: []*values.Type{values.StringType}`
maps `Index[string]`; register its pointer shape separately as an unnamed type.
The compiler checks the source fields against the supplied shape. Native-backed
fields and guest allocations use the shape's native layout.

Methods on generic receivers need a bodyless source declaration, such as
`func (i *Index[K]) Read() K`, and an `ExternalMethod` for each concrete
receiver. The source signature supplies type substitution; bindings are kept
separate by receiver type. These methods support direct calls, method values,
method expressions and interface dispatch. They do not use `Instantiate`.

Source declarations can share a package with native types, records, values,
functions, and methods. Such mixed packages must contain declarations only;
function bodies belong in separate source packages. Declare method receivers in
source and map their native shapes with `SourceName`. Native declarations are
seeded before source checking. A native `TypeDecl.Name` struct may contain or
embed a registered source generic instance, including an instance parameterized
by that struct. The compiler fills these native fields after binding the source
instances, then validates their constraints. Registered instance arguments are
resolved before their consumers; registration order does not define type identity.

While the package source is being checked, those deferred native structs do not
yet have fields. Source declarations therefore cannot use their fields in constant
expressions, use them as map keys, or use them as arguments to layout-dependent
constraints such as `comparable`. Declare such types in source and register their
native layouts through `SourceName` instead. Consumer scripts see the completed
native structs and can use their fields and constraints normally.

A package can provide `Source` instead of native declarations. Its functions
and methods compile as guest Go code. `SharedTypes` preserves a native scalar's
type identity across host signatures and KVO fields:

```go
idType := values.NewType("ids.ID", values.Raw)
ids := evm.Package{
    Path: "ids",
    Source: idsSource, // the same Go scalar/method source used by the host
    SharedTypes: []evm.SharedType{{
        Name: "ID", Native: reflect.TypeFor[ID](), Type: idType,
    }},
}
```

Register `ids` with the model package for compilation and linking. A KVO word
whose native type is `ID` appears as `ids.ID`, including inside collections.
Its methods work even when the script imports only the model package. Shared
source must use supported EVM imports and cannot declare globals or init
functions. The shared type's scalar kind must match its native declaration.
Keep host-specific integrations in separate files; embed the shared source.

## Internals and tests

[Inside EVM](_docs/EVMBook.md) covers values, instruction encoding, compilation,
calling conventions, the evaluator and optimization. [`evmasm`](evmasm) is the
assembler for tests and diagnostics; bytecode has no persisted compatibility
format. [Benchmarks](bench/README.md) separate execution, compilation and database
costs.

```sh
go test ./...
go run ./EVMBook-rebuild.go
```

## Execution coverage

Create `coverage := evm.NewCoverage(program)` and call
`vm.SetCoverage(coverage)` before execution. `SetCoverage(nil)` disables it.
Guest calls and synchronous native-to-guest callbacks share the collector.
Collectors belong to one linked program and are not concurrent: collect each
concurrent run separately, then `Merge` after the runs finish.

`Instructions` returns counts, conditional jump outcomes, and source positions,
including instructions never executed. `Lines` groups these by source file and
line. A line is complete only when all its instructions and both outcomes of
its conditional jumps were observed. Source positions without a line number
are excluded. These are bytecode-level diagnostics; host code and instructions
removed by compilation are not measured.

Loop fusion can duplicate a source condition into an entry test and a back
edge. Source reports combine their normalized outcomes; raw counts retain
both machine branches. Proven constant entry guards of unrolled loops have
no source coverage obligation; raw counters retain them. Boolean conditions
map to their use sites, including repeated tests of the same parameter.
This metadata does not change execution.

Coverage selects a generated interpreter at invocation entry. The ordinary
interpreter has no per-instruction coverage checks. Fused operations remain
fused; their source positions attribute the execution. Fused comparisons also
record the consumed jump's outcome. Coverage bypasses float-loop acceleration
to observe each iteration. Regenerate with `go generate ./evm`; a test rejects
stale generated code.

## License

Copyright (c) 2026 Andrey Tarantsov. Published under the [Zero-Clause BSD license](LICENSE).
