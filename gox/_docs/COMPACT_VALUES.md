# Compact GoX values

The native Go APIs stay unchanged. EVM value copies use compact cells; values
whose addresses escape still use field-addressable storage. The compiler keeps
nonescaping locals in registers, including writes to their fields.

## Content

`Content` occupies one `values.Value`: a GC-visible string pointer plus a tagged
word. Two process-wide `TypedPair` representations share the same logical type:
plain text and trusted HTML. The auxiliary uint32 holds the full byte length.
Lengths above `math.MaxUint32` are rejected before conversion.

The zero cell means empty plain text. Equality and map hashing compare text and
trust, never byte addresses. Packing, unpacking, and passing through `any` need
no wrapper allocation or string copy. Ownership acquisition retains the trust
bit while applying the normal string ownership rules. Rendering decodes the
cell and uses the existing context-specific escaping rules.

`Text` and `TrustedHTML` have direct VM bindings. Native Go callers still receive
the public struct with `Content string` and `TrustedHTML bool` fields.

## Optional

GoX's source-defined `Optional[T]` instances use one cell when T is a boolean,
integer, floating-point number, string, or registered compact struct such as
`Content`. An ordinary present value uses T's existing cell. The zero cell means
missing; a descriptor-owned marker distinguishes present zero. Other markers
preserve a nonzero Value with Present=false, including writes to Value before
setting Present. Packing and field reads allocate nothing.

Optionality is not entirely erased: `Optional[int]` and `int` remain distinct
runtime types through `any`. These optional cells box at that boundary. Markers
are compilation metadata, not permanent representation IDs.

Nested optionals, slices, maps, arbitrary structs, and other payloads retain
ordinary struct storage. Native-bound generic instances also retain their
registered layout. These fallbacks preserve Go field mutation, nominal type
identity, and nested presence states without assuming spare bits in every T.

## Host integration

`values.CellStruct` describes a two-field compact struct. `evmbind.Options`
accepts native descriptors in `CellStructs`; generated and reflected bindings
both use the descriptor. Binding fingerprints include this representation so
stale generated wrappers cannot silently read the previous layout.

`evm.Package.StructValues` lets a package choose storage for its source-defined
generic structs at compilation. Returning nil keeps the ordinary layout. GoX
registers its Optional policy through this hook; EVM does not import GoX.

Use `Shape.FieldValue` and `Shape.LogicalKind` when inspecting logical values.
`Shape.GetField`/`SetField` address flattened storage, not a compact value cell.
Native adapters constructing compact optionals use the descriptor's Pack method.

Tests cover contextual escaping, zero values, copy and field-pointer semantics,
native and generated bindings, conversions to other named structs, `any`, map
keys, and allocation-free packing. Application benchmark output is checked
against `html/template` before timing.
