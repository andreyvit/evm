package evm

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/andreyvit/evm/values"
)

// programListing describes everything Link derived for a program, without
// addresses, so that two links of one module can be compared: one from a
// package list and one through a prepared environment. Method IDs are
// assigned in map order, so methods are listed by name. Tests use it, and so
// does the evmcrosscheck build; see link_crosscheck.go.
func programListing(p *Program) string {
	var out strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&out, format+"\n", args...) }
	typeNames := func(ts []*values.Type) string {
		names := make([]string, len(ts))
		for i, t := range ts {
			names[i] = t.Name()
		}
		return strings.Join(names, ",")
	}
	shapeNames := func(shapes []*values.Shape) string {
		names := make([]string, len(shapes))
		for i, s := range shapes {
			names[i] = s.Type.Name()
		}
		return strings.Join(names, ",")
	}
	shapeName := func(s *values.Shape) string {
		if s == nil {
			return "-"
		}
		return s.Type.Name()
	}
	layout := func(l *values.NativeLayout) string {
		if l == nil {
			return "-"
		}
		return fmt.Sprintf("{go=%v size=%d fields=%d opaque=%d address=%t codec=%t compare=%t array=%t map=%t slice=%t}", l.GoType, l.Size, len(l.Fields), l.OpaqueCell, l.Address != nil, l.Codec != nil, l.Compare != nil, l.Array != nil, l.Map != nil, l.Slice != nil)
	}
	shape := func(s *values.Shape) string {
		if s == nil {
			return "-"
		}
		var fields, methods []string
		for _, f := range s.Fields {
			fields = append(fields, fmt.Sprintf("%s:%s@%d/%t/%q", f.Name, f.Shape.Type.Name(), f.Offset, f.Embedded, f.Tag))
		}
		for _, m := range s.Methods {
			methods = append(methods, fmt.Sprintf("%s(%s)(%s)", m.ID, shapeNames(m.Signature.Params), shapeNames(m.Signature.Results)))
		}
		return fmt.Sprintf("%s kind=%d bits=%d len=%d cells=%d display=%q pointerLike=%t variadic=%t cell=%t pointerType=%v elem=%s key=%s fields=%v methods=%v params=(%s) results=(%s) native=%s allocation=%s",
			s.Type.Name(), s.Kind, s.Bits, s.Len, s.Cells, s.DisplayName, s.PointerLike, s.Variadic, s.CellStruct != nil, s.NativePointerType, shapeName(s.Elem), shapeName(s.Key), fields, methods, shapeNames(s.Params), shapeNames(s.Results), layout(s.Native), layout(s.NativeAllocation))
	}
	methodNames := make(map[uint32]string, len(p.methodIDs))
	for name, id := range p.methodIDs {
		methodNames[id] = name
	}

	line("init=%d recordListSlots=%d runtimeError=%d panicNil=%d nativeImports=%d json=%v", p.init, p.recordListSlots, p.runtimeError, p.panicNil, p.nativeImports, p.jsonTypes)
	line("entries=%v", slices.Sorted(maps.Keys(p.entries)))
	line("methods=%v", slices.Sorted(maps.Keys(p.methodIDs)))
	line("words=%v conversions=%v", p.words, p.conversions)
	for i, s := range p.strings {
		line("string %d %q", i, s.AsString())
	}
	for i, f := range p.functions {
		line("func %d %s method=%t internal=%t sig=(%s)(%s) params=%v results=%v r=%d/%d p=%d/%d definedRaw=%t recover=%d captures=%d localReads=%v code=%x", i, f.Name, f.Method, f.Internal, typeNames(f.Signature.Params), typeNames(f.Signature.Results), f.params, f.results, f.RCount, f.rSize, f.PCount, f.pSize, f.definedRaw, f.RecoverPC, len(f.Captures), f.localReads, f.Code)
	}
	for i, g := range p.natives {
		form, record := "-", "-"
		if g.form != nil {
			form = fmt.Sprintf("%+v", *g.form)
		}
		if g.record != nil {
			record = g.record.Name
		}
		line("native %d %s sig=(%s)(%s) params=%v results=%v r=%d p=%d resultString=%t record=%s form=%s op=%v window=%t word=%t call=%t panics=%t variadic=%t ownership=%+v", i, g.Name, typeNames(g.Signature.Params), typeNames(g.Signature.Results), g.params, g.results, g.rSize, g.pSize, g.resultString, record, form, g.Direct.Op, g.Direct.Window, g.Direct.word != nil, g.Call != nil, g.panics, g.Variadic, g.Ownership)
	}
	for id, t := range p.types {
		if id == 0 {
			continue
		}
		var methods []string
		for mid, m := range t.methods {
			methods = append(methods, fmt.Sprintf("%s=%s(%s)(%s)%t%v", methodNames[mid], m.ID, typeNames(m.Signature.Params), typeNames(m.Signature.Results), m.Variadic, m.Function))
		}
		slices.Sort(methods)
		line("type %d %s id=%d methods=%v", id, shape(t.shape), p.typeIDs[t.shape.Type], methods)
	}
	for i, op := range p.invokes {
		line("invoke %d %s %s.%s r=%d p=%d", i, methodNames[op.methodID], op.InterfaceName, op.Method.ID, op.rSize, op.pSize)
	}
	for i, s := range p.signatures {
		line("signature %d (%s)(%s) %v %v", i, typeNames(s.Params), typeNames(s.Results), s.params, s.results)
	}
	line("functionRefs=%v closures=%v", p.functionRefs, p.closures)
	for i, s := range p.shapes {
		line("shape %d %s", i, shape(s))
	}
	for i, s := range p.globals {
		line("global %d %s", i, shape(s))
	}
	for i, op := range p.extendedOps {
		record := "-"
		if op.Record != nil {
			record = fmt.Sprintf("%s/%t", op.Record.Name, op.Record.Mutable)
		}
		line("extended %d kind=%d shape=%s source=%s in=%v out=%v index=%d child=%d flags=%d packed=%t record=%s listSlot=%d listAccess=%t childModel=%t", i, op.Kind, shapeName(op.Shape), shapeName(op.Source), op.In, op.Out, op.Index, op.ChildKey, op.Flags, op.Packed, record, op.listSlot, op.listAccess != nil, op.childModel != nil)
	}
	for i, d := range p.defers {
		line("defer %d %+v", i, d)
	}
	line("floatLoops:\n%s", p.DisassembleFloatLoops())

	var names []string
	for s, required := range p.requirements {
		var methods []string
		for _, m := range required {
			methods = append(methods, methodNames[m.id]+"="+m.method.ID)
		}
		names = append(names, fmt.Sprintf("%s:%v", s.Type.Name(), methods))
	}
	slices.Sort(names)
	line("requirements=%v", names)
	// Programs see the same types whether a snapshot is shared or their own.
	var all []*values.Type
	for _, s := range p.shapes {
		all = append(all, s.Type)
	}
	for _, t := range p.types {
		if t.shape != nil {
			all = append(all, t.shape.Type)
		}
	}
	for _, g := range p.natives {
		all = append(all, g.Signature.Params...)
		all = append(all, g.Signature.Results...)
	}
	names = nil
	for _, t := range all {
		names = append(names, t.Name()+"="+shape(p.Shape(t)))
	}
	slices.Sort(names)
	for _, name := range slices.Compact(names) {
		line("shape of %s", name)
	}
	names = nil
	for t, s := range p.nativeGoShapes {
		names = append(names, t.String()+"="+s.Type.Name())
	}
	slices.Sort(names)
	line("nativeGoShapes=%v", names)
	names = nil
	for t, b := range p.nativeModels {
		names = append(names, fmt.Sprintf("%v=%s/%t/%t", t, b.record.Name, b.ambiguous, b.value))
	}
	for t := range p.nativeRecordModels {
		names = append(names, "record "+t.Name())
	}
	for t, b := range p.nativeSliceTypes {
		names = append(names, fmt.Sprintf("slice %v=%s/%s", t, b.logical.Name(), shapeName(b.shape)))
	}
	for t := range p.nativeCodecs {
		names = append(names, "codec "+t.Name())
	}
	slices.Sort(names)
	line("native=%v", names)
	for _, name := range slices.Sorted(maps.Keys(p.sourceBranches)) {
		line("branches %s %d", name, len(p.sourceBranches[name]))
	}
	return out.String()
}
