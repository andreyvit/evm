# GoX for Zed

Automatic syntax highlighting for `.gox` files in Zed. The
[GoX Tree-sitter grammar](../tree-sitter-gox) extends Tree-sitter Go with
markup tags and interpolation, highlighting frontmatter as YAML and `<script>`
/ `<style>` bodies as JavaScript and CSS.

## Install from a local checkout

In Zed, open the command palette, run **zed: install dev extension**, and
select `_editors/zed`. Zed fetches the grammar commit pinned in
`extension.toml`.

To test an *unpushed* grammar commit, copy `_editors/zed` outside this
repository, point `repository` in the copy's `extension.toml` to this
checkout's `file://` URL, set `rev` to your local commit, and install that copy
in Zed. (Zed downloads the WASI SDK the first time it builds a grammar.)

## Update the grammar

From `_editors/tree-sitter-gox`, run `tree-sitter generate` and
`tree-sitter test` with Tree-sitter CLI 0.25.10. Commit the generated files,
update `grammars.gox.rev` in `extension.toml` to that commit, rebuild the dev
extension in Zed, and open `gox/testdata/examples/showcase.gox` to verify.

## Publish to the Zed registry

1. Push the grammar and extension commits to the public EVM repository.
2. Fork `zed-industries/extensions` and add EVM as an HTTPS submodule:

```sh
git submodule add https://github.com/andreyvit/evm extensions/gox
```

3. Pin the submodule to the release commit on a public branch and add this
   entry to `extensions.toml` (matching the version in `extension.toml`):

```toml
[gox]
submodule = "extensions/gox"
path = "_editors/zed"
version = "0.1.0"
```

4. Run `pnpm sort-extensions` and open a PR against
   `zed-industries/extensions`. Zed publishes the extension once merged—see the
   [Zed publishing guide](https://zed.dev/docs/extensions/publishing/publishing-guide).
