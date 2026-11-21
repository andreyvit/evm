package evm

import (
	"fmt"
	"slices"
	"unsafe"

	"github.com/andreyvit/evm/values"
)

// MethodTables are the method tables that the modules of one compile
// environment share: the list every such module has as its MethodImports,
// and the host types whose methods name nothing but that list. A module that
// lists one of the types has the same Methods slice as here.
//
// A link environment prepared with the tables binds them once, and its
// programs share the result. Nothing may change them afterwards.
type MethodTables struct {
	Imports []Import
	Types   []DynamicType
}

// sharedMethods is one MethodTables bound to the packages of a link
// environment. Nothing here changes after the environment is built.
type sharedMethods struct {
	imports []Import
	// native is the index in LinkEnvironment.natives of each import, or -1
	// for one that failed or that is bound for each program.
	native []int32
	errs   map[int]error // why an import failed
	// perProgram reports that some import is bound for each program. A
	// module that names one binds all of its method imports itself.
	perProgram bool
	types      map[*values.Shape]*sharedMethodTable
}

// sharedMethodTable is the linked method table of a host type, or the error
// every link of a module that lists the type reports.
type sharedMethodTable struct {
	methods []DynamicMethod // as modules list them
	linked  map[uint32]*DynamicMethod
	err     error
}

// prepareMethods binds the method tables. A table that cannot be bound fails
// the links of the modules that list it, as it does without an environment.
func (e *LinkEnvironment) prepareMethods(tables []MethodTables) {
	e.methods = make(map[*Import]*sharedMethods)
	e.methodIDs = make(map[string]uint32)
	methodID := func(name string) uint32 {
		id, ok := e.methodIDs[name]
		if !ok {
			id = uint32(len(e.methodIDs))
			e.methodIDs[name] = id
		}
		return id
	}
	for _, table := range tables {
		if len(table.Imports) == 0 || e.methods[&table.Imports[0]] != nil {
			continue
		}
		s := &sharedMethods{imports: table.Imports, native: make([]int32, len(table.Imports)), types: make(map[*values.Shape]*sharedMethodTable, len(table.Types))}
		for i := range table.Imports {
			s.native[i] = -1
			imp := &table.Imports[i]
			if imp.Receiver == nil {
				// Only a method can be a method. A link reports it.
				s.perProgram = true
				continue
			}
			var scratch linkedNative
			prepared, err := e.linkImportQuietly(imp, &scratch)
			switch {
			case err != nil:
				if s.errs == nil {
					s.errs = make(map[int]error)
				}
				s.errs[i] = err
			case prepared < 0:
				s.perProgram = true
			default:
				s.native[i] = int32(prepared)
			}
		}
		e.methods[&table.Imports[0]] = s
		if s.perProgram {
			continue
		}
		for _, t := range table.Types {
			if t.Shape == nil || t.Shape.Type == nil || len(t.Methods) == 0 || s.types[t.Shape] != nil {
				continue
			}
			shared := true
			for _, method := range t.Methods {
				if _, ok := method.Function.MethodImport(); !ok {
					shared = false
				}
			}
			if !shared {
				continue
			}
			linked := &sharedMethodTable{methods: t.Methods}
			linked.linked, linked.err = linkMethods(t.Shape.Type, t.Methods, methodID, func(ref FunctionRef) (methodTarget, bool, error) {
				n, _ := ref.MethodImport()
				return e.sharedMethodTarget(s, n)
			})
			s.types[t.Shape] = linked
		}
	}
}

// linkImportQuietly binds an import no module may ever name. A panic counts
// as a binding to make for each program, which then panics in the link that
// names the import.
func (e *LinkEnvironment) linkImportQuietly(imp *Import, dst *linkedNative) (prepared int, err error) {
	defer func() {
		if recover() != nil {
			prepared, err = -1, nil
		}
	}()
	return e.linkImport(imp, nil, dst)
}

func (e *LinkEnvironment) sharedMethodTarget(s *sharedMethods, n int) (methodTarget, bool, error) {
	if n >= len(s.imports) {
		return methodTarget{}, false, nil
	}
	i := s.native[n]
	if i < 0 {
		return methodTarget{}, false, s.errs[n]
	}
	return methodTarget{ref: MethodImportRef(int(i)), sig: e.natives[i].Signature, receiver: true}, true, nil
}

// methodTarget is the function a method table names, as a program refers to it.
type methodTarget struct {
	ref      FunctionRef
	sig      Signature
	receiver bool // the first parameter is a receiver slot
	captures bool
}

// linkMethods checks the method table of a concrete type and returns it by
// method ID, with the references target resolved.
func linkMethods(typ *values.Type, methods []DynamicMethod, methodID func(string) uint32, target func(FunctionRef) (methodTarget, bool, error)) (map[uint32]*DynamicMethod, error) {
	if len(methods) == 0 {
		return nil, nil
	}
	linked := make(map[uint32]*DynamicMethod, len(methods))
	methods = slices.Clone(methods)
	for i := range methods {
		method := &methods[i]
		if method.ID == "" {
			return nil, fmt.Errorf("empty method identity")
		}
		id := methodID(method.ID)
		if _, ok := linked[id]; ok {
			return nil, fmt.Errorf("duplicate method identity")
		}
		t, ok, err := target(method.Function)
		if err != nil {
			return nil, err
		}
		if !ok || len(t.sig.Params) == 0 || t.sig.Params[0] != typ || !sameSignature(Signature{t.sig.Params[1:], t.sig.Results}, method.Signature) {
			return nil, fmt.Errorf("invalid dynamic method target %s", method.ID)
		}
		if !t.receiver {
			return nil, fmt.Errorf("dynamic target lacks receiver slot")
		}
		if t.captures {
			return nil, fmt.Errorf("method target captures")
		}
		method.Signature = copySignature(method.Signature)
		method.Function = t.ref
		linked[id] = method
	}
	return linked, nil
}

// methodLinker builds the method tables of one program.
type methodLinker struct {
	e      *LinkEnvironment
	p      *Program
	m      *Module
	shapes *shapeSnapshots
	// shared is the environment's binding of the module's method imports,
	// or nil for a module that binds its own.
	shared *sharedMethods
	own    []int32 // method import to its index in p.methodNatives plus one
}

func newMethodLinker(e *LinkEnvironment, p *Program, m *Module, shapes *shapeSnapshots) *methodLinker {
	l := &methodLinker{e: e, p: p, m: m, shapes: shapes}
	if len(m.MethodImports) == 0 {
		return l
	}
	s := e.methods[&m.MethodImports[0]]
	if s == nil || len(s.imports) != len(m.MethodImports) {
		return l
	}
	if s.perProgram {
		for _, t := range m.Types {
			for _, method := range t.Methods {
				if n, ok := method.Function.MethodImport(); ok && n < len(s.native) && s.native[n] < 0 && s.errs[n] == nil {
					return l
				}
			}
		}
	}
	l.shared = s
	p.methodNatives = e.natives
	return l
}

// link returns the method table of a type the module lists.
func (l *methodLinker) link(typ *values.Type, t *DynamicType) (map[uint32]*DynamicMethod, error) {
	if l.shared != nil {
		// The module lists the environment's own table for the type.
		if table := l.shared.types[t.Shape]; table != nil && len(table.methods) == len(t.Methods) && unsafe.SliceData(table.methods) == unsafe.SliceData(t.Methods) {
			return table.linked, table.err
		}
	}
	return linkMethods(typ, t.Methods, l.p.methodID, l.target)
}

func (l *methodLinker) target(ref FunctionRef) (methodTarget, bool, error) {
	p, m := l.p, l.m
	if n, ok := ref.MethodImport(); ok {
		if l.shared != nil {
			return l.e.sharedMethodTarget(l.shared, n)
		}
		if n >= len(m.MethodImports) {
			return methodTarget{}, false, nil
		}
		if l.own == nil {
			l.own = make([]int32, len(m.MethodImports))
		}
		if l.own[n] == 0 {
			p.methodNatives = append(p.methodNatives, linkedNative{})
			if _, err := l.e.linkImport(&m.MethodImports[n], l.shapes, &p.methodNatives[len(p.methodNatives)-1]); err != nil {
				return methodTarget{}, false, err
			}
			l.own[n] = int32(len(p.methodNatives))
		}
		i := int(l.own[n] - 1)
		return methodTarget{ref: MethodImportRef(i), sig: p.methodNatives[i].Signature, receiver: m.MethodImports[n].Receiver != nil}, true, nil
	}
	sig, ok := p.functionSignature(ref)
	if !ok {
		return methodTarget{}, false, nil
	}
	if ref.Native {
		if uint64(ref.Index) >= uint64(len(m.Imports)) {
			return methodTarget{}, false, nil
		}
		return methodTarget{ref: ref, sig: sig, receiver: m.Imports[ref.Index].Receiver != nil}, true, nil
	}
	f := &p.functions[ref.Index]
	return methodTarget{ref: ref, sig: sig, receiver: f.Method, captures: len(f.Captures) > 0}, true, nil
}

func (p *Program) methodID(name string) uint32 {
	if id, ok := p.sharedMethodIDs[name]; ok {
		return id
	}
	if id, ok := p.methodIDs[name]; ok {
		return id
	}
	id := uint32(len(p.sharedMethodIDs) + len(p.methodIDs))
	p.methodIDs[name] = id
	return id
}

// lookupMethodID returns the ID of a method name that some type or
// interface the program or its environment links has.
func (p *Program) lookupMethodID(name string) (uint32, bool) {
	if id, ok := p.sharedMethodIDs[name]; ok {
		return id, true
	}
	id, ok := p.methodIDs[name]
	return id, ok
}
