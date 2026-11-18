package evmcompiler

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"reflect"
	"strconv"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/values"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/ssa"
)

type recordProperty struct {
	owner         *evm.RecordType
	prop          kvo.PropImpl
	valueReceiver bool
}

func (p recordProperty) method() string {
	if p.valueReceiver {
		return fmt.Sprintf("Evm$value%d", p.prop.Code())
	}
	return fmt.Sprintf("Evm$key%d", p.prop.Code())
}

// The public type check sees KVO properties. SSA sees opaque handles and
// typed address intrinsics. Go itself schedules assignment evaluation,
// compound assignments and increments before the intrinsics are lowered.
func (e *typeEnvironment) declareRecordIdentities(pkg *types.Package, records []*evm.RecordType) error {
	type identity struct {
		model   *kvo.Model
		mutable bool
	}
	byModel := map[identity]*evm.RecordType{}
	for _, r := range records {
		if r == nil || r.Model == nil || r.Pointer == nil || r.Pointer.Repr() != recordRepr(r) || !token.IsExported(r.Name) || !token.IsIdentifier(r.Name) {
			return fmt.Errorf("invalid KVO record declaration")
		}
		key := identity{r.Model, r.Mutable}
		if byModel[key] != nil {
			return fmt.Errorf("duplicate KVO model %s", r.Name)
		}
		byModel[key] = r
		obj := types.NewTypeName(token.NoPos, pkg, r.Name, nil)
		if pkg.Scope().Insert(obj) != nil {
			return fmt.Errorf("duplicate type %s", r.Name)
		}
		named := types.NewNamed(obj, nil, nil)
		if err := e.bindNative(r.Value, named); err != nil {
			return err
		}
		if err := e.bindNative(r.Pointer, types.NewPointer(named)); err != nil {
			return err
		}
	}
	return nil
}

func (e *typeEnvironment) defineRecords(pkg *types.Package, records []*evm.RecordType) error {
	type nativeScalar struct {
		typ       *values.Type
		ambiguous bool
	}
	scalars := make(map[reflect.Type]nativeScalar)
	for typ, decl := range e.nativeTypes {
		shape := decl.decl.Shape
		if decl.decl.Name == "" || decl.decl.ScalarBase == nil || shape.Native == nil {
			continue
		}
		key := shape.Native.GoType
		if key == nil || key.PkgPath() == "" || key.Name() == "" {
			continue
		}
		old, exists := scalars[key]
		scalars[key] = nativeScalar{typ: typ, ambiguous: old.ambiguous || exists && old.typ != typ}
	}
	type identity struct {
		model   *kvo.Model
		mutable bool
	}
	byModel := map[identity]*evm.RecordType{}
	names := map[*evm.RecordType]*types.Named{}
	for _, r := range records {
		byModel[identity{r.Model, r.Mutable}] = r
		names[r] = pkg.Scope().Lookup(r.Name).Type().(*types.Named)
	}
	for _, r := range records {
		named := names[r]
		sealType := types.Type(types.NewInterfaceType(nil, nil).Complete())
		if !r.Mutable && r.NativeAllocation != nil {
			element := types.Type(types.Typ[types.Uint8])
			if !r.NativeAllocation.Comparable() {
				element = types.NewSignatureType(nil, nil, nil, nil, nil, false)
			}
			sealType = types.NewArray(element, 0)
		}
		seal := types.NewField(token.NoPos, pkg, "seal"+r.Name, sealType, false)
		fields := []*types.Var{seal}
		selectors := map[string]bool{seal.Name(): true}
		var fieldErr error
		var fieldType func(kvo.AnyType) types.Type
		fieldType = func(k kvo.AnyType) types.Type {
			if child := k.Model(); child != nil {
				if decl := byModel[identity{child, r.Mutable}]; decl != nil {
					return e.toGo[decl.Pointer]
				}
				return nil
			}
			for path, pkg := range e.packages {
				for _, shared := range pkg.SharedTypes {
					if native, ok := k.(interface{ NativeType() reflect.Type }); ok && shared.Native == native.NativeType() {
						if _, err := e.importPackage(path); err != nil {
							fieldErr = err
							return nil
						}
						return e.toGo[shared.Type]
					}
				}
			}
			if native, ok := k.(interface{ NativeType() reflect.Type }); ok {
				entry := scalars[native.NativeType()]
				if entry.ambiguous {
					fieldErr = fmt.Errorf("ambiguous native scalar %s", native.NativeType())
					return nil
				}
				scalar := entry.typ
				if scalar != nil {
					// Scalar identities are declared before record fields. Importing
					// their consumer functions here can cycle back into this model.
					if _, err := e.declarePackage(e.nativeTypes[scalar].pkg); err != nil {
						fieldErr = err
						return nil
					}
					t := e.toGo[scalar]
					if t == nil {
						fieldErr = fmt.Errorf("native scalar %s is not declared", scalar.Name())
					}
					return t
				}
			}
			if scalar := evm.RecordScalarType(k); scalar != nil {
				t, err := e.goType(scalar, pkg)
				if err != nil {
					fieldErr = err
				}
				return t
			}
			switch k := k.(type) {
			case *kvo.ScalarDataType:
				if k.NativeType().Kind() == reflect.Slice {
					return types.NewSlice(types.Typ[types.Uint8])
				}
			case *kvo.MapType:
				a, b := fieldType(k.MapKeyType()), fieldType(k.ItemType())
				if a != nil && b != nil {
					return types.NewMap(a, b)
				}
			case *kvo.ListType:
				if elem := fieldType(k.ItemType()); elem != nil {
					return types.NewSlice(elem)
				}
			}
			return nil
		}
		selectorTypes := make(map[kvo.PropCode]types.Type)
		for _, prop := range r.Model.AllProps() {
			t := fieldType(prop.AnyType())
			if fieldErr != nil {
				return fmt.Errorf("%s.%s: %w", r.Name, prop.Name(), fieldErr)
			}
			if t == nil {
				if r.SchemaView {
					continue
				}
				return fmt.Errorf("%s.%s: unsupported KVO property type %s", r.Name, prop.Name(), prop.AnyType().Name())
			}
			if prop.Code() >= evm.VMFieldBase || !token.IsIdentifier(prop.Name()) || prop.Name() == "_" || prop.Name() == seal.Name() {
				return fmt.Errorf("invalid KVO property %s.%s", r.Name, prop.Name())
			}
			property := recordProperty{owner: r, prop: prop}
			if e.recordFacade {
				selectorTypes[prop.Code()] = t
			} else {
				recv := types.NewVar(token.NoPos, pkg, "", e.toGo[r.Pointer])
				res := types.NewVar(token.NoPos, pkg, "", types.NewPointer(t))
				sig := types.NewSignatureType(recv, nil, nil, nil, types.NewTuple(res), false)
				fn := types.NewFunc(token.NoPos, pkg, property.method(), sig)
				named.AddMethod(fn)
				e.recordMethods[fn] = property
				if !r.Mutable {
					property.valueReceiver = true
					recv := types.NewVar(token.NoPos, pkg, "", named)
					res := types.NewVar(token.NoPos, pkg, "", t)
					fn := types.NewFunc(token.NoPos, pkg, property.method(), types.NewSignatureType(recv, nil, nil, nil, types.NewTuple(res), false))
					named.AddMethod(fn)
					e.recordMethods[fn] = property
				}
			}
		}
		if e.recordFacade {
			for name, prop := range r.Model.Selectors() {
				t := selectorTypes[prop.Code()]
				if t == nil { // Unsupported properties are omitted from schema views.
					continue
				}
				if selectors[name] {
					return fmt.Errorf("conflicting KVO selector %s.%s", r.Name, name)
				}
				selectors[name] = true
				field := types.NewField(token.NoPos, e.guest, name, t, false)
				fields = append(fields, field)
				e.recordFields[field] = recordProperty{owner: r, prop: prop}
			}
		}
		named.SetUnderlying(types.NewStruct(fields, nil))
	}
	return nil
}
func hasJSON(t kvo.AnyType) bool {
	switch t := t.(type) {
	case *kvo.JSONType:
		return true
	case *kvo.ListType:
		return hasJSON(t.ItemType())
	case *kvo.MapType:
		return hasJSON(t.ItemType())
	}
	return false
}
func (c *typedCompiler) registerJSONTypes(pos token.Pos) {
	if c.module.JSONTypes.Object != 0 {
		return
	}
	anyT := c.env.toGo[values.AnyType]
	id := func(t types.Type) uint32 { return c.dynamicType(t, pos) }
	c.module.JSONTypes = evm.JSONTypes{Boolean: id(types.Typ[types.Bool]), Integer: id(types.Typ[types.Int64]), Unsigned: id(types.Typ[types.Uint64]), Float: id(types.Typ[types.Float64]), String: id(types.Typ[types.String]), Array: id(types.NewSlice(anyT)), Object: id(types.NewMap(types.Typ[types.String], anyT))}
}

func rewriteRecordFields(file *ast.File, info *types.Info, fields map[*types.Var]recordProperty) {
	astutil.Apply(file, nil, func(c *astutil.Cursor) bool {
		s, ok := c.Node().(*ast.SelectorExpr)
		if !ok {
			return true
		}
		sel := info.Selections[s]
		if sel == nil {
			return true
		}
		field, _ := sel.Obj().(*types.Var)
		prop, ok := fields[field]
		if !ok {
			return true
		}
		_, pointer := sel.Recv().Underlying().(*types.Pointer)
		prop.valueReceiver = !prop.owner.Mutable && !pointer
		if prop.valueReceiver && info.Types[s.X].Addressable() {
			switch ast.Unparen(s.X).(type) {
			case *ast.SelectorExpr, *ast.IndexExpr:
				// Read addressable aggregate rows without copying their native value.
				s.X = &ast.ParenExpr{X: &ast.UnaryExpr{Op: token.AND, X: s.X}}
				prop.valueReceiver = false
			}
		}
		s.Sel = &ast.Ident{NamePos: s.Sel.Pos(), Name: prop.method()}
		if prop.valueReceiver {
			c.Replace(&ast.CallExpr{Fun: s})
			return true
		}
		c.Replace(&ast.StarExpr{Star: s.Pos(), X: &ast.CallExpr{Fun: s}})
		return true
	})
}
func (f *lowerer) recordRef(v ssa.Value) (*ssa.Call, recordProperty, bool) {
	call, ok := v.(*ssa.Call)
	if !ok {
		return nil, recordProperty{}, false
	}
	fn := call.Call.StaticCallee()
	if fn == nil {
		return nil, recordProperty{}, false
	}
	obj, _ := fn.Object().(*types.Func)
	p, ok := f.c.env.recordMethods[obj]
	return call, p, ok
}
func (f *lowerer) recordInstruction(ins ssa.Instruction) bool {
	var address ssa.Value
	var in, out []vreg
	var typ types.Type
	kind := evm.ExtendedGetKey
	switch i := ins.(type) {
	case *ssa.Call:
		_, property, ok := f.recordRef(i)
		if !ok {
			return false
		}
		if property.valueReceiver {
			receiver := i.Call.Args[0]
			if hasJSON(property.prop.AnyType()) {
				f.c.registerJSONTypes(i.Pos())
			}
			f.extended(evm.ExtendedGetKey, f.shape(i.Type()), f.regs[receiver], f.regs[i], property.prop.Code(), evm.RecordValueReceiver, f.shape(receiver.Type()))
			ops := f.c.module.ExtendedOps
			ops[len(ops)-1].Record = property.owner
			return true
		}
		for _, use := range *i.Referrers() {
			switch u := use.(type) {
			case *ssa.UnOp:
				if u.Op == token.MUL {
					continue
				}
			case *ssa.Store:
				if u.Addr == i && u.Val != i {
					continue
				}
			}
			f.c.fail(use.Pos(), "cannot take the address of a KVO property")
		}
		return true
	case *ssa.Store:
		address = i.Addr
		kind = evm.ExtendedSetKey
		typ = i.Val.Type()
	case *ssa.UnOp:
		if i.Op != token.MUL {
			return false
		}
		address = i.X
		typ = i.Type()
		out = f.regs[i]
	default:
		return false
	}
	ref, p, ok := f.recordRef(address)
	if !ok {
		return false
	}
	in = []vreg{f.regs[ref.Call.Args[0]][0]}
	if v, ok := ins.(ssa.Value); ok {
		if _, ok := f.recordLists[v]; ok {
			if f.directRecordList(v) == nil {
				f.recordListOp(evm.ExtendedRecordList, v, in, out)
			}
			return true
		}
	}
	if store, ok := ins.(*ssa.Store); ok {
		if !p.owner.Mutable || p.prop.TypeModel() != nil {
			f.c.fail(store.Pos(), "cannot assign %s.%s; write scalar properties of a mutable record", p.owner.Name, p.prop.Name())
			return true
		}
		in = append(in, f.regs[store.Val][0])
	}
	if f.optimizing() && kind == evm.ExtendedGetKey && !p.owner.Mutable && evm.RecordWordType(p.prop) != nil && ins.Block() == ref.Block() && f.foldChildWord(ref, p, typ, out[0]) {
		return true
	}
	if p.owner.Mutable && p.prop.TypeModel() != nil {
		f.markLocalRead(ins.(ssa.Value))
	}
	if hasJSON(p.prop.AnyType()) {
		f.c.registerJSONTypes(ins.Pos())
	}
	f.extended(kind, f.shape(typ), in, out, p.prop.Code(), 0, nil)
	ops := f.c.module.ExtendedOps
	ops[len(ops)-1].Record = p.owner
	return true
}

// Fuse adjacent reads only when the intermediate handle has exactly one use.
// The fused word keeps the position of the child read.
func (f *lowerer) foldChildWord(ref *ssa.Call, leaf recordProperty, typ types.Type, dst vreg) bool {
	child, ok := ref.Call.Args[0].(*ssa.UnOp)
	if !ok || child.Op != token.MUL || child.Block() != ref.Block() || len(*child.Referrers()) != 1 || len(*ref.Referrers()) != 1 {
		return false
	}
	pc := len(f.code.Code) - 1
	if pc < 0 || f.code.Code[pc].Op() != evm.OpExtended {
		return false
	}
	op := &f.c.module.ExtendedOps[f.code.Code[pc].BC()]
	if op.Kind != evm.ExtendedGetKey || op.Record.Mutable || op.Shape.Kind != values.TypedPairKind || op.Out[0] != operand(f.regs[child][0]) {
		return false
	}
	op.Kind, op.ChildKey = evm.ExtendedGetChildKey, leaf.prop.Code()
	op.Source, op.Shape, op.Out[0] = op.Shape, f.shape(typ), operand(dst)
	delete(f.code.LocalRecordReads, pc)
	return true
}

func (f *lowerer) isReadRecord(t *values.Type) bool {
	for _, p := range f.c.env.packages {
		for _, r := range p.Records {
			if r.Pointer == t {
				return !r.Mutable
			}
		}
	}
	return false
}

// A nonescaping row may occupy one slot per producing instruction and call
// depth. Every observer must finish before that instruction runs again.
func (f *lowerer) markLocalRead(v ssa.Value) {
	if !f.optimizing() || !f.readUsesLocal(v) {
		return
	}
	if f.code.LocalRecordReads == nil {
		f.code.LocalRecordReads = make(map[int]bool)
	}
	f.code.LocalRecordReads[len(f.code.Code)] = true
}

func (f *lowerer) readUsesLocal(v ssa.Value) bool {
	uses := *v.Referrers()
	// The common "for c := Next(); c != nil; c = Next()" joins its two
	// producers at the loop header. Only forward a final result directly
	// through the jump: running user code before the jump could observe the
	// previous iteration's row after its slot had already been overwritten.
	if len(uses) == 1 {
		if phi, ok := uses[0].(*ssa.Phi); ok {
			block := v.(ssa.Instruction).Block()
			code := block.Instrs
			if len(code) < 2 || code[len(code)-2] != v.(ssa.Instruction) || len(block.Succs) != 1 || block.Succs[0] != phi.Block() {
				return false
			}
			uses = *phi.Referrers()
			v = phi
		}
	}
	for _, use := range uses {
		switch u := use.(type) {
		case *ssa.Call:
			// A mutable grandchild can retain this handle as its parent.
			if _, p, ok := f.recordRef(u); ok && u.Call.Args[0] == v && (!p.owner.Mutable || p.prop.TypeModel() == nil) {
				continue
			}
		case *ssa.BinOp:
			if u.Op == token.EQL || u.Op == token.NEQ {
				other := u.X
				if other == v {
					other = u.Y
				}
				if c, ok := other.(*ssa.Const); ok && c.IsNil() {
					continue
				}
			}
		}
		return false
	}
	return true
}

// The private identifier cannot be spelled in guest source. The public
// spelling names a type, so ordinary selectors on variables stay untouched.
func operationIdent(path string, f *evm.ExternalFunction) string {
	return fmt.Sprintf("Evm$%x$%s$%s", path, f.On.Name, f.Name)
}

func (e *typeEnvironment) rewriteRecordOperations(file *ast.File, info *types.Info, aliases map[string]string) bool {
	if info == nil {
		return false
	}
	changed := false
	astutil.Apply(file, nil, func(c *astutil.Cursor) bool {
		sel, ok := c.Node().(*ast.SelectorExpr)
		if !ok {
			return true
		}
		tv := info.Types[sel.X]
		if !tv.IsType() {
			return true
		}
		named, ok := types.Unalias(tv.Type).(*types.Named)
		if !ok || named.Obj().Pkg() == nil {
			return true
		}
		path := named.Obj().Pkg().Path()
		pkg := e.packages[path]
		if pkg == nil {
			return true
		}
		for i := range pkg.Functions {
			f := &pkg.Functions[i]
			if f.On == nil || f.On.Name != named.Obj().Name() || f.Name != sel.Sel.Name {
				continue
			}
			name := operationIdent(path, f)
			ident := &ast.Ident{NamePos: sel.Sel.Pos(), Name: name}
			if t, ok := sel.X.(*ast.SelectorExpr); ok {
				if id, ok := t.X.(*ast.Ident); ok {
					if owner, ok := info.Uses[id].(*types.PkgName); ok && owner.Imported().Path() == path {
						sel.X = id
						sel.Sel = ident
						changed = true
						return true
					}
				}
			}
			dotImported := false
			for _, imp := range file.Imports {
				importPath, _ := strconv.Unquote(imp.Path.Value)
				if importPath == path && imp.Name != nil && imp.Name.Name == "." {
					dotImported = true
					break
				}
			}
			if !dotImported {
				aliases[name] = path
			}
			c.Replace(ident)
			changed = true
			break
		}
		return true
	})
	return changed
}

func recordRepr(r *evm.RecordType) values.Repr {
	return values.Pair
}
func (e *typeEnvironment) isReadRecord(t *values.Type) bool {
	for _, p := range e.packages {
		for _, r := range p.Records {
			if r.Pointer == t {
				return !r.Mutable
			}
		}
	}
	return false
}

// A new defined type would inherit the opaque Go facade but not its row
// representation. Type aliases preserve the original identity and are allowed.
func (c *typedCompiler) checkRecordDerivedTypes(info *types.Info) {
	if info == nil {
		return
	}
	for _, obj := range info.Defs {
		name, ok := obj.(*types.TypeName)
		if !ok || name.IsAlias() {
			continue
		}
		t := name.Type().Underlying()
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem().Underlying()
		}
		if _, ok := t.(*types.Struct); !ok {
			continue
		}
		for _, pkg := range c.env.packages {
			for _, row := range pkg.Records {
				original := c.env.toGo[row.Value]
				if original != nil && name.Type() != original && types.Identical(t, original.Underlying()) {
					c.fail(name.Pos(), "cannot define a new type based on model row %s; use a type alias", row.Name)
					break
				}
			}
		}
	}
}
