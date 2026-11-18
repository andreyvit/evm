package evmcompiler

import (
	"go/token"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/values"
	"golang.org/x/tools/go/ssa"
)

func (c *typedCompiler) externalValue(g *ssa.Global) *evm.ExternalValue {
	if g.Pkg == nil {
		return nil
	}
	pkg := c.env.packages[g.Pkg.Pkg.Path()]
	if pkg == nil {
		return nil
	}
	for i := range pkg.Values {
		if pkg.Values[i].Name == g.Name() {
			return &pkg.Values[i]
		}
	}
	return nil
}

func (f *lowerer) checkExternalValueUses(g *ssa.Global) {
	for _, block := range f.fn.Blocks {
		for _, use := range block.Instrs {
			if _, ok := use.(*ssa.DebugRef); ok {
				continue
			}
			if load, ok := use.(*ssa.UnOp); ok && load.Op == token.MUL {
				continue
			}
			for _, operand := range use.Operands(nil) {
				if operand != nil && *operand == g {
					f.c.fail(use.Pos(), "host-owned package value %s cannot be assigned or addressed", g.String())
				}
			}
		}
	}
}

func (f *lowerer) loadExternalValue(load *ssa.UnOp) bool {
	g, ok := load.X.(*ssa.Global)
	if !ok || load.Op != token.MUL {
		return false
	}
	v := f.c.externalValue(g)
	if v == nil {
		return false
	}
	key := importKey{g.Pkg.Pkg.Path(), v.LinkName(), nil}
	index, exists := f.c.imports[key]
	sig := evm.Signature{Results: []*values.Type{v.Type}}
	if !exists {
		index = uint32(len(f.c.module.Imports))
		f.c.imports[key] = index
		f.c.module.Imports = append(f.c.module.Imports, evm.Import{Package: key.pkg, Name: v.LinkName(), Signature: sig})
	}
	layout, err := evm.LayoutOf(sig.Results)
	if err != nil {
		f.c.fail(load.Pos(), "%v", err)
		return true
	}
	f.outR = max(f.outR, layout.RCount)
	f.outP = max(f.outP, layout.PCount)
	f.emit(evm.WithBC(evm.OpCallHost, 0, index))
	f.outMove(layout.Slots[0], f.regs[load][0], true)
	return true
}
