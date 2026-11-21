package evmcompiler

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/values"
)

// moduleDifference describes the first difference between a module compiled
// from a package list and the same source compiled through a prepared
// Environment, or returns "" if they are equivalent.
//
// Equivalent modules have the same code, tables and metadata. Types the
// compiler derives, such as []int, have a new identity in every compile, so
// they are compared by name. One difference is allowed: besides the shapes
// its code uses, a module lists the shapes and sources of the host packages
// its source can reach. The prepared module may list more of them, because
// it selects packages by what the imported declarations name, and the
// package-list module by what importing happened to touch.
func moduleDifference(lazy, prepared *evm.Module) string {
	if a, b := lazy.Disassemble(), prepared.Disassemble(); a != b {
		return "disassembly differs:\n" + firstDifferentLine(a, b)
	}
	if a, b := moduleTables(lazy), moduleTables(prepared); a != b {
		return "tables differ:\n" + firstDifferentLine(a, b)
	}
	counts := make(map[string]int)
	for _, s := range prepared.Shapes {
		counts[shapeName(s)]++
	}
	for _, s := range lazy.Shapes {
		name := shapeName(s)
		if counts[name]--; counts[name] < 0 {
			return "the prepared module lacks shape " + name
		}
	}
	for name, text := range lazy.Sources {
		if prepared.Sources[name] != text {
			return "the prepared module lacks source " + name
		}
	}
	return ""
}

func firstDifferentLine(a, b string) string {
	x, y := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(x) || i < len(y); i++ {
		var l, r string
		if i < len(x) {
			l = x[i]
		}
		if i < len(y) {
			r = y[i]
		}
		if l != r {
			return fmt.Sprintf("line %d\n  package list: %s\n  prepared:     %s", i+1, l, r)
		}
	}
	return ""
}

func shapeName(s *values.Shape) string {
	if s == nil || s.Type == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s kind=%d cells=%d native=%v", s.Type.Name(), s.Kind, s.Cells, s.Native != nil)
}

func moduleTables(m *evm.Module) string {
	var b strings.Builder
	typeNames := func(ts []*values.Type) string {
		names := make([]string, len(ts))
		for i, t := range ts {
			names[i] = "<nil>"
			if t != nil {
				names[i] = fmt.Sprintf("%s/%d", t.Name(), t.Repr())
			}
		}
		return strings.Join(names, ",")
	}
	signature := func(s evm.Signature) string { return "(" + typeNames(s.Params) + ")(" + typeNames(s.Results) + ")" }
	fmt.Fprintf(&b, "json %+v noopt=%v strict=%v init=%q\n", m.JSONTypes, m.NoOptimization, m.StrictCallWindows, m.Init)
	for i, f := range m.Functions {
		fmt.Fprintf(&b, "function %d %s %s method=%v internal=%v transparent=%v r=%d p=%d recover=%d code=%v position=%v positions=%v lines=%v captures=%d ownership=%v local=%v\n", i, f.Name, signature(f.Signature), f.Method, f.Internal, f.RecoverTransparent, f.RCount, f.PCount, f.RecoverPC, f.Code, f.Position, f.Positions, f.Lines, len(f.Captures), f.OwnershipSites, slices.Sorted(maps.Keys(f.LocalRecordReads)))
		for _, c := range f.Captures {
			fmt.Fprintf(&b, "  capture %s slot=%d\n", typeNames([]*values.Type{c.Type}), c.Slot)
		}
	}
	for i, imp := range m.Imports {
		fmt.Fprintf(&b, "import %d %s.%s receiver=%s args=%s %s\n", i, imp.Package, imp.Name, typeNames([]*values.Type{imp.Receiver}), typeNames(imp.TypeArgs), signature(imp.Signature))
	}
	fmt.Fprintf(&b, "words %v\nstrings %q\nconversions %v\nrefs %v\n", m.Words, m.Strings, m.Conversions, m.FunctionRefs)
	for i, t := range m.Types {
		fmt.Fprintf(&b, "type %d %s methods=%d\n", i, shapeName(t.Shape), len(t.Methods))
	}
	for i, s := range m.Signatures {
		fmt.Fprintf(&b, "signature %d %s\n", i, signature(s))
	}
	for i, op := range m.ExtendedOps {
		record := ""
		if op.Record != nil {
			record = op.Record.Name
		}
		fmt.Fprintf(&b, "extended %d kind=%d shape=%s source=%s index=%d child=%d flags=%d in=%v out=%v record=%s\n", i, op.Kind, shapeName(op.Shape), shapeName(op.Source), op.Index, op.ChildKey, op.Flags, op.In, op.Out, record)
	}
	for i, s := range m.Globals {
		fmt.Fprintf(&b, "global %d %s\n", i, shapeName(s))
	}
	fmt.Fprintf(&b, "invokes=%d closures=%d defers=%d\n", len(m.Invokes), len(m.Closures), len(m.Defers))
	for _, name := range slices.Sorted(maps.Keys(m.SourceBranches)) {
		branches := m.SourceBranches[name]
		for _, pc := range slices.Sorted(maps.Keys(branches)) {
			fmt.Fprintf(&b, "branch %s %d %+v\n", name, pc, branches[pc])
		}
	}
	return b.String()
}
