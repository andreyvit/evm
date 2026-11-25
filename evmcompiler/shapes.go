package evmcompiler

import (
	"fmt"
	"go/types"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/nativebind"

	"github.com/andreyvit/evm/values"
)

func (e *typeEnvironment) shape(t types.Type) (result *values.Shape, err error) {
	t = types.Default(types.Unalias(t))
	if err := e.resolveDeclaredShape(t); err != nil {
		return nil, err
	}
	if s := e.shapeOf(t); s != nil {
		return s, nil
	}
	if n, ok := t.(*types.Named); ok && n.Obj().Name() == "Cache" && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == concurrencyPath && n.TypeArgs().Len() == 2 {
		for i := range 2 {
			arg := n.TypeArgs().At(i)
			if !e.cacheDataType(arg, i == 0, make(map[types.Type]bool)) {
				return nil, fmt.Errorf("concurrency.Cache cannot share %s", arg)
			}
		}
	}
	s := &values.Shape{Cells: 1, DisplayName: displayTypeName(t)}
	e.shapes.Set(t, s)
	e.shapeOrder = append(e.shapeOrder, s)
	if e.shaping == nil {
		e.shaping = map[*values.Shape]bool{}
	}
	e.shaping[s] = true
	defer func() {
		delete(e.shaping, s)
		if err != nil {
			e.shapes.Delete(t)
		}
	}()
	vt := e.typeOf(t)
	if d, ok := e.nativeTypes[vt]; ok && d.decl.Shape.CellStruct != nil {
		if err := e.checkCompactFields(t, d.decl.Shape); err != nil {
			return nil, err
		}
		// The storage is the host's; the name stays the one that source uses.
		name := s.DisplayName
		*s = *d.decl.Shape
		s.DisplayName = name
		return s, nil
	}
	if row := e.recordValues[vt]; e.recordFacade && row != nil && row.NativeAllocation != nil {
		// Selector fields exist only for type checking, not native storage.
		*s = *evm.ModelValueShape(row)
		return s, nil
	}
	if vt == values.TimeType {
		s.Kind = values.TimestampKind
		s.Type = vt
		s.Native = e.nativeLayout(vt)
		return s, nil
	}
	if vt == nil {
		if named, ok := t.(*types.Named); ok && named.Obj().Pkg() != nil {
			if pkg := e.packages[named.Obj().Pkg().Path()]; pkg != nil {
				if factory := pkg.StructValues[named.Obj().Name()]; factory != nil {
					args := make([]*values.Shape, named.TypeArgs().Len())
					for i := range args {
						args[i], err = e.shape(named.TypeArgs().At(i))
						if err != nil {
							return nil, err
						}
					}
					s.CellStruct = factory(types.TypeString(t, nil), args)
				}
			}
		}
	}
	repr := values.Raw
	switch u := t.Underlying().(type) {
	case *types.Basic:
		s.Bits = 64
		switch u.Kind() {
		case types.Bool:
			s.Kind = values.BoolKind
		case types.Int, types.Int8, types.Int16, types.Int32, types.Int64:
			s.Kind = values.IntKind
		case types.Uint, types.Uint8, types.Uint16, types.Uint32, types.Uint64, types.Uintptr:
			s.Kind = values.UintKind
		case types.Float32, types.Float64:
			s.Kind = values.FloatKind
		case types.String:
			s.Kind = values.StringKind
			repr = values.Pair
		case types.Complex64, types.Complex128:
			s.Kind = values.ComplexKind
			s.Bits = 64
			elem := types.Typ[types.Float32]
			if u.Kind() == types.Complex128 {
				s.Bits = 128
				s.Cells = 2
				repr = values.Pointer
				elem = types.Typ[types.Float64]
			}
			s.Elem, _ = e.shape(elem)
		default:
			return nil, fmt.Errorf("type %s is not implemented yet", t)
		}
		switch u.Kind() {
		case types.Int8, types.Uint8:
			s.Bits = 8
		case types.Int16, types.Uint16:
			s.Bits = 16
		case types.Int32, types.Uint32, types.Float32:
			s.Bits = 32
		}
	case *types.Interface:
		s.Kind = values.InterfaceKind
		repr = values.Pair
	case *types.Signature:
		s.Kind = values.FunctionKind
		repr = values.Pair
	case *types.Pointer:
		s.Kind = values.PointerKind
		repr = values.Pointer
		if _, err := e.shape(u.Elem()); err != nil {
			return nil, err
		}
		elem := e.typeOf(u.Elem())
		if e.nativeLayout(elem) != nil || e.requiresNativeStorage(u.Elem()) || e.containsModelRow(u.Elem()) || e.mayViewNative(u.Elem()) {
			repr = values.Pair
		}
		if vt != nil {
			if e.mayViewNative(u.Elem()) && vt.Repr() != values.Pair {
				return nil, fmt.Errorf("pointer %s requires native address tags", t)
			}
			repr = vt.Repr()
			if e.recordOwners[vt] != "" {
				s.Kind = values.TypedPairKind
				s.PointerLike = true
			}
		}
	case *types.Struct:
		s.Kind = values.StructKind
		repr = values.Pointer
		if s.CellStruct != nil {
			s.Kind = values.TypedPairKind
			repr = values.Pair
			vt = s.CellStruct.Type
			e.fromGo.Set(t, vt)
		}
	case *types.Array:
		s.Kind = values.ArrayKind
		repr = values.Pointer
		s.Len = uint64(u.Len())
	case *types.Slice:
		s.Kind = values.SliceKind
		repr = values.Pair
	case *types.Map:
		s.Kind = values.MapKind
		repr = values.Pointer
	default:
		return nil, fmt.Errorf("type %s is not implemented yet", t)
	}
	if vt != nil && vt.Repr() != repr {
		return nil, fmt.Errorf("native representation mismatch for %s", t)
	}
	if vt == nil {
		if s.Kind == values.StringKind {
			vt = values.NewOwnedType(types.TypeString(t, nil), repr, values.StringType.Own)
		} else {
			vt = values.NewType(types.TypeString(t, nil), repr)
		}
		e.fromGo.Set(t, vt)
	}
	s.Type = vt
	s.Native = e.nativeLayout(vt)
	s.NativeAllocation = e.nativeAllocations[vt]
	if named, ok := types.Unalias(t).(*types.Named); ok && s.Native == nil {
		// A script-declared slice or map type converts freely to and from its
		// underlying type, so both must use one storage. When a host binds
		// the underlying type natively, the named type is native too.
		switch under := named.Underlying().(type) {
		case *types.Slice, *types.Map:
			if err := e.resolveDeclaredShape(under); err != nil {
				return nil, err
			}
			underType := e.typeOf(under)
			if layout := e.nativeLayout(underType); layout != nil {
				s.Native = layout
				e.nativeLayouts[vt] = layout
			}
		}
	}
	if array, ok := t.Underlying().(*types.Array); ok && s.Native == nil {
		if err := e.resolveDeclaredShape(types.NewSlice(array.Elem())); err != nil {
			return nil, err
		}
		sliceType := e.typeOf(types.NewSlice(array.Elem()))
		layout := e.nativeLayout(sliceType)
		if layout == nil {
			elem, err := e.shape(array.Elem())
			if err != nil {
				return nil, err
			}
			layout = e.collectionLayouts[nativeCollectionKey{elem: elem.Type}]
		}
		if layout != nil && layout.Slice != nil {
			n := uint64(array.Len())
			if n > math.MaxUint32 || (layout.Slice.ElementSize != 0 && n > uint64(^uint(0)>>1)/layout.Slice.ElementSize) {
				return nil, fmt.Errorf("native array %s exceeds VM capacity", t)
			}
			s.Native = layout.Slice.Array(n)
			if layout.GoType == nil {
				physical := s.Native.GoType
				s.Native.GoType = nil
				e.setSyntheticStorage(s, physical, s.Native)
			} else {
				e.nativeLayouts[vt] = s.Native
			}
		}
	}
	switch u := t.Underlying().(type) {
	case *types.Interface:
		for i := 0; i < u.NumMethods(); i++ {
			m := u.Method(i)
			ms, e2 := e.shape(withoutReceiver(m.Type().(*types.Signature)))
			if e2 != nil {
				return nil, e2
			}
			s.Methods = append(s.Methods, values.Method{ID: m.Id(), Signature: ms})
		}
	case *types.Signature:
		s.Variadic = u.Variadic()
		if u.Recv() != nil {
			r, e2 := e.shape(u.Recv().Type())
			if e2 != nil {
				return nil, e2
			}
			s.Params = append(s.Params, r)
		}
		for i := 0; i < u.Params().Len(); i++ {
			p, e2 := e.shape(u.Params().At(i).Type())
			if e2 != nil {
				return nil, e2
			}
			s.Params = append(s.Params, p)
		}
		for i := 0; i < u.Results().Len(); i++ {
			r, e2 := e.shape(u.Results().At(i).Type())
			if e2 != nil {
				return nil, e2
			}
			s.Results = append(s.Results, r)
		}
	case *types.Pointer:
		s.Elem, err = e.shape(u.Elem())
	case *types.Slice:
		s.Elem, err = e.shape(u.Elem())
	case *types.Map:
		s.Key, err = e.shape(u.Key())
		if err == nil && !s.Key.Comparable() {
			return nil, fmt.Errorf("invalid map key type %s", u.Key())
		}
		if err == nil {
			s.Elem, err = e.shape(u.Elem())
		}
	case *types.Struct:
		s.Cells = 0
		for i := 0; i < u.NumFields(); i++ {
			field := u.Field(i)
			fs, e2 := e.shape(field.Type())
			if e2 != nil {
				return nil, e2
			}
			s.Fields = append(s.Fields, values.Field{Name: field.Name(), Shape: fs, Offset: s.Cells, Embedded: field.Embedded(), Tag: u.Tag(i)})
			s.Cells += fs.Cells
			if s.Cells > math.MaxUint32 {
				return nil, fmt.Errorf("type %s exceeds storage limit", t)
			}
		}
		if s.Native != nil && s.Native.OpaqueCell != 0 {
			s.Cells++
		}
	case *types.Array:
		s.Elem, err = e.shape(u.Elem())
		if err == nil {
			if s.Elem.Cells != 0 && s.Len > math.MaxUint32/s.Elem.Cells {
				return nil, fmt.Errorf("type %s exceeds storage limit", t)
			}
			s.Cells = s.Len * s.Elem.Cells
		}
	}
	if err != nil {
		return nil, err
	}
	if s.Native == nil && (s.Kind == values.SliceKind || s.Kind == values.MapKind) {
		key := nativeCollectionKey{elem: s.Elem.Type}
		if s.Key != nil {
			key.key = s.Key.Type
		}
		if layout := e.collectionLayouts[key]; layout != nil {
			s.Native = layout
			e.nativeLayouts[vt] = layout
		}
	}
	if (s.Kind == values.SliceKind || s.Kind == values.ArrayKind) && s.Elem.NativeAllocation != nil {
		element := s.Elem.NativeAllocation
		layout := nativebind.Layout(reflect.SliceOf(element.GoType), nil, false)
		layout.Slice.Element = element
		if s.Kind == values.ArrayKind {
			layout = layout.Slice.Array(s.Len)
			layout.Array.Element = element
		}
		s.NativeAllocation = layout
	}
	if s.CellStruct != nil {
		if len(s.Fields) != 2 || s.CellStruct.Type == nil || s.CellStruct.Type.Repr() != values.Pair || s.CellStruct.Get == nil || s.CellStruct.Pack == nil {
			return nil, fmt.Errorf("invalid compact struct %s", t)
		}
		if err := e.cellStructStorage(t, s); err != nil {
			return nil, err
		}
	}
	delete(e.shaping, s)
	if err := e.addSyntheticStorage(t, s); err != nil {
		return nil, err
	}
	return s, nil
}

// Match the source type spelling used by Go's formatter. go/types uses compact
// struct/interface spelling; reflect (and therefore fmt) includes inner spaces.
// checkCompactFields makes sure that a struct declared in source has the
// fields of the host's compact struct, in the same order. A field access
// reads a cell by position: if source swapped two fields, each name would
// read the other's value.
func (e *typeEnvironment) checkCompactFields(t types.Type, native *values.Shape) error {
	st, ok := t.Underlying().(*types.Struct)
	if !ok {
		return nil
	}
	mismatch := func(format string, args ...any) error {
		return fmt.Errorf("struct %s does not match its host declaration: %s", displayTypeName(t), fmt.Sprintf(format, args...))
	}
	if st.NumFields() != len(native.Fields) {
		return mismatch("it has %d fields, the host has %d", st.NumFields(), len(native.Fields))
	}
	for i, want := range native.Fields {
		f := st.Field(i)
		if f.Name() != want.Name || f.Embedded() != want.Embedded {
			return mismatch("field %d is %s, the host has %s", i, f.Name(), want.Name)
		}
		got, err := e.shape(f.Type())
		if err != nil {
			return err
		}
		// Host declarations leave some widths unset, a string's for one.
		w := want.Shape
		if got.Kind != w.Kind || got.Bits != 0 && w.Bits != 0 && got.Bits != w.Bits || got.Type != nil && w.Type != nil && got.Type != w.Type {
			return mismatch("field %s has another type than the host's", f.Name())
		}
	}
	return nil
}

func displayTypeName(t types.Type) string {
	t = types.Unalias(t)
	switch u := t.(type) {
	case *types.Basic:
		return types.Typ[u.Kind()].Name()
	case *types.Named:
		if u.Obj().Pkg() == nil {
			return u.Obj().Name()
		}
		name := u.Obj().Pkg().Name() + "." + u.Obj().Name()
		if u.TypeArgs().Len() > 0 {
			args := make([]string, u.TypeArgs().Len())
			for i := range args {
				args[i] = displayTypeName(u.TypeArgs().At(i))
			}
			name += "[" + strings.Join(args, ",") + "]"
		}
		return name
	case *types.Pointer:
		return "*" + displayTypeName(u.Elem())
	case *types.Slice:
		return "[]" + displayTypeName(u.Elem())
	case *types.Array:
		return fmt.Sprintf("[%d]%s", u.Len(), displayTypeName(u.Elem()))
	case *types.Map:
		return "map[" + displayTypeName(u.Key()) + "]" + displayTypeName(u.Elem())
	case *types.Struct:
		if u.NumFields() == 0 {
			return "struct {}"
		}
		fields := make([]string, u.NumFields())
		for i := range fields {
			f := u.Field(i)
			fields[i] = displayTypeName(f.Type())
			if !f.Embedded() {
				fields[i] = f.Name() + " " + fields[i]
			}
			if u.Tag(i) != "" {
				fields[i] += " " + strconv.Quote(u.Tag(i))
			}
		}
		return "struct { " + strings.Join(fields, "; ") + " }"
	case *types.Interface:
		if u.NumMethods() == 0 {
			return "any"
		}
		methods := make([]string, u.NumMethods())
		for i := range methods {
			m := u.Method(i)
			methods[i] = m.Name() + strings.TrimPrefix(displayTypeName(m.Type()), "func")
		}
		return "interface { " + strings.Join(methods, "; ") + " }"
	case *types.Signature:
		args := make([]string, u.Params().Len())
		for i := range args {
			args[i] = displayTypeName(u.Params().At(i).Type())
			if u.Variadic() && i == len(args)-1 {
				args[i] = "..." + strings.TrimPrefix(args[i], "[]")
			}
		}
		text := "func(" + strings.Join(args, ", ") + ")"
		res := make([]string, u.Results().Len())
		for i := range res {
			res[i] = displayTypeName(u.Results().At(i).Type())
		}
		if len(res) == 1 {
			text += " " + res[0]
		} else if len(res) > 1 {
			text += " (" + strings.Join(res, ", ") + ")"
		}
		return text
	}
	return types.TypeString(t, nil)
}

// Addresses of model slots may point into cells or a host backing array,
// independently of which host declarations this compilation imports.
func (e *typeEnvironment) containsModelRow(t types.Type) bool {
	var seen elementChain
	for seen.enter(t) {
		if vt := e.typeOf(t); vt != nil && e.recordOwners[vt] != "" {
			return true
		}
		switch a := t.Underlying().(type) {
		case *types.Array:
			t = a.Elem()
		case *types.Slice:
			t = a.Elem()
		default:
			return false
		}
	}
	return false
}

// elementChain stops a walk through element types when it returns to a type
// it has left, as in type T []T, or type A []B with type B []A.
type elementChain struct {
	buf  [8]types.Type
	seen []types.Type
}

func (c *elementChain) enter(t types.Type) bool {
	if c.seen == nil {
		c.seen = c.buf[:0]
	}
	if slices.Contains(c.seen, t) {
		return false
	}
	c.seen = append(c.seen, t)
	return true
}
