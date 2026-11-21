# Native Go compilation: design proposal

> **Status:** Design proposal—not yet implemented. GoX currently compiles and
> runs through EVM. `Preprocess` produces diagnostic Go output, not a native
> compilation target, and no native generator, freshness verifier, dev watcher,
> or backend selector exists yet.

## Goal

Let an application use one set of GoX templates and host API contracts in two
explicit modes:

- **Development:** Compile edited templates through EVM inside the running
  server for instant feedback.
- **Production (optional):** Generate native Go code, compile it into the
  application binary, and select that backend explicitly.

Running GoX on EVM remains a first-class production option for apps that want
live template updates—native compilation should stay optional. Above all,
neither mode should ever silently serve stale templates when newer ones are
expected.

## One frontend, two backends

Parsing, name resolution, implicit arguments, components, typed slots,
contextual escaping, and nil-guard insertion must stay in a single shared
frontend. Its output—the rendering plan, constants, source maps, and resolved
host contracts—feeds both backends. Never reimplement GoX semantics in a
separate native text emitter.

Two gaps must be closed before native generation can work:

1. **Self-contained plans:** Today's `GeneratedSources` calls rendering
   operations by index into a separate plan, and `Builder` checks its owning
   EVM program. Native output must emit a complete, immutable plan and a native
   rendering context—copying `GeneratedSources` into a `.go` file is not
   enough.
2. **Real Go import paths:** Virtual EVM paths such as `gox` or application
   virtual packages are not necessarily real Go import paths. A host binding
   manifest must map virtual identities to real Go packages, types, functions,
   and adapters, rejecting missing mappings at generation time rather than
   guessing by basename.

## Portable language and binding contract

Define an explicit **portable subset**—the intersection of supported EVM Go and
native GoX—so native mode never silently accepts code that fails in development.
Validate portable sources through the shared frontend and EVM compatibility
checks during generation.

Host contracts must capture parameter *names* as well as types (implicit
arguments and component attributes depend on them), along with exported field
aliases, method sets, trusted-content identities, constants, and model schemas.
Treat changes to any of these as compatibility-breaking even when ordinary Go
callers would still compile.

Types that are not interchangeable across backends need explicit adapters:

- **KVO models:** Native adapters must preserve schema semantics and mutation
  rules; a similarly shaped Go struct is not sufficient.
- **Guest callables:** EVM function handles, closures, and bound methods are
  not native Go function pointers and need backend-aware adapters.
- **Ambient and interface values:** Preserve supported concrete type identities
  and reject unsupported round trips instead of stringifying or copying them.
- **Host callbacks and builders:** Preserve caller context, errors, and
  argument ownership, keeping builder lifetimes render-scoped in both modes.

Expose a backend-neutral renderer registry with stable renderer IDs, input
schemas, and invocation adapters (generated native packages can also expose
typed Go entry points). The host selects its backend explicitly at startup
rather than guessing from whichever files happen to be on disk.

## Preventing stale generated code

Generation should produce deterministic files plus a manifest hashing:

- Every source filename and full file contents (including `.go` helpers and
  imported template packages) in deterministic order—no timestamps—so adding,
  deleting, or renaming a file changes the fingerprint.
- The transitive host contract hash and binding-generator version.
- GoX language, compiler, and rendering-plan ABI versions.
- Compilation options, output format, and relevant build constraints/settings.
- Renderer IDs, input schemas, and generated artifact hashes.

Embed that fingerprint into both the generated package and application build
metadata, and enforce freshness across the release pipeline:

- **Pre-build & CI check:** Because `go build` never runs `go generate`, a
  proposed freshness command recomputes the manifest and fails on any
  difference, missing file, or leftover generated file. CI regenerates into a
  temp directory and compares byte-for-byte.
- **Single release build:** Build the source snapshot, generated files, and
  final binary in one job without overlaying a different source tree afterward.
- **Deploy & startup verification:** Check the binary's embedded fingerprint
  against the release's expected fingerprint (a self-consistent old binary is
  still old). If templates are mounted externally at startup, compare their
  fingerprint against the generated manifest and refuse mismatches. Surface the
  active backend and fingerprint in diagnostics, and roll back binary and
  manifest together.

## Development updates

An application watcher can compile and link a fresh EVM template bundle in the
background and swap it in atomically, letting in-flight renders finish on the
old bundle (never mixing a new module with an old plan).

If an edit fails to compile, report diagnostics and keep the last valid bundle
only under an explicit dev policy—displaying that it is stale alongside both
fingerprints—and never silently fall back to generated native code. Watching
and publication policy remain the host application's responsibility.

## Behavioral parity and honest limits

For portable templates, both backends must agree byte-for-byte on output,
contextual escaping, attribute handling, evaluation order, implicit arguments,
slot captures, nil diagnostics, and source locations, verified by running the
same conformance fixtures (including malformed data and failure paths) through
both.

Execution controls, however, are not automatically identical: EVM instruction
budgets and cancellation checks do not exist in plain native Go loops. Before
shipping native mode, either add explicit native instrumentation or document a
trusted-code-only native mode with weaker interruption guarantees—never imply
an enforceable CPU deadline just because a `context.Context` is passed. Output
byte limits can be shared in `Builder`, and portable error/panic semantics must
be specified honestly without pretending native crashes are recoverable VM
errors.

## Implementation roadmap

1. Freeze the portable subset and build shared backend conformance fixtures.
2. Extract the complete frontend result and version its rendering-plan ABI.
3. Define native binding manifests and backend-neutral renderer registration.
4. Emit native Go and immutable plans; verify parity across the portable subset.
5. Add deterministic manifests, freshness checks, and release verification.
6. Add dev bundle swapping, explicit backend selection, and documented
   execution limits before declaring native mode production-ready.

**Open decisions:** generated package layout, build-tag vs. explicit registry
selection, native loop instrumentation, and how apps supply their expected
release fingerprint. Settle these against a real host integration rather than
hiding them behind an automatic dev/prod heuristic.
