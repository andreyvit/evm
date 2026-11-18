# Source-defined host bindings

`New(guestPath, source, options)` builds a schema from the Go source of a
package. There is one way to supply source: an `fs.FS` holding the package's
`.go` files, usually the package's own files through `//go:embed`.

```go
//go:embed app.go
var source embed.FS

schema, err := evmbind.New("app", source, evmbind.Options{
    Functions: map[string]any{"Pluralize": Pluralize, "Discount": Discount},
    Types:     []reflect.Type{reflect.TypeFor[Store](), reflect.TypeFor[User](), reflect.TypeFor[Cart]()},
})
```

By default the schema exposes everything the source exports: every type with
its exported fields and methods, every package function and every constant.
Function bodies, unbound variables, unexported declarations, unexported struct
fields and `_test.go` files are ignored, so real implementation source works
as is.
The source supplies declarations, parameter names and doc comments; it is not
executed. See the [runnable example](../examples/gox-basic).

Go cannot look a function or a type up by name, so the host supplies them:

- `Options.Functions` maps each exported package function to its Go value.
  A function can instead be a method of `Options.Host` taking a leading
  `*evm.Call`; `schema.Bind(host)` then supplies the receiver.
- Native types are found by reflection through bound function signatures,
  method signatures and struct fields. `Options.Types` adds types that nothing
  leads to. Concrete context types register with `Pointer[T]()`.

- `Options.Values` maps an exported package variable to its Go value. Only
  bound variables are exposed, and the source must declare their types.
  Scripts read a variable and call its methods; they cannot assign it.

Construction fails, naming the declaration, when an exported function has no
binding, a type has no native Go type, or a declaration uses a type the
binding does not support. `Options.Omit` leaves declarations out: a type,
function or constant by name, a method as `Type.Method`. A type that an
exposed declaration still refers to stays. `Options.Methods` selects methods
per type; `Options.Fields` and `Options.OmitFields` select fields.

Compile scripts against `schema.Package()`. For each distinct host receiver,
link the module against `schema.Bind(receiver)`; use `Bind(nil)` without one.
Create as many sessions from a linked program as needed. A schema owns type
identities. A program owns the numeric dynamic type IDs used during execution.

Keep the schema in a host-level `sync.OnceValue`. Parsing, type checking, enum
extraction and native layout binding then happen once per binary, while every
environment reuses `schema.Package()` and its type identities.

## Expose part of a package

Set `Options.Only` to pick declarations out of a larger native package without
duplicating them. The schema then exposes the listed types and the local types
they reference, the methods listed in `Options.Methods`, the functions bound
in `Options.Functions` and `GenericFunctions`, and the typed constants of
exposed types. `Options.Selective` does the same with every exported type.
Complete constant blocks are checked so `iota`, omitted expressions and
aliases keep their native values; constants of unrelated types are not
exported. Struct pointers retain the native layout. Exported embedded structs
and struct pointers keep Go field promotion, value copying and pointer
aliasing semantics. `Options.Omit` is an error here: a selective schema lists
what it exposes.

## Concrete generic structs

`Options.Instances` binds explicit instances of a generic source struct:

```go
Instances: []evmbind.Instance{{
    Type: "Box",
    Native: reflect.TypeFor[Box[int]](),
    Args: []reflect.Type{reflect.TypeFor[int]()},
}},
```

The source template supplies fields, constraints and selected method signatures.
Scripts use `Box[int]`; the binding retains that instance's native layout.
Every reachable instance must be registered. Reflection cannot recover generic
arguments, so the host supplies them explicitly; mismatches fail registration.
Field omissions and method selections use the template name and apply to all
instances. Native generic aliases and non-struct templates are not supported.
Instance bindings currently use reflection; `StaticOnly` rejects them.

## Consolidated native type aliases

A source package can re-export an imported native type with a Go alias when
both schemas use the same guest package path and the type name stays unchanged.
The alias shares the dependency's type, pointer shape and native layout; it
emits no second guest declaration. `Schema.Type` resolves the alias name.
Same-name constant re-exports share the owning declaration when their Go types
and exact values match; conflicting declarations fail during schema building.
Merge the dependency and consumer package fragments before compiling or linking.
Register native methods, field omissions and interface implementations on the
owning schema. Renamed aliases and aliases into another guest package are not
supported by this re-export path.

## Native model arguments and results

Pass the shared model package in `Options.Models` when selecting native model
source with `Options.Only`. The model metadata supplies its native Go types; no
second Go-type-to-model registry is needed. `NativePackage` identifies the
original Go package when it differs from the script path. It can also be
inferred from the selected `Types`, `Implementations`, or generated `Static.Types`.
Supply `NativePackage` explicitly when those roots include unrelated native
packages with types sharing a local declaration name.
Model struct fields are omitted before dependency selection. Their script type
is the model package's immutable row, not a second native struct declaration.

Import that schema into callable host schemas through `Options.Imports` and
link the shared model package. Native pointers retain their identity and nil
value. Native values use ordinary Go struct assignment. Encoded rows and rows
backed by a different native struct require explicit conversion before being
passed to a native pointer parameter. The binding does not decode, cache, or
copy changes back. Native-backed immutable rows remain live views: native code
can change their source object.

Use `Options.Mutable` on the callable schema to expose selected native model
pointer parameters/results as editable rows:

```go
opt := evmbind.Options{
    Imports: imports,
    Mutable: map[string]evmbind.MutableSlots{
        "GetCustomer": {Results: []int{0}},
        "UpdateCustomer": {Params: []int{0}},
    },
}
```

Indices start at zero and exclude the method receiver and host-only `*evm.Call`.
Method keys use `Type.Method`. The same options must be supplied to generated
bindings. Unknown names, invalid indices, and non-model or non-pointer slots
fail schema construction. An annotation applies only to that call site;
ordinary model pointers and `any` results remain immutable by default.

For native `GetCustomer() *Customer`, the annotated script result is
`*db.CustomerEdit`. The script can write `customer.name = "Ada"`; the original
native object changes immediately. Pointer equality and interface boxing retain
its identity. Native callbacks observe the change without a flush. An editable
encoded database record cannot be passed as a native pointer; that requires
explicit conversion. Native code remains responsible for persistence, just as
when it writes a Go struct directly.

Child reads retain native pointers, including nil. Whole-child replacement
remains unsupported by the row API. Native slices retain their backing arrays,
including nested scalar slices and slices of modeled pointers. Assigning a
VM-cell slice to a native field requires explicit conversion; there is no hidden
copy. Native model maps need an explicitly bound native map layout. Optional
collection wrappers and slices of model struct values are not adapted implicitly.

Editable row handles use two VM words to distinguish native pointers from
encoded editors. Native scalar writes do not allocate an editor; string writes
retain the native setter's ownership rules. Programs exposing native model
slices use tagged addresses for their possible element types and general slice
operations where cell-only fast paths would be unsafe. Guest allocations still
use their ordinary storage; no per-object registry is introduced.

Registered concrete generic models retain their shared model identity. Only
registered instantiations may cross the model boundary. Aliases resolve to their
original native types. Reflective and generated wrappers use the same binding
rules. Local model source needs a selective schema; callable sources import
that shared schema.

## Omit host-only fields

Set `Options.OmitFields` to a map from local type names to field names. Fields
are removed before dependency selection, so omitted context fields do not pull
their service interfaces into the script API. Unknown types, unknown fields and
duplicate omissions fail schema construction. Unexported fields are omitted
automatically. Pass the same omissions to `Generate` when generating wrappers.

The native Go struct and its field offsets stay unchanged. A native value copy
retains omitted fields in an inaccessible snapshot; copying or changing exposed
fields does not discard that state. Assigning a newly constructed script value
zeros omitted fields. Snapshots are shallow copies, matching Go struct copying,
and their allocations count against the VM budget. Script field operations,
equality and hashing use the exposed fields only.

## Share declarations across packages

`Options.Imports` maps native Go import paths to previously built schemas.
Native source keeps its original imports; script signatures and fields use the
shared schema's guest package and type identity. Link all dependency packages
alongside the consuming schema. Local and imported structs retain native pointer
aliasing, including through transitive schema dependencies.

Each native import path must resolve to one shared schema instance across the
import graph. Consumers reuse its parsed native declarations, including
transitive types; separately selected subsets should be combined in that schema.
Standard declarations are shared across schemas and initialized once. Several
native packages may emit into one script package without losing Go type identity.

Pass the same map to `Generate` when generating native wrappers.
The generated code imports the actual native Go packages. Imported data layouts
remain owned by the dependency schema; constructing a consumer does not mutate
them. Imported interfaces reuse the owning schema's registered implementations
and conversions, including typed nils. Link the owner's `Bind(nil)` package
(or `Bind(host)` when it has package functions) to install its methods. Scripts
need not explicitly import that package merely to receive an implementation
through a consumer's API.

## Generic native functions

Register compiled package-function instances with `Options.GenericFunctions`:

    GenericFunctions: map[string][]evmbind.FunctionInstance{
        "Var": {
            {Args: []reflect.Type{reflect.TypeFor[string]()}, Function: forms.Var[string]},
            {Args: []reflect.Type{reflect.TypeFor[int64]()}, Function: forms.Var[int64]},
        },
    }

Scripts use the original signature, with inferred or explicit type arguments:
`forms.Var(&name)` or `forms.Var[string](&name)`. Native pointer arguments retain
their storage. The host supplies every compiled instance; unsupported script
types fail at compilation/linking rather than triggering runtime specialization.
Constraints and the native function signature are checked during schema setup.

Register generic structs appearing in these signatures through `Instances` too,
for example `Binding[string]`. Arguments must match registered VM type identities;
a script-defined named type does not match its underlying native scalar type.
These instances currently use reflected calls; generated bindings and `StaticOnly`
reject them. Generic package functions are separate from generic function types
such as `Validator[T]`, which still need binding support.

## Generate bindings

Generate direct Go wrappers in a separate package:

    go run github.com/andreyvit/evm/evmbind/cmd/evmbind \
        -dir ../api -path demo -native example.org/project/api \
        -package apibindings -o bindings_gen.go

The command covers a whole package. A program calling
`Generate(path, nativeImport, outputPackage, source, options)` takes the same
options as `New`, including `Only`, `Selective`, `Omit` and `Imports`.

Pass &apibindings.Bindings in Options.Static. Automatic selects generated
wrappers when the source fingerprint and complete set of wrapper names match.
Missing or stale bindings select reflection. With StaticOnly, `New` panics
on a mismatch and `RegisterMethods` returns an error.
The evmreflect build tag excludes generated Go references, so native API
renames/removals can be developed before regeneration. A package imported in
both builds can supply a zero Static under that tag.

Native package functions and methods support variadic arguments, including
interface dispatch and bound method values. Calls use ordinary Go semantics:
omitted arguments give an empty tail, and `values...` retains the supplied
slice backing. Generated wrappers use Go's `...` call syntax; reflected
wrappers use `reflect.Value.CallSlice`. Variadic calls use the ordinary call
window rather than specialized direct-call opcodes. Variadic native function
fields remain a separate, unsupported case.

## Native values

Pass a host interface argument with schema.Interface(program, ptr). The concrete
type must have been registered and implement a declared interface. R contains
the program's nonzero type index. P holds the original, GC-visible Go pointer.
A typed nil keeps its type index. Both dispatch paths expose only declared
interface methods, and the generated path calls the native interface directly.
Context values passed back to native code must originate from a registered host
implementation. Repack using the destination program when crossing programs.
Use `PointerForCall` when a receiver needs the current VM call to open or
close a budget scope. It can return a shallow receiver copy for that call;
the original pointer stays in the guest interface. Embedded host interfaces
can be passed to procedures accepting just the smaller method set.

The data boundary supports bools, integers, floats, strings, named
scalars, nested data structs with exported fields, and pointer fields to data
structs or scalars. Data structs cross by value. Host pointers retain their
original Go address: guest fields, aliases, escaped field addresses, and native
methods read and mutate the same object. `schema.NativePointer(ptr)` supplies
a registered struct pointer as a guest entry argument. This borrows the native
object; callers must synchronize concurrent access.

Guest allocations of registered data structs/scalars and their globals use
native Go storage too. Other guest addresses backed by VM cells retain the
call-scoped temporary/copyback boundary. Host interfaces carry opaque pointers
and expose methods.
Typed slices and maps of supported native types can appear in fields, arguments,
and results. They retain native backing storage: element updates, map mutations,
and aliases follow Go semantics. Slice growth may replace its backing array.

Maps such as `map[string]any` use the same native storage as other typed
maps, including nested maps and slices. Numbers retain their Go types and
host mutations reach the shared map. Native method-interface fields, slice
elements and map values preserve the concrete native object and typed nils.
Their values must be registered native implementations; guest-only types cannot
be stored in a native method-interface slot. Reading a native-written value
whose concrete type is not registered fails with an unregistered-type error,
including error implementations; no proxy is created. Interface map keys
are not supported. Unsupported declarations fail schema creation.

Ordinary `any` works in arguments, results, struct fields and typed slices.
Registered native values retain their Go types and reference identity. Values
whose types exist only in the guest use `*evm.GuestValue`, retaining the owning
program and exact VM value; passing that handle back preserves its type and
aliases. A handle from another program is rejected. No JSON conversion occurs.
Native result types must be registered; schemas exposing `any` link their
registered concrete types and primitive scalar types automatically.

When an immutable model row is linked, `any` values containing a registered
native model pointer become that row type. The original pointer, including a
typed nil, survives the return to native Go. Several Go types can share one
model. If one Go type belongs to several linked models, pass a `kvo.Record`
with its model to select the interpretation. This does not limit table access.
A modeled `kvo.Record` retains its representation; encoded rows return as
records without decoding. A guest-created nil row returns as a missing record
with the model attached. Model rows take precedence over a separately exposed
native struct type. This handoff currently covers pointers and known-model
records, not native struct values or unmodeled records.


Builtin `error` works in native arguments, results, struct fields and map values. Native
and guest errors retain identity, nil values and error methods through the
existing EVM error bridge. Generated and reflected bindings use the same
conversion. Native `[]error` storage is not yet supported.

## Native package functions

Use `Options.Functions` to bind source declarations to Go function values:

```go
schema, err := evmbind.New("db", edb.TransactionBindingSource, evmbind.Options{
    Only:      []string{"RawRange"},
    Functions: map[string]any{"RawPrefix": edb.RawPrefix},
})
```

In a selective schema only the bound functions enter the guest package. Their
referenced types are selected recursively; implementation bodies are omitted. Binding checks
argument and result types against the supplied function. Calls use that value,
including a supplied closure, without a synthetic `*evm.Call` parameter or
host method. Returned native references keep their native sharing behavior.

`Generate` uses the same options and selection as `New`. Generated
calls invoke the registered function value, including named function values.
The binding fingerprint distinguishes package functions from host methods,
so generated wrappers for one form cannot silently serve the other.

## Retained functions

Use `*evm.NamedFunctionOf[any]` only when the host validates the signature at
registration, such as a GoX renderer whose data type is declared by the script.
Scripts see an `any` parameter. The binding accepts public, noncapturing guest
functions or nil and rejects other values; the host receives `*evm.NamedFunction`.
A typed nil function is rejected. Ordinary `any` parameters keep their normal
value conversion. This handle is input-only; use a typed `FunctionOf` when the
host needs to return a callable or retain captures.


Use `*evm.FunctionOf[any]` for the same host-checked signature when captures
are allowed. The host receives a retained `*evm.FunctionHandle`. Named functions,
closures and bound methods use the normal retained-function representation.
Both nil and a typed nil function produce a nil handle, as with typed
`FunctionOf` parameters. Nonfunction values fail at the call boundary. This
form is input-only; the host must inspect the signature before invocation.

Declare a native registration parameter as
`*evm.NamedFunctionOf[func(int) any]`. The generic alias is an ordinary
`*evm.NamedFunction` in native Go. Its type argument supplies the guest
signature: scripts pass `func(int) any` directly, and incompatible functions
fail compilation. Both generated and reflection bindings resolve the handle
without boxing the guest function in an empty interface. A nil function gives
the host a nil handle.

The annotated guest signature may contain nested function parameters and slices,
including `func(func([]string), func([]any))` for a header/row producer. These
values remain in VM storage; retaining the function does not convert its
arguments into native collections. Use program-owned native callbacks when
invoking it from the host. Ordinary native collection bindings retain the
codec limits described above.

These handles retain only public, noncapturing guest entry metadata. Invoke
with the owning program; captured closures, bound methods and native functions
are not retained by this API. Native function parameters, including typed
callbacks, use the originating VM's globals, context and budgets. Native calls
and callbacks must be serialized, like all other use of that VM. Guest panics
unwind native defers and remain recoverable by guest callers.

Native function fields retain their originating VM. To invoke them while that
VM is idle, either run host work through `vm.RunNative(ctx, call, budget)` or
explicitly open `vm.OpenCallbackScope(ctx, budget)`. Opening it after creating
callbacks enables idle calls for those existing callbacks too.
An open scope permits idle calls on that same VM with the supplied per-call
budget and context; close it before reusing the VM for unrelated work. Reset
also expires all exported callbacks. Without an open scope, idle calls fail.
As with `Run`, an execution failure requires Reset before further execution;
a failed idle callback therefore ends the usable session.
Guest-defined error methods follow the same rules: keep their scope open while
Go code formats or inspects those errors. The context must remain valid during
that inspection. Native Go errors and native-created functions are unaffected.

Native callable values carry their original Go function value, so assigning a
function back to native storage reuses it without accumulating wrapper layers.
No global identity map or goroutine-local VM selection is used.

Use `*evm.FunctionOf[func(int) int]` when closures, bound methods or native
functions are allowed. Native Go sees `*evm.FunctionHandle`. Both generated and
reflection bindings accept and return these handles as script function values.
Returned handles must belong to the current program and match the declared
signature; nil round-trips as a nil function. Handle creation is allocation
metered. Host code can invoke a retained handle with `VM.RunFunction` or
`RunFunctionWithBudget`. Captures retain their identity across invocations.
Native struct fields may use the same `FunctionOf` annotation. Field reads,
writes, address-taking and struct copies translate through the current program;
foreign handles fail before invocation. Stores allocate and meter a retained
handle. Native Go keeps its ordinary typed pointer field.

## Direct calls and fields

A generated wrapper qualifies for
a function with up to two arguments or a pointer method with up to one explicit
argument and zero or one result. Host functions and registered record methods
share the same signature classifier and scalar conversion generator. Supported
values are words (including named scalars and float bits), strings and true pointers. The compiler emits the instruction
for the wrapper’s declared signature. Reflection and older generated wrappers
continue to use their existing call ABI. Interface methods use virtual dispatch.
For a handwritten binding, `Direct` alone also supports function values and
deferred calls. Supply `Call` alongside it to provide your own window wrapper.

A generated pointer wrapper passes native addresses directly. Only VM-cell
arguments need a temporary and copyback. The VM charges a
returned string once and keeps its Go data pointer visible to the GC. For a
handwritten direct binding that returns a KVO row, use `Call.RecordValue` to
construct its typed pair. A direct callback may return a conversion
or execution error. Expose fallible application operations through `Native`.

Generated output also supplies native struct field operations. `NativeFields`
returns the shared `nativebind.Struct`; `NativeType` returns its nominal
`values.Type`. KVO `Binding.WithNativeSchema` and `Projection.WithNativeSchema`
reuse both, so property access and host methods operate on one Go object.
The record codec generator uses the same field generator for types that need
persistence bindings without script exposure. Generated field access, including
escaped scalar addresses, uses typed Go operations; fallback bindings share
nativebind's reflection implementation. Regenerate after changing declarations.

## Explicit record methods

Ordinary Go helpers can become methods of a specific KVO model. The Go symbol,
model and script method name are independent; no naming convention or prefix
stripping controls registration. For example, in a native model package:

```go
func Customer_PointBalance(r kvo.Record) int64 {
    return r.Get(PointsEarned) - r.Get(PointsRedeemed) - r.Get(PointsExpired)
}
```

Declare the script-facing mapping in a separate package:

```go
func RecordMethods() []evmbind.Method {
    return []evmbind.Method{
        evmbind.RecordMethod(nm.MCustomerStatsOverall, "PointBalance",
            nm.Customer_PointBalance, evmbind.BorrowOnly),
    }
}
```

A small `go:generate` program imports that list, calls
`evmbind.GenerateMethods("bindings", RecordMethods())`, and writes the result
to a Go file in package `bindings`. The output package must differ from the
helpers' packages. Pass its generated `MethodBindings` to registration **before
compiling scripts or publishing the package**:

```go
pkg := must(evm.ModelPackage("m", nm.Schema))
ensure(evmbind.RegisterMethods(&pkg, RecordMethods(), evmbind.MethodOptions{
    Mode: evmbind.StaticOnly,
    Static: bindings.MethodBindings,
}))
```

`must` and `ensure` panic on an error. Go callers use
`nm.Customer_PointBalance(r)`; scripts use
`stats.PointBalance()`. Both execute the same helper over the original 16-byte
record, whether it references Fast2, Compact2, MessagePack, native struct or
in-memory data. Calls do not decode or change representation. The method is
available only on explicitly registered immutable model types; editors have a
separate method set. The same helper can be registered on several models.
Properties, existing methods and duplicate registrations cannot be shadowed.
Registration errors leave the package unchanged.

`BorrowOnly` is a trusted promise: the helper must neither retain its arguments
nor invalidate their backing storage, directly or through calls it makes. Use
it for read-only calculations such as this balance helper. It prevents ownership
analysis from inserting unnecessary copies. Omit it for conservative behavior;
`NoRetain` and `NoInvalidate` can also be specified separately. This metadata is
not inferred from Go code.

Generated adapters call Go functions directly, with no reflection or record
boxing. `StaticOnly` rejects missing or stale adapters; `Automatic` can fall
back to reflective calls, which allocate. Regenerate after changing a helper's
signature. Generation requires exported top-level functions, with
`kvo.Record` first, scalar arguments and scalar results. Supported scalars
are bool, integers (excluding uintptr), floats and strings, including named Go
types. Guest signatures use their underlying scalar types. Variadic functions,
collections, record results, closures and bound Go methods are not supported
by this generator. Signatures beyond the direct argument/result limits use the
ordinary call window; bound methods, method expressions, interface calls
and deferred calls work too.

String results are copied and charged to the VM allocation budget so they can
safely outlive borrowed record storage. Numeric/bool calculations can remain
allocation-free. A helper's own allocations are its responsibility. Generated
files are excluded by `evmreflect`; supply an empty `StaticMethods` and use
`Automatic` under that tag if reflection-only development builds are desired.

See [record-method benchmarks](../_docs/BENCH-RECORD-METHODS.md) and the executable
generation example in `internal/methodfixture/gen`.

See [binding consolidation benchmarks and API guide](../_docs/BINDINGS.md) for
void calls, metadata reuse, and manual Shape/DirectBinding declarations.

Native Go function types can also appear in data fields and ordinary method
signatures. The host owns their VM and callback lifetime as described above.
`vm.RunFunctionNative(ctx, handle, args, results, budget)` converts ordinary
arguments and results within the same supplied budget and VM state. Argument types
must match the registered signature; no implicit numeric conversions occur.
Interface parameters accept nil or values implementing the required methods.
Interface results retain their concrete identity; guest-only values return as
`*evm.GuestValue`. Nil interface results, including nil errors, return nil.

Reading a guest-created function back from native storage recovers its original
program-owned handle, so script calls keep the caller's budget and captures.
The bridge tracks closure identities weakly and removes entries after the Go
wrapper dies. Foreign-program handles are rejected. Native function values are
copied when read from fields, so replacing a field does not change a previously
loaded function. Variadic native function types remain unsupported.

## Reflection-based discovery

`Discover` exposes native functions and their reachable types without embedded
source. Prefer `New` when the source is available: reflection has no parameter
names, doc comments or constants. Exported fields are included recursively;
private fields remain hidden and preserve their native storage. Recursive
references retain pointer identity.

```go
_, packages, err := evmbind.Discover("app", evmbind.Discovery{
    Options: evmbind.Options{
        Functions: map[string]any{"Foo": Foo}, // Foo returns *Bar
        Imports: existingSchemas,
    },
    Packages: []evmbind.PackageExposure{
        {Pattern: "example.org/app/...", Methods: true},
        {Pattern: "example.org/app/models/...", Package: "app/models", Methods: true},
        {Pattern: "example.org/app/internal/secrets", Exclude: true},
    },
})
```

Pass the returned packages to compilation and linking, together with existing
bound packages. Types mapped to the function path share its package, so
`app.Foo` and `app.Bar` can sit side by side; each returned path appears once.
`Discovery.Constants` adds named constants to the function package. The
returned schema's `NativePointer` accepts any discovered struct pointer. No `Bar` declaration or explicit type root is needed. Use
`Options.Types` for additional roots, particularly concrete types supplied through
`any`, whose dynamic types cannot be inferred from a function signature.

Types default to their native import paths, with exported fields and no concrete
methods. A rule can enable every exported method, remap types into an umbrella
package, or reject exposure. Interface declarations retain their method sets.
Rules match exact import paths or `/...` descendants (including the parent).
The longest match wins; exact rules beat descendant rules at the same path.
Later rules break ties. A selected rule replaces the broader rule's settings.
Name collisions under an umbrella package fail explicitly.

Existing schemas in `Options.Imports` keep their identities and API selections;
discovery does not broaden them. Their referenced types must already be bound.
Discovery uses reflected adapters and does not require generated wrappers.

Reflection cannot enumerate package globals, package functions or constants.
Register functions and constants explicitly; host-owned global values can use
`ExternalValue`.
There is no automatic-globals option. Concrete generic instances still require
explicit source bindings. Unsupported native shapes fail schema construction
with a type/field error rather than being silently omitted. As with source
bindings, pointers and supported collections retain native storage and mutations.
