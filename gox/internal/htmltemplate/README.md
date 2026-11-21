# Upstream `html/template` copy

Copied from Go 1.27.3 `src/html/template` (BSD-3-Clause; see `LICENSE`).

**Local changes:**

- Renamed the package, removed the `internal/godebug` dependency, and
  permanently enabled secure meta-refresh URL escaping.
- Added `planner.go` to expose compile-time context analysis and preselected
  escaping functions to GoX.
- Copied upstream escaping, transition, and trusted-content tests (omitting the
  test for disabling meta-refresh URL escaping, and updating package-qualified
  error expectations).

Keep all other upstream files unchanged when updating to a newer Go release.

Trusted-content types alias the standard `html/template` types so native
helpers and GoX share exact type identity. `stdlib_content.go` embeds the Go
1.27.3 declarations for binding generation only—it is not compiled into a
second set of Go types.
