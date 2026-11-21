package evmcompiler

import (
	"fmt"
	"go/types"
	"slices"
	"strings"
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
