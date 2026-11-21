# Compact GoX values

Templates pass `gox.Content` and `gox.Optional[T]` through component calls
constantly. Without special handling, every small struct copy would allocate a
heap snapshot in VM storage. Instead, EVM packs value copies of `Content` and
common `Optional[T]` specializations into a single 16-byte `values.Value`,
keeping non-escaping locals—including writes to their fields—entirely in
registers.

Native Go APIs stay completely unchanged, and any value whose address escapes
still gets ordinary field-addressable storage.

## `Content`

`gox.Content` fits in one `values.Value`. It holds a GC-visible string pointer
in `P` and a tagged word in `R`. Two process-wide `TypedPair` representations
share the same logical type—one for plain text and one for trusted HTML—with
the auxiliary `uint32` storing the full byte length (lengths above
`math.MaxUint32` are rejected before conversion).

- The zero EVM value represents empty plain text.
- Equality and map hashing compare both the text and the trust flag, never byte
  addresses.
- Packing, unpacking, and passing through `any` allocate no wrapper and copy no
  string bytes.
- Ownership acquisition keeps the trust bit while applying standard string
  ownership rules, and rendering unpacks the value and runs the normal
  context-specific escaping filters.
- `gox.Text` and `gox.TrustedHTML` have direct VM bindings, while native Go
  callers still see the ordinary struct `{Content string, TrustedHTML bool}`.

## `Optional[T]`

GoX's source-defined `Optional[T]` fits in one EVM value whenever `T` is a `bool`,
integer, float, `string`, or registered compact struct such as `Content`:

- A present, nonzero value uses the ordinary EVM representation of `T`.
- The zero EVM value represents missing (`Present == false`, zero `Value`).
- A descriptor-owned marker distinguishes a *present* zero value from a missing
  one, and additional markers preserve a nonzero `Value` with `Present == false`
  (for example, setting `Value` before setting `Present`).
- Packing and field reads allocate nothing.

Optionality is never erased at runtime: `Optional[int]` and `int` remain
distinct dynamic types through `any`, where compact optionals box at the
interface boundary. Markers are per-compilation metadata rather than permanent
representation IDs.

Nested optionals, slices, maps, arbitrary structs, other payloads, and
native-bound generic instances fall back to ordinary struct storage, preserving
Go field mutation, nominal type identity, and nested presence without assuming
spare bits in arbitrary types.

## Host integration

- **`values.CellStruct`** describes a two-field compact struct. Pass native
  descriptors in `evmbind.Options.CellStructs`; both generated and reflected
  bindings use them, and binding fingerprints include this layout so stale
  generated wrappers are caught automatically.
- **`evm.Package.StructValues`** lets a package choose compact storage for its
  source-defined generic structs during compilation (returning `nil` keeps the
  standard layout). GoX registers its `Optional` policy through this hook so
  EVM never has to import GoX.
- Use `Shape.FieldValue` and `Shape.LogicalKind` to inspect logical values
  (`Shape.GetField` and `Shape.SetField` address flattened storage, not compact
  values). Native adapters constructing compact optionals call the descriptor's
  `Pack` method.

Tests cover contextual escaping, zero values, copy and field-pointer semantics,
generated and reflected bindings, conversions to other named structs, `any`,
map keys, and allocation-free packing. Application benchmark output is verified
byte-for-byte against `html/template` before timing.
