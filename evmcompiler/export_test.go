package evmcompiler

import (
	"fmt"
	"go/types"
	"slices"
	"strings"

	"github.com/andreyvit/evm/values"
)

var ModuleDifference = moduleDifference

// Fingerprint describes everything a prepared environment shares between
// compiles that a compile could conceivably change: the packages' scopes and
// method sets and the sizes of every shared table.
func (env *Environment) Fingerprint() string {
	var b strings.Builder
	fmt.Fprintf(&b, "files=%d\n", len(env.files))
	for _, f := range env.files {
		fmt.Fprintf(&b, "file %s base=%d size=%d lines=%d\n", f.Name(), f.Base(), f.Size(), f.LineCount())
	}
	for _, e := range []*typeEnvironment{env.real, env.facade} {
		fmt.Fprintf(&b, "universe toGo=%d fromGo=%d shapes=%d order=%d layouts=%d synthetic=%d memo=%d natives=%d receivers=%d generic=%d records=%d/%d operations=%d imported=%d declared=%d errors=%d standard=%d pending=%d/%d/%d\n",
			len(e.toGo), e.fromGo.Len(), e.shapes.Len(), len(e.shapeOrder), len(e.nativeLayouts), len(e.syntheticTypes), len(e.nativeSourceMemo), len(e.natives), len(e.nativeReceiverMethods), len(e.genericNativeDecls), len(e.recordFields), len(e.recordMethods), len(e.recordOperations), len(e.imported), len(e.declared), len(e.importErrors), len(e.standard), len(e.pendingNativeStorage), len(e.pendingSourceChecks), len(e.pendingMapKeys))
		for _, s := range e.shapeOrder {
			fmt.Fprintf(&b, "shape %s kind=%d cells=%d fields=%d methods=%d native=%v\n", s.Type.Name(), s.Kind, s.Cells, len(s.Fields), len(s.Methods), s.Native != nil)
		}
		// The method tables modules share.
		typeNames := func(ts []*values.Type) string {
			names := make([]string, len(ts))
			for i, t := range ts {
				names[i] = t.Name()
			}
			return strings.Join(names, ",")
		}
		fmt.Fprintf(&b, "method imports=%d bindings=%d numbers=%d tables=%d\n", len(e.methodImports), len(e.methodBindings), len(e.methodNumbers), len(e.hostMethods))
		for i, imp := range e.methodImports {
			fmt.Fprintf(&b, "method import %d %s.%s receiver=%s (%s)(%s) ownership=%+v\n", i, imp.Package, imp.Name, imp.Receiver.Name(), typeNames(imp.Signature.Params), typeNames(imp.Signature.Results), imp.Ownership)
		}
		var tables []string
		for s, methods := range e.hostMethods {
			table := "method table " + s.Type.Name()
			for _, m := range methods {
				table += fmt.Sprintf(" %s(%s)(%s)%v%v", m.ID, typeNames(m.Signature.Params), typeNames(m.Signature.Results), m.Variadic, m.Function)
			}
			tables = append(tables, table)
		}
		slices.Sort(tables)
		b.WriteString(strings.Join(tables, "\n") + "\n")
		for _, pkg := range e.importOrder {
			fmt.Fprintf(&b, "package %s %q complete=%v imports=%d\n", pkg.Path(), pkg.Name(), pkg.Complete(), len(pkg.Imports()))
			scope := pkg.Scope()
			for _, name := range scope.Names() {
				obj := scope.Lookup(name)
				fmt.Fprintf(&b, "  %s\n", types.ObjectString(obj, nil))
				if named, ok := obj.Type().(*types.Named); ok {
					if _, isType := obj.(*types.TypeName); isType {
						fmt.Fprintf(&b, "    underlying %s\n", named.Underlying())
						methods := slices.Collect(named.Methods())
						for _, m := range methods {
							fmt.Fprintf(&b, "    method %s\n", types.ObjectString(m, nil))
						}
					}
				}
			}
		}
	}
	return b.String()
}

// SSAChecks reports whether the SSA builder checks its own output in this
// build, which costs far more than building.
func SSAChecks() bool { return ssaChecks != 0 }

// WithoutReference runs f without comparing each function it compiles with
// the analyses that were replaced (see ownership_reference_test.go), for
// sources those would take minutes on.
func WithoutReference(f func()) {
	ownership, lists := checkOwnership, checkRecordLists
	checkOwnership, checkRecordLists = nil, nil
	defer func() { checkOwnership, checkRecordLists = ownership, lists }()
	f()
}
