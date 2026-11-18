package evmcompiler

import (
	"go/types"
	"slices"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/values"
	"golang.org/x/tools/go/ssa"
)

type nativeReceiverMethod struct {
	origin   *types.Func
	receiver *values.Type
}

type nativeInstance struct {
	binding nativeBinding
	args    []*values.Type
}

func (c *typedCompiler) nativeBindingFor(fn *ssa.Function) (nativeBinding, []*values.Type, bool) {
	if fn == nil || len(fn.Blocks) != 0 {
		return nativeBinding{}, nil, false
	}
	obj, ok := fn.Object().(*types.Func)
	if !ok {
		return nativeBinding{}, nil, false
	}
	binding, ok := c.env.natives[obj.Origin()]
	if c.env.genericNativeDecls[obj.Origin()] {
		if genericTemplate(fn) {
			return nativeBinding{}, nil, false
		}
		receiver, err := c.env.valueType(fn.Signature.Recv().Type())
		if err == nil {
			binding, ok = c.env.nativeReceiverMethods[nativeReceiverMethod{obj.Origin(), receiver}]
		}
		if err != nil || !ok {
			c.fail(fn.Pos(), "no native %s for receiver %s", obj.Name(), fn.Signature.Recv().Type())
			return nativeBinding{}, nil, false
		}
	}
	if !ok || binding.function == nil {
		return binding, nil, ok
	}
	if binding.function.Instantiate == nil {
		if host := c.env.packages[obj.Pkg().Path()]; host != nil && host.HasSource() {
			actual, err := c.env.signature(fn.Signature)
			if err != nil || !slices.Equal(actual.Params, binding.function.Signature.Params) || !slices.Equal(actual.Results, binding.function.Signature.Results) || fn.Signature.Variadic() != binding.function.Variadic {
				c.fail(fn.Pos(), "native source signature mismatch for %s", fn)
				return nativeBinding{}, nil, false
			}
		}
		return binding, nil, true
	}
	if genericTemplate(fn) {
		return nativeBinding{}, nil, false
	}
	if cached, ok := c.nativeInstances[fn]; ok {
		return cached.binding, cached.args, true
	}
	var args []*values.Type
	for _, arg := range fn.TypeArgs() {
		t, err := c.env.valueType(arg)
		if err != nil {
			c.fail(fn.Pos(), "%v", err)
			return nativeBinding{}, nil, false
		}
		args = append(args, t)
	}
	bound, err := binding.function.Instantiate(slices.Clone(args))
	if err != nil {
		c.fail(fn.Pos(), "%v", err)
		return nativeBinding{}, nil, false
	}
	actual, err := c.env.signature(fn.Signature)
	if err != nil || !slices.Equal(actual.Params, bound.Signature.Params) || !slices.Equal(actual.Results, bound.Signature.Results) || fn.Signature.Variadic() != bound.Variadic {
		c.fail(fn.Pos(), "generic native signature mismatch for %s", fn)
		return nativeBinding{}, nil, false
	}
	if bound.Instantiate != nil || bound.Call == nil && bound.Direct.Call == nil || bound.Direct.Op != 0 && bound.Direct.Call == nil {
		c.fail(fn.Pos(), "invalid generic native binding for %s", fn)
		return nativeBinding{}, nil, false
	}
	if err := bound.Ownership.Validate(bound.Signature); err != nil {
		c.fail(fn.Pos(), "%v", err)
		return nativeBinding{}, nil, false
	}
	bound.Name = binding.function.Name
	if bound.Direct.Op != 0 || bound.Direct.Call != nil {
		var packages []evm.Package
		for _, pkg := range c.env.packages {
			packages = append(packages, *pkg)
		}
		instance := evm.Package{Path: binding.key.pkg}
		if binding.key.receiver == nil {
			instance.Functions = []evm.ExternalFunction{bound}
		} else {
			instance.Methods = []evm.ExternalMethod{{Name: bound.Name, Receiver: binding.key.receiver, Signature: evm.Signature{Params: bound.Signature.Params[1:], Results: bound.Signature.Results}, Direct: bound.Direct, Variadic: bound.Variadic}}
		}
		if err := evm.ValidateDirectBindings(append(packages, instance)); err != nil {
			c.fail(fn.Pos(), "%v", err)
			return nativeBinding{}, nil, false
		}
	}
	binding.function = &bound
	if c.nativeInstances == nil {
		c.nativeInstances = make(map[*ssa.Function]nativeInstance)
	}
	c.nativeInstances[fn] = nativeInstance{binding, args}
	return binding, args, true
}
