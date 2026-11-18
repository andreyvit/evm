const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const textmate = require("vscode-textmate");
const oniguruma = require("vscode-oniguruma");

const editors = path.dirname(__dirname);
const fixture = fs.readFileSync(path.join(__dirname, "highlight.gox"), "utf8");

function loadGrammar(file) {
  return textmate.parseRawGrammar(fs.readFileSync(file, "utf8"), file);
}

async function main() {
  const builtinsDirectory = process.argv[2];
  assert(builtinsDirectory, "Pass the path to VS Code's built-in extensions directory.");
  const gox = loadGrammar(path.join(editors, "textmate/syntaxes/GoX.tmLanguage"));
  const grammars = new Map([[gox.scopeName, gox]]);
  for (const extension of ["go", "yaml", "javascript", "css"]) {
    const directory = path.join(builtinsDirectory, extension, "syntaxes");
    for (const name of fs.readdirSync(directory)) {
      if (name.endsWith(".json")) {
        const grammar = loadGrammar(path.join(directory, name));
        grammars.set(grammar.scopeName, grammar);
      }
    }
  }

  await oniguruma.loadWASM(fs.readFileSync(require.resolve("vscode-oniguruma/release/onig.wasm")));
  const registry = new textmate.Registry({
    onigLib: Promise.resolve({
      createOnigScanner: patterns => new oniguruma.OnigScanner(patterns),
      createOnigString: text => new oniguruma.OnigString(text),
    }),
    loadGrammar: async scope => grammars.get(scope),
  });

  const grammar = await registry.loadGrammar("source.gox");
  let stack = textmate.INITIAL;
  const lines = fixture.split("\n").map(line => {
    const { ruleStack, tokens } = grammar.tokenizeLine(line, stack);
    stack = ruleStack;
    return { line, tokens };
  });

  function check(context, token, expected) {
    const { line, tokens } = lines.find(({ line }) => line.includes(context));
    const column = line.indexOf(context) + context.indexOf(token);
    const match = tokens.find(({ startIndex, endIndex }) => startIndex <= column && column < endIndex);
    assert(match.scopes.some(scope => scope.includes(expected)), `${context}: ${match.scopes.join(" ")}`);
  }

  check("---", "---", "punctuation.definition.frontmatter.gox");
  check('name: "Test card"', "name", "entity.name.tag.yaml");
  check("func Page", "func", ".go");
  check("left<right", "<", "keyword.operator.comparison.go");
  check("<Label", "Label", "entity.name.tag.gox");
  check("args:{key()", "key", "meta.embedded.expression.go.gox");
  check("Hello {name}", "name", "meta.embedded.expression.go.gox");
  check("keep <literal>", "literal", "string.quoted.double.go");
  check("// <comment>", "comment", "comment.line.double-slash.go");
  check("const greeting", "const", "storage.type.js");
  check("{=name}", "name", "meta.embedded.expression.go.gox");
  check(".badge", "badge", "entity.other.attribute-name.class.css");
  check("{=accent}", "accent", "meta.embedded.expression.go.gox");
  check("<Widget", "Widget", "entity.name.tag.gox");
  console.log("GoX TextMate scopes passed.");
}

main().catch(error => { console.error(error); process.exitCode = 1; });
