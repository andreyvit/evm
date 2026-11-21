package evmcompiler

import (
	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/values"
	"go/token"
	"go/types"
	"golang.org/x/tools/go/ssa"
	"slices"
)

func withoutReceiver(sig *types.Signature) *types.Signature {
	return types.NewSignatureType(nil, nil, nil, sig.Params(), sig.Results(), sig.Variadic())
}
func (c *typedCompiler) dynamicType(t types.Type, pos token.Pos) uint32 {
	s, err := c.env.shape(t)
	if err != nil {
		c.fail(pos, "%v", err)
		return 0
	}
	if i, ok := c.dynamic[s]; ok {
		return i
	}
	if len(c.module.Types) == 0 {
		c.module.Types = append(c.module.Types, evm.DynamicType{}) // ID zero is the nil interface.
	}
	i := uint32(len(c.module.Types))
	c.dynamic[s] = i
	c.module.Types = append(c.module.Types, evm.DynamicType{Shape: s})
	set := c.pkg.Prog.MethodSets.MethodSet(t)
	var methods []evm.DynamicMethod
	for n := 0; n < set.Len(); n++ {
		sel := set.At(n)
		obj := sel.Obj().(*types.Func)
		// Generic methods require type arguments and cannot implement an interface method.
		if obj.Signature().TypeParams().Len() != 0 {
			continue
		}
		// Synthetic property addresses exist only during selector lowering.
		if _, intrinsic := c.env.recordMethods[obj]; intrinsic {
			continue
		}
		fn := c.pkg.Prog.MethodValue(sel)
		target, ok := c.methodTarget(fn, pos)
		if !ok {
			continue
		}
		sig := withoutReceiver(obj.Type().(*types.Signature))
		vs, err := c.env.signature(sig)
		if err != nil {
			c.fail(pos, "%v", err)
			continue
		}
		methods = append(methods, evm.DynamicMethod{ID: obj.Id(), Signature: vs, Variadic: sig.Variadic(), Function: target})
	}
	if base := c.env.base; base != nil {
		if shared := base.hostMethods[s]; shared != nil && sameMethodTable(methods, shared) {
			methods = shared
		}
	}
	c.module.Types[i].Methods = methods
	return i
}

// methodTarget returns the function a method table names. A native method
// that is not generic goes to the module's method imports: nothing but
// method tables names those, and a module compiled through an environment
// shares the environment's list of them.
func (c *typedCompiler) methodTarget(f *ssa.Function, pos token.Pos) (evm.FunctionRef, bool) {
	if _, ok := c.functions[f]; !ok {
		reported := len(c.errors)
		if binding, typeArgs, ok := c.nativeBindingFor(f); ok && len(typeArgs) == 0 && binding.function != nil && binding.function.Instantiate == nil && binding.key.receiver != nil {
			if i, ok := c.methodImport(binding); ok {
				return evm.MethodImportRef(i), true
			}
		}
		c.errors = c.errors[:reported] // target looks the binding up again
	}
	target, _, ok := c.target(f, pos)
	return target, ok
}

func (c *typedCompiler) methodImport(binding nativeBinding) (int, bool) {
	if base := c.env.base; base != nil {
		// The environment numbered the native methods for all its modules.
		n, ok := base.methodNumbers[binding.key]
		if !ok || base.methodBindings[n] != binding.function {
			return 0, false
		}
		c.module.MethodImports = base.methodImports
		return int(n), true
	}
	if i, ok := c.methodImports[binding.key]; ok {
		return int(i), true
	}
	if len(c.module.MethodImports) >= evm.MethodImportBase-1 {
		return 0, false
	}
	i := len(c.module.MethodImports)
	c.methodImports[binding.key] = uint32(i)
	external := binding.function
	c.module.MethodImports = append(c.module.MethodImports, evm.Import{Package: binding.key.pkg, Name: binding.key.name, Receiver: binding.key.receiver, Signature: evm.Signature{Params: slices.Clone(external.Signature.Params), Results: slices.Clone(external.Signature.Results)}, Ownership: external.Ownership.Clone()})
	return i, true
}
func (f *lowerer) makeInterface(i *ssa.MakeInterface) {
	s := f.shape(i.X.Type())
	if s == nil {
		return
	}
	idx := f.c.dynamicType(i.X.Type(), i.Pos())
	f.extended(evm.ExtendedMakeInterface, s, f.regs[i.X], f.regs[i], uint64(idx), 0, nil)
}
func (f *lowerer) typeAssert(i *ssa.TypeAssert) {
	s := f.shape(i.AssertedType)
	if s != nil {
		if _, isInterface := i.AssertedType.Underlying().(*types.Interface); !isInterface {
			f.c.dynamicType(i.AssertedType, i.Pos())
		}
		f.extended(evm.ExtendedTypeAssert, s, f.regs[i.X], f.regs[i], 0, 0, nil)
	}
}
func (f *lowerer) invoke(call *ssa.Call) {
	idx, params, results, ok := f.invokeDescriptor(&call.Call)
	if !ok {
		return
	}
	f.outMove(0, f.regs[call.Call.Value][0], false)
	for j, arg := range call.Call.Args {
		f.outMove(params.Slots[j]+1, f.regs[arg][0], false)
	}
	f.emit(evm.WithBC(evm.OpCallVirt, 0, idx))
	for j, r := range f.regs[call] {
		f.outMove(results.Slots[j], r, true)
	}
}
func (f *lowerer) invokeDescriptor(common *ssa.CallCommon) (uint32, evm.Layout, evm.Layout, bool) {
	sig := withoutReceiver(common.Method.Type().(*types.Signature))
	shape := f.shape(sig)
	if shape == nil {
		return 0, evm.Layout{}, evm.Layout{}, false
	}
	desc := evm.Invoke{Method: values.Method{ID: common.Method.Id(), Signature: shape}, InterfaceName: displayTypeName(common.Value.Type())}
	s, err := f.c.env.signature(sig)
	if err != nil {
		f.c.fail(f.pos, "%v", err)
		return 0, evm.Layout{}, evm.Layout{}, false
	}
	result, err := evm.LayoutOf(s.Results)
	if err != nil {
		f.c.fail(f.pos, "%v", err)
		return 0, evm.Layout{}, evm.Layout{}, false
	}
	params, err := evm.LayoutOf(s.Params)
	if err != nil {
		f.c.fail(f.pos, "%v", err)
		return 0, evm.Layout{}, evm.Layout{}, false
	}
	f.outR = max(f.outR, params.RCount+1, result.RCount)
	f.outP = max(f.outP, params.PCount+1, result.PCount)
	idx, exists := f.c.invokes[desc]
	if !exists {
		idx = uint32(len(f.c.module.Invokes))
		f.c.module.Invokes = append(f.c.module.Invokes, desc)
		f.c.invokes[desc] = idx
	}
	return idx, params, result, true
}
