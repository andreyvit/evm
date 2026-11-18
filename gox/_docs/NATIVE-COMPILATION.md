# Native Go compilation: proposed compatibility contract

Status: design proposal. GoX currently compiles and runs through EVM.
`Preprocess` exposes diagnostic Go output, not a native compilation target.
No native generator, freshness verifier, dev watcher or automatic backend selector
is implemented by this proposal.

## Goal

Use one set of GoX sources and host API contracts in two modes:

- Development: compile edited templates through EVM inside the running host.
- Production: generate native Go, build it into the application, and select that
  implementation explicitly.

EVM rendering remains a valid production choice for applications that need live
updates. Native compilation should be optional, not a new prerequisite for GoX.
Neither mode should silently serve an older template when a newer one is expected.

## One frontend, two backends

Keep parsing, name resolution, implicit arguments, components, typed slots,
contextual escaping and nil-guard insertion in one frontend. Its result must
include the rendering plan, constants, source maps and resolved host contracts.
Both backends consume that result. Do not independently reimplement these rules
in a native text emitter.

The current generated Go calls operations by index. Their meaning lives in the
separate rendering plan, and Builder operations currently verify their owning
rendering program. Native generation must emit a complete immutable plan and
provide a supported native rendering context; copying GeneratedSources into a
.go file is insufficient.

Generated files need real Go import paths. EVM paths such as `gox` and application
virtual packages are not necessarily native import paths. A host binding manifest
must map virtual identities to native packages, types, functions and adapters.
Reject missing mappings during generation. Never guess a native path by basename.

## Portable language and binding contract

Define a documented portable subset: the intersection of supported EVM Go and
native GoX behavior. Native mode must not quietly accept language features that
fail during development. Validate portable sources through the shared frontend
and EVM compatibility checks during generation.

Host contracts must include parameter names as well as types: implicit arguments
and component attributes depend on those names. Include exported field aliases,
method sets, trusted-content identities, constants and model schemas where they
affect compilation. Treat changes to these as compatibility changes even if an
ordinary Go caller would still compile.

Bindings need explicit policies for values that are not interchangeable:

- KVO model selectors and record operations need native adapters that retain
  their schema and mutation rules; a similarly shaped Go struct is not enough.
- Guest function handles, closures and bound methods need backend-aware adapters.
  An EVM function value is not a native Go function pointer.
- Ambient values and interface payloads must preserve supported concrete type
  identities. Reject unsupported round trips rather than stringify or copy them.
- Native host callbacks must preserve caller context, errors and argument
  ownership. Builder lifetimes remain render-scoped in both modes.

Build a backend-neutral renderer registry with stable renderer IDs, input schemas
and invocation adapters. Generated native wrappers can additionally expose typed
Go entry points. The host selects a backend at initialization; it does not infer
one from whichever files happen to be present.

## Prevent stale generated output

Generation produces a manifest and deterministic files. The manifest includes:

- Every source filename and content hash, including ordinary Go helpers and
  imported template packages, in deterministic order.
- The transitive host contract hash and binding-generator version.
- GoX language, compiler and rendering-plan ABI versions.
- Compilation options, output format and relevant target/build settings.
- Renderer IDs, input schemas and generated artifact hashes.

Hash full source contents, not timestamps. Include the complete filename set so
adding, deleting or renaming a file changes the fingerprint. Build constraints
and generated host schemas must participate in dependency discovery. Embed the
fingerprint into the generated package and application build metadata.

The production build must verify freshness before compiling the application.
A proposed check command recomputes the manifest and fails on any difference,
missing generated file or unexpected leftover generated file. CI also regenerates
into a temporary directory and compares deterministic output. `go build` does not
run `go generate`; a generate directive alone is not an enforcement mechanism.

Build the source snapshot, generated files and final binary in one release job.
After verification, do not overlay a different source tree onto that build.
Deployment must check the artifact fingerprint against the release's expected
fingerprint. A self-consistent old binary is still old; checking only its embedded
manifest cannot detect that the wrong release was deployed.

At startup, if templates are mounted externally, compare their fingerprint with
the generated manifest and refuse a mismatch. If production ships no sources,
use the release manifest and deployment check. Expose the selected backend and
fingerprint in diagnostics. Keep rollbacks explicit and roll back the matching
binary and manifest together.

## Development updates

An application watcher may compile and link a complete new EVM template bundle
in the background, then atomically replace the active bundle. In-flight renders
keep the old bundle until they finish; never mix a new module with an old plan.

On an invalid edit, report the new diagnostics and keep the last valid bundle only
under an explicit development policy. Display that it is stale, together with
both fingerprints. Do not silently fall back to generated native code. Watcher
integration and publication policy belong to the host application.

## Behavioral parity and deliberate limits

For portable templates, both backends must agree on output bytes, contextual
escaping, attribute handling, evaluation order, implicit argument selection,
slot captures, nil diagnostics and source locations. Run the same rendering
fixtures through both backends, including malformed data and failure paths.

Execution controls are not automatically equivalent. EVM instruction budgets
and cancellation checks do not appear in ordinary native Go loops. Before native
mode is released, choose either explicit native instrumentation or a documented
trusted-code-only native mode with weaker interruption guarantees. Do not promise
an enforceable CPU deadline merely because a context is passed to native code.
Output limits can be shared in the builder. Also specify which panic and error
behavior is portable; avoid pretending native crashes are recoverable VM errors.

## Bounded implementation sequence

1. Freeze portable semantics and build shared backend conformance fixtures.
2. Extract the complete frontend result and version its rendering-plan ABI.
3. Define native binding mappings and backend-neutral renderer registration.
4. Emit native Go and immutable plans; verify parity for the portable subset.
5. Add deterministic manifests, freshness checks and release verification.
6. Add development bundle replacement and explicit backend selection, then
   document supported execution limits before calling native mode production-ready.

Open decisions: generated package layout, build-tag versus explicit registry
selection, native execution instrumentation, and how applications supply their
expected release fingerprint. Those choices should be settled with a working
host integration, not hidden behind an automatic dev/prod heuristic.
