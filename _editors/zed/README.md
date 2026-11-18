# GoX for Zed

Highlights `.gox` files automatically. The [GoX grammar](../tree-sitter-gox)
extends Tree-sitter Go with markup and interpolation. Zed highlights
frontmatter as YAML and uses JavaScript and CSS for script and style bodies.

## Install from a checkout

In Zed, run **zed: install dev extension** and select `_editors/zed`.
Zed fetches the grammar revision pinned in `extension.toml`.

To test an unpushed grammar revision, make a copy of this directory outside
the checkout. In the copy's manifest, set the grammar's `repository` to the
checkout's `file://` URL, and set `rev` to the local grammar commit. Select
the copy in Zed. Zed downloads the WASI SDK when it first builds a grammar.

## Update the grammar

From `_editors/tree-sitter-gox`, run `tree-sitter generate` and
`tree-sitter test` with Tree-sitter CLI 0.25.10. Commit the generated sources
and update `grammars.gox.rev` to that commit. Rebuild the dev extension in Zed
and open `gox/testdata/examples/showcase.gox` to check highlighting.

## Publish

Push the grammar and extension commits to the public EVM repository. Fork
`zed-industries/extensions` and add EVM as a submodule over HTTPS.

```sh
git submodule add https://github.com/andreyvit/evm extensions/gox
```

Pin the submodule to the extension release commit on a public branch. Add
the following entry to the registry's `extensions.toml`.

```toml
[gox]
submodule = "extensions/gox"
path = "_editors/zed"
version = "0.1.0"
```

Match the extension manifest's version, run `pnpm sort-extensions`, and open
a PR against `zed-industries/extensions`. Zed publishes the extension after
the PR merges. [Zed publishing guide](https://zed.dev/docs/extensions/publishing/publishing-guide)
