# GoX editor support

GoX adds tags and interpolation to Go. These definitions reuse the editor's Go
highlighting and highlight frontmatter as YAML.

Run the commands below from the repository root. The smoke tests share
[highlight.gox](tests/highlight.gox), covering nested tags, comparisons, computed
attribute keys, YAML, Go strings and comments, and embedded JavaScript and CSS.

## Zed

See [GoX for Zed](zed). It uses the shared Tree-sitter parser and Zed queries.

## Vim

Use `_editors/vim` as a runtime directory. To install it as a Vim package on Unix:

```sh
mkdir -p ~/.vim/pack/gox/start
ln -s "$PWD/_editors/vim" ~/.vim/pack/gox/start/gox
```

Run the filetype and syntax group checks in a clean Vim session.

```sh
vim -Nu NONE -i NONE -es -S _editors/tests/vim.vim
```

## VS Code and TextMate

Open an Extension Development Host with
`code --extensionDevelopmentPath="$PWD/_editors/textmate"`. To install a packaged
extension, run `npx @vscode/vsce@3.6.2 package` in `_editors/textmate` and use
**Extensions: Install from VSIX…**.

For TextMate, create `GoX.tmbundle` in
`~/Library/Application Support/TextMate/Bundles`. Copy
[textmate/info.plist](textmate/info.plist) to its root and
[GoX.tmLanguage](textmate/syntaxes/GoX.tmLanguage) into its `Syntaxes` directory.
Install the [Go](https://github.com/rsms/Go.tmbundle) and
[YAML](https://github.com/textmate/yaml.tmbundle) syntax bundles if those
scopes are missing, then restart TextMate. TextMate 2.0.23 provides the
JavaScript and CSS bundles and was verified with the shared fixture.
Editors with TextMate bundle import, including JetBrains IDEs, can import this
bundle when the Go, YAML, JavaScript, and CSS scopes are available.

Test with VS Code's tokenizer and built-in grammars. On macOS, the latter live
in `Visual Studio Code.app/Contents/Resources/app/extensions`.

```sh
deps=$(mktemp -d)
npm install --prefix "$deps" vscode-textmate@9.2.0 vscode-oniguruma@2.0.1
NODE_PATH="$deps/node_modules" node _editors/tests/textmate.cjs /path/to/vscode/extensions
```

## Sublime Text

Copy [GoX.sublime-syntax](sublime/GoX.sublime-syntax) to the `User` directory
under **Preferences → Browse Packages**. The native definition uses Sublime's
Go syntax and keeps Go interpolation visible inside JavaScript and CSS.
Verified on Sublime Text build 4200.

To run scope assertions in Sublime, temporarily put
[the test plugin](tests/sublime_gox_test.py) alongside the GoX syntax. With
Sublime open, run the command below with absolute paths. The JSON result reports
whether the probes passed. Remove the test plugin when done.

```sh
subl --background --command "gox_syntax_test {\"fixture\":\"$PWD/_editors/tests/highlight.gox\",\"result\":\"/tmp/gox-sublime-result.json\"}"
```

## Neovim

Use Neovim 0.11 or later and Tree-sitter CLI 0.25.10. Add
`_editors/neovim` to `runtimepath` for filetype detection and activation.
Install the parser and queries in a runtime directory, such as your Neovim
configuration directory.

```sh
nvim_runtime="${XDG_CONFIG_HOME:-$HOME/.config}/nvim"
mkdir -p "$nvim_runtime/parser" "$nvim_runtime/queries/gox"
tree-sitter build _editors/tree-sitter-gox -o "$nvim_runtime/parser/gox.so"
cp _editors/zed/languages/gox/highlights.scm "$nvim_runtime/queries/gox/"
cp _editors/tree-sitter-runtime/queries/gox/injections.scm "$nvim_runtime/queries/gox/"
```

Use installed YAML, JavaScript, and CSS Tree-sitter parsers and queries for
embedded highlighting. With those available, run the headless scope checks.

```sh
nvim --headless --cmd "set runtimepath^=$PWD/_editors/neovim" \
  _editors/tests/highlight.gox -l _editors/tests/neovim.lua
```

## Helix

Merge [helix/languages.toml](helix/languages.toml) into your Helix
`languages.toml`. For an unpushed grammar, use a `source = { path = "…" }`
table with the absolute path to `_editors/tree-sitter-gox`.

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

In the fixture, use `:tree-sitter-highlight-name` over a tag or YAML key to
inspect the active highlight. Neovim and Helix load JavaScript and CSS spans on either side
of a Go interpolation as separate syntax regions.
