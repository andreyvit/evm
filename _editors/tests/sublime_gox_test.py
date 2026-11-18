import json

import sublime
import sublime_plugin


PROBES = [
    ("---", "---", "punctuation.definition.frontmatter.gox"),
    ('name: "Test card"', "name", "meta.embedded.block.yaml.gox"),
    ('"enabled": true', "true", "constant.language.boolean"),
    ("func Page", "func", "keyword.declaration.function.go"),
    ("left<right", "<", "keyword.operator.comparison.go"),
    ("<Label", "Label", "entity.name.tag.gox"),
    ("args:{key()", "key", "meta.embedded.expression.go.gox"),
    ("Hello {name}", "name", "meta.embedded.expression.go.gox"),
    ("keep <literal>", "literal", "string.quoted.double.go"),
    ("// <comment>", "comment", "comment.line.double-slash.go"),
    ("const greeting", "const", "keyword.declaration.js"),
    ("{=name}", "name", "meta.embedded.expression.go.gox"),
    (".badge", "badge", "entity.other.attribute-name.class.css"),
    ("{=accent}", "accent", "meta.embedded.expression.go.gox"),
    ("<Widget", "Widget", "entity.name.tag.gox"),
]


class GoxSyntaxTestCommand(sublime_plugin.ApplicationCommand):
    def run(self, fixture, result):
        def report(failures):
            with open(result, "w") as output:
                json.dump({"passed": not failures, "failures": failures}, output, indent=2)

        syntax = sublime.find_syntax_for_file(fixture)
        if syntax is None or syntax.scope != "source.gox":
            report(["GoX was not selected for .gox"])
            return

        with open(fixture) as source:
            text = source.read()
        view = sublime.active_window().new_file()
        view.set_scratch(True)
        view.assign_syntax(syntax)
        view.run_command("append", {"characters": text})

        def inspect():
            failures = []
            for context, token, expected in PROBES:
                position = text.index(context) + context.index(token)
                scope = view.scope_name(position)
                if expected not in scope:
                    failures.append({"context": context, "expected": expected, "actual": scope})
            report(failures)
            view.close()

        sublime.set_timeout(inspect, 500)
