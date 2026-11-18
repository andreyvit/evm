package evmbind

import (
	"fmt"
	"go/types"
	"reflect"
	"slices"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/values"
)

// FunctionInstance binds one compiled native specialization. Args are explicit:
// reflection cannot recover a function's generic type arguments.
type FunctionInstance struct {
	Args     []reflect.Type
	Function any
}

type sourceGenericFunction struct {
	name      string
	signature *types.Signature
	instances []sourceFunctionInstance
}
type sourceFunctionInstance struct {
	args     []*values.Type
	op       *operation
	function any
}

func functionNames(opt Options) map[string]bool {
	names := make(map[string]bool, len(opt.Functions)+len(opt.GenericFunctions))
	for name := range opt.Functions {
		names[name] = true
	}
	for name := range opt.GenericFunctions {
		names[name] = true
	}
	for name := range opt.Values {
		names[name] = true
	}
	return names
}

func (s *source) addGenericFunction(f *types.Func, instances []FunctionInstance) {
	sig := f.Signature()
	if sig.TypeParams().Len() == 0 {
		fail("%s is not a generic function", f.Name())
	}
	if len(instances) == 0 {
		fail("generic function %s requires native instances", f.Name())
	}
	fn := &sourceGenericFunction{name: f.Name(), signature: sig}
	for _, in := range instances {
		if len(in.Args) != sig.TypeParams().Len() {
			fail("instantiate %s: expected %d type arguments, got %d", f.Name(), sig.TypeParams().Len(), len(in.Args))
		}
		native := reflect.ValueOf(in.Function)
		if !native.IsValid() || native.Kind() != reflect.Func || native.IsNil() {
			fail("invalid native function instance %s", f.Name())
		}
		args := make([]types.Type, len(in.Args))
		vmArgs := make([]*values.Type, len(in.Args))
		for i, t := range in.Args {
			args[i] = s.instanceArgument(t)
			vmArgs[i] = s.node(args[i]).shape.Type
		}
		instantiated, err := types.Instantiate(nil, sig, args, true)
		if err != nil {
			fail("instantiate %s: %v", f.Name(), err)
		}
		for _, old := range fn.instances {
			if slices.Equal(old.args, vmArgs) {
				fail("duplicate native instance of %s", f.Name())
			}
		}
		op := s.op(types.NewFunc(f.Pos(), f.Pkg(), f.Name(), instantiated.(*types.Signature)), nil)
		op.packageFunction = true
		fn.instances = append(fn.instances, sourceFunctionInstance{args: vmArgs, op: op, function: in.Function})
	}
	s.genericFunctions = append(s.genericFunctions, fn)
}

func (s *Schema) installGenericFunctions(native map[*node]reflect.Type) {
	for _, fn := range s.src.genericFunctions {
		functions := make([]evm.ExternalFunction, len(fn.instances))
		for i, in := range fn.instances {
			wrapper := reflectWrapper(in.op, nil, native, in.function)
			functions[i] = evm.ExternalFunction{Signature: in.op.sig(), Variadic: in.op.variadic, Call: s.bind(entry{op: in.op, wrapper: wrapper}, in.function)}
		}
		s.pkg.Functions = append(s.pkg.Functions, evm.ExternalFunction{Name: fn.name, Instantiate: func(args []*values.Type) (evm.ExternalFunction, error) {
			for i, in := range fn.instances {
				if slices.Equal(in.args, args) {
					return functions[i], nil
				}
			}
			names := make([]string, len(args))
			for i, t := range args {
				names[i] = t.Name()
			}
			return evm.ExternalFunction{}, fmt.Errorf("no native instance of %s for %v", fn.name, names)
		}})
	}
}
