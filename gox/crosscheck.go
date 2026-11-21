//go:build evmcrosscheck

package gox

import (
	"fmt"

	"github.com/andreyvit/evm"
)

// With the evmcrosscheck build tag, every package-list compile is repeated
// through a prepared Environment and the two templates must match; see the
// same file in evmcompiler. A mismatch panics.
//
// Compiles with a frontmatter parser are not repeated: frontmatter is parsed
// before type checking, and tests count the parser's calls.
func init() { crossCheck = compareWithPrepared }

func compareWithPrepared(options Options, entry string, sources map[string]string, packages []evm.Package, lazy *Template, lazyErr error) {
	if options.ParseFrontmatter != nil {
		return
	}
	env, err := NewEnvironment(packages...)
	if err != nil {
		return // A package that cannot be imported; evmcompiler logs these.
	}
	prepared := options
	prepared.Environment = env
	for round := 0; round < 2; round++ {
		tmpl, err := prepared.compileOnce(entry, sources, nil)
		if fmt.Sprint(lazyErr) != fmt.Sprint(err) {
			// Some diagnostics name map entries in iteration order. Accept
			// any message the package-list path also produces.
			stable := true
			for attempt := 0; attempt < 16 && stable; attempt++ {
				_, again := options.compileOnce(entry, sources, packages)
				stable = fmt.Sprint(again) != fmt.Sprint(err)
			}
			if stable {
				panic(fmt.Sprintf("prepared environment (round %d): error mismatch\npackage list: %v\nprepared:     %v\nsources: %q", round, lazyErr, err, sources))
			}
			continue
		}
		if lazyErr != nil {
			continue
		}
		if a, b := templateSummary(lazy), templateSummary(tmpl); a != b {
			panic(fmt.Sprintf("prepared environment (round %d): template mismatch\npackage list:\n%s\nprepared:\n%s\nsources: %q", round, a, b, sources))
		}
	}
}

func templateSummary(t *Template) string {
	return fmt.Sprintf("entry=%q parameters=%+v functions=%+v classes=%+v dynamic=%v operations=%d files=%+v\n%s\n%s", t.Entry, t.Parameters, t.FunctionParameters, t.Classes, t.DynamicClasses, len(t.operations), t.files, t.GeneratedSource, t.Module.Disassemble())
}
