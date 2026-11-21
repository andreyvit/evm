# GoX editor support

Syntax highlighting for `.gox` templates across seven editors: Zed, Vim,
Neovim, VS Code, TextMate, Sublime Text, and Helix. Every grammar reuses your
editor's native Go highlighting, highlights frontmatter as YAML, and switches
to JavaScript and CSS inside `<script>` and `<style>` blocks.

Run all commands below from the repository root. Every smoke test checks
against the same fixture, [tests/highlight.gox](tests/highlight.gox), covering
nested tags, comparisons, computed attribute keys, YAML frontmatter, Go strings
and comments, and embedded JavaScript and CSS.

## Zed

See [GoX for Zed](zed/README.md), which uses the shared Tree-sitter parser and
Zed queries.

## Vim

Add `_editors/vim` to your runtime path. On Unix, install it as a native Vim
package:

```sh
mkdir -p ~/.vim/pack/gox/start
ln -s "$PWD/_editors/vim" ~/.vim/pack/gox/start/gox
```

Verify filetype detection and syntax groups in a clean Vim session:

```sh
vim -Nu NONE -i NONE -es -S _editors/tests/vim.vim
```

## VS Code and TextMate

- **Try in VS Code:** Open an Extension Development Host with
  `code --extensionDevelopmentPath="$PWD/_editors/textmate"`.
- **Package for VS Code:** Run `npx @vscode/vsce@3.6.2 package` inside
  `_editors/textmate`, then run **Extensions: Install from VSIX…**.
- **Install in TextMate:** Create `GoX.tmbundle` in
  `~/Library/Application Support/TextMate/Bundles`, copy
  [textmate/info.plist](textmate/info.plist) to its root, and copy
  [GoX.tmLanguage](textmate/syntaxes/GoX.tmLanguage) into its `Syntaxes`
  directory. Install the [Go](https://github.com/rsms/Go.tmbundle) and
  [YAML](https://github.com/textmate/yaml.tmbundle) bundles if those scopes are
  missing, then restart TextMate. (Verified on TextMate 2.0.23, which ships
  with JavaScript and CSS bundles. Other editors with TextMate bundle import,
  including JetBrains IDEs, can import this bundle too.)

Run the tokenizer test against VS Code's built-in grammars (on macOS, located
in `Visual Studio Code.app/Contents/Resources/app/extensions`):

```sh
deps=$(mktemp -d)
npm install --prefix "$deps" vscode-textmate@9.2.0 vscode-oniguruma@2.0.1
NODE_PATH="$deps/node_modules" node _editors/tests/textmate.cjs /path/to/vscode/extensions
```

## Sublime Text

Copy [GoX.sublime-syntax](sublime/GoX.sublime-syntax) into your `User`
directory (**Preferences → Browse Packages**). It builds on Sublime's native Go
syntax and keeps Go interpolations highlighted inside JavaScript and CSS
(verified on Sublime Text build 4200).

To run scope assertions, temporarily copy
[tests/sublime_gox_test.py](tests/sublime_gox_test.py) alongside the syntax
file, run the command below while Sublime is open, check the JSON result, and
remove the test plugin afterward:

```sh
subl --background --command "gox_syntax_test {\"fixture\":\"$PWD/_editors/tests/highlight.gox\",\"result\":\"/tmp/gox-sublime-result.json\"}"
```

## Neovim

Requires Neovim 0.11+ and Tree-sitter CLI 0.25.10. Add `_editors/neovim` to
your `runtimepath` for filetype detection, then build and install the parser
and queries:

```sh
nvim_runtime="${XDG_CONFIG_HOME:-$HOME/.config}/nvim"
mkdir -p "$nvim_runtime/parser" "$nvim_runtime/queries/gox"
tree-sitter build _editors/tree-sitter-gox -o "$nvim_runtime/parser/gox.so"
cp _editors/zed/languages/gox/highlights.scm "$nvim_runtime/queries/gox/"
cp _editors/tree-sitter-runtime/queries/gox/injections.scm "$nvim_runtime/queries/gox/"
```

With YAML, JavaScript, and CSS Tree-sitter parsers installed, run the headless
scope checks:

```sh
nvim --headless --cmd "set runtimepath^=$PWD/_editors/neovim" \
  _editors/tests/highlight.gox -l _editors/tests/neovim.lua
```

## Helix

Merge [helix/languages.toml](helix/languages.toml) into your Helix
`languages.toml` (for an unpushed grammar, set `source = { path = "…" }` to the
absolute path of `_editors/tree-sitter-gox`):

```sh
helix_config="${XDG_CONFIG_HOME:-$HOME/.config}/helix"
mkdir -p "$helix_config/runtime/grammars" "$helix_config/runtime/queries/gox"
cp _editors/zed/languages/gox/highlights.scm "$helix_config/runtime/queries/gox/"
cp _editors/tree-sitter-runtime/queries/gox/injections.scm "$helix_config/runtime/queries/gox/"
hx --grammar fetch
hx --grammar build
hx --health gox
hx _editors/tests/highlight.gox
```

Use `:tree-sitter-highlight-name` over any tag or YAML key to inspect its
active highlight. Note that Neovim and Helix highlight JavaScript and CSS spans
on either side of a Go interpolation as separate syntax regions.
