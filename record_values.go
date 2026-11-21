package evm

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"unsafe"

	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/kvo/codec/row2"
	"github.com/andreyvit/evm/values"
)

func recordShapeMatches(k kvo.AnyType, s *values.Shape) bool {
	if s == nil {
		return false
	}
	if typ := RecordScalarType(k); typ != nil {
		if s.Type == typ {
			return true
		}
		nativeType, ok := k.(interface{ NativeType() reflect.Type })
		if !ok {
			return false
		}
		native := nativeType.NativeType()
		switch native.Kind() {
		case reflect.String:
			return s.Kind == values.StringKind
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return s.Kind == values.UintKind && s.Bits == uint8(native.Size()*8)
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return s.Kind == values.IntKind && s.Bits == uint8(native.Size()*8)
		}
		return false
	}
	switch t := k.(type) {
	case *kvo.EntityType:
		return s.Kind == values.PointerKind || s.Kind == values.TypedPairKind
	case *kvo.ScalarDataType:
		return t.NativeType().Kind() == reflect.Slice && s.Kind == values.SliceKind && s.Elem.Type == values.Uint8
	case *kvo.MapType:
		return s.Kind == values.MapKind && recordShapeMatches(t.MapKeyType(), s.Key) && recordShapeMatches(t.ItemType(), s.Elem)
	case *kvo.ListType:
		return s.Kind == values.SliceKind && recordShapeMatches(t.ItemType(), s.Elem)
	}
	return false
}

type recordFieldValue struct {
	program  *Program
	path     []row2.Step
	s        *values.Shape
	k        kvo.AnyType
	value    values.Value
	baseline []byte
	assigned bool
}

// Collections have normal Go reference semantics inside an edit. A second
// selector returns the same map or slice. Put writes its current contents.
func (s *editorState) field(program *Program, shape *values.Shape, path []row2.Step) *recordFieldValue {
	for _, p := range s.fields {
		if slices.Equal(p.path, path) && (p.program == program && p.s == shape || sharedRecordShape(p.s, shape, 0)) {
			return p
		}
	}
	return nil
}

// Interface cells and functions carry program-local identities. Other matching
// collection layouts can share their actual backing, including retained aliases.
func sharedRecordShape(a, b *values.Shape, depth int) bool {
	if a == nil || b == nil {
		return a == b
	}
	if depth > 64 || a.Kind != b.Kind || a.Cells != b.Cells {
		return false
	}
	if a.Kind == values.InterfaceKind || a.Kind == values.FunctionKind {
		return false
	}
	// Row cells retain their model identity regardless of native slot codecs.
	if a.Kind == values.TypedPairKind {
		return a.Type == b.Type
	}
	if a.Native != nil || b.Native != nil {
		if a.Native == nil || b.Native == nil || a.Native.GoType != b.Native.GoType {
			return false
		}
		switch a.Kind {
		case values.MapKind:
			return a.Native.Map != nil && b.Native.Map != nil
		case values.SliceKind:
			return a.Native.Slice != nil && b.Native.Slice != nil
		case values.BoolKind, values.IntKind, values.UintKind, values.FloatKind, values.StringKind:
			return a.Type == b.Type
		default:
			return false
		}
	}
	if a.Type != b.Type {
		return false
	}
	switch a.Kind {
	case values.MapKind:
		return sharedRecordShape(a.Key, b.Key, depth+1) && sharedRecordShape(a.Elem, b.Elem, depth+1)
	case values.SliceKind, values.ArrayKind:
		return a.Len == b.Len && sharedRecordShape(a.Elem, b.Elem, depth+1)
	case values.StructKind, values.PointerKind:
		return false
	default:
		return true
	}
}

type recordConverter struct {
	p *Program
	b *executionBudget
}

func (vm *VM) recordField(f frame, op *ExtendedOp, r *borrowedRecord, mutable bool, b *executionBudget) error {
	k := op.Record.Model.MustPropByCode(op.Index).AnyType()
	cv := recordConverter{vm.program, b}
	var state *editorState
	var path []row2.Step
	var cached *recordFieldValue
	composite := op.Shape.Kind == values.MapKind || op.Shape.Kind == values.SliceKind || op.Shape.Kind == values.InterfaceKind
	var scratch [8]row2.Step
	if mutable {
		scratch[0] = op.step
		var err error
		state, path, err = r.editPath(scratch[:1], b)
		if err != nil {
			return err
		}
		if composite {
			cached = state.field(vm.program, op.Shape, path)
			if cached == nil {
				if err := cv.flush(state, path, 0); err != nil {
					return err
				}
			}
		}
	}
	var v values.Value
	if op.Kind == ExtendedSetKey {
		v = vm.read(f, op.In[1])
		if !composite {
			raw, err := cv.encode(k, op.Shape, v, 0)
			if err != nil {
				return err
			}
			return state.edit.TryFastSetPathValueWithWriter(path, raw, nil)
		}
	} else if cached != nil {
		vm.write(f, op.Out[0], cached.value)
		return nil
	} else if !mutable && op.listAccess != nil {
		v, err := cv.decodeRecordList(op.listAccess.Records(r.read), op.Shape, k.ItemType().(*kvo.EntityType).Model())
		if err != nil {
			return err
		}
		vm.write(f, op.Out[0], v)
	} else {
		var raw values.Value
		if mutable {
			raw = state.edit.GetPathValue(path)
		} else {
			raw = r.read.GetValue(op.Index)
		}
		var err error
		if op.Shape.Kind == values.StringKind && (!mutable || state.edit.OwnsData()) {
			v = raw
		} else {
			v, err = cv.decode(k, op.Shape, raw, mutable, 0)
		}
		if err != nil {
			return err
		}
		vm.write(f, op.Out[0], v)
	}
	if composite && mutable {
		if cached == nil {
			if err := cv.charge(128 + uint64(len(path))*32); err != nil {
				return err
			}
			cached = &recordFieldValue{program: vm.program, path: slices.Clone(path), s: op.Shape, k: k}
			state.fields = append(state.fields, cached)
		}
		if op.Kind == ExtendedSetKey {
			cached.assigned = true
		} else if cached.baseline == nil {
			raw, err := cv.encode(k, op.Shape, v, 0)
			if err != nil {
				return err
			}
			cached.baseline = raw.AsBytes()
		}
		cached.value = v
	}
	return nil
}

func (cv recordConverter) call() *VM             { return &VM{Context: cv.b.ctx, program: cv.p, budget: cv.b} }
func (cv recordConverter) charge(n uint64) error { return charge(n, &cv.b.allocated, cv.b.limits) }

// FlushRecord applies edits made through Go map and slice aliases before the
// external database saves this one row. Repeated saves see subsequent edits.
func (c *VM) FlushRecord(editor *kvo.Editor) error {
	scope := c.budget.recordScope()
	if scope == nil || scope.closed {
		return nil
	}
	return (recordConverter{c.program, c.budget}).flush(scope.editors[editor], nil, 0)
}

// FlushMutableRecord applies alias edits retained by this row handle.
func (c *VM) FlushMutableRecord(v values.Value, model *kvo.Model) error {
	r := mutableRecord(v)
	if r == nil || r.model != model || r.state == nil || r.parent != nil {
		return fmt.Errorf("expected a mutable root record")
	}
	return (recordConverter{c.program, c.budget}).flush(r.state, nil, 0)
}

func (cv recordConverter) flush(state *editorState, prefix []row2.Step, depth int) error {
	if state == nil {
		return nil
	}
	if depth > 128 {
		return errors.New("cyclic or deeply nested KVO record")
	}
	for _, p := range state.fields {
		if len(p.path) < len(prefix) || !slices.Equal(p.path[:len(prefix)], prefix) {
			continue
		}
		raw, err := (recordConverter{p.program, cv.b}).encode(p.k, p.s, p.value, depth+1)
		if err != nil {
			return err
		}
		if p.assigned || !bytes.Equal(raw.AsBytes(), p.baseline) {
			if err := state.edit.TryFastSetPathValueWithWriter(p.path, raw, nil); err != nil {
				return err
			}
			p.baseline, p.assigned = raw.AsBytes(), false
		}
	}
	return nil
}

// Object lists may contain nils and unexposed native/MessagePack fields.
// Read each element directly; row2 list serialization cannot preserve nils.
func (cv recordConverter) decodeRecordList(list kvo.RecordList, s *values.Shape, model *kvo.Model) (values.Value, error) {
	if err := cv.b.contextErr(); err != nil {
		return values.Value{}, err
	}
	if list.Missing() {
		return values.Value{}, nil
	}
	n := uint64(list.Len())
	if n > math.MaxUint32 {
		return values.Value{}, ErrAllocation
	}
	storage := collectionStorageFor(s, false)
	p, err := storage.allocate(n, s.Elem.Cells, &cv.b.allocated, cv.b.limits)
	if err != nil {
		return values.Value{}, err
	}
	cursor := list.Cursor()
	for i := uint64(0); i < n; i++ {
		if i&255 == 0 {
			if err := cv.b.contextErr(); err != nil {
				return values.Value{}, err
			}
		}
		row, ok := cursor.Next()
		if !ok {
			return values.Value{}, fmt.Errorf("KVO object list ended before its declared length")
		}
		var v values.Value
		if !row.Missing() {
			if row.Model() != model {
				return values.Value{}, fmt.Errorf("incorrect object list model")
			}
			v, err = kvo.Type(model).Own(row.Value(), cv.b)
			if err != nil {
				return values.Value{}, err
			}
		}
		if storage.native != nil {
			if err := storeNative(cv.call(), s.Elem, storage.at(p, i, s.Elem.Cells), v, &cv.b.allocated, cv.b.limits); err != nil {
				return values.Value{}, err
			}
		} else {
			s.Elem.Store(values.CellAt(p, i*s.Elem.Cells), v)
		}
	}
	return values.SliceValue(p, uint32(n), uint32(n)), nil
}

func (cv recordConverter) decode(k kvo.AnyType, s *values.Shape, x values.Value, mutable bool, depth int) (values.Value, error) {
	var zero values.Value
	if depth > 128 {
		return zero, errors.New("KVO nesting exceeds 128")
	}
	if err := cv.b.contextErr(); err != nil {
		return zero, err
	}
	switch t := k.(type) {
	case *kvo.WordType:
		return x, nil
	case *kvo.JSONType:
		return cv.decodeJSON(x.AsBytes(), depth)
	case *kvo.ScalarDataType:
		if t == kvo.TTimestamp {
			return x, nil
		}
		data := x.AsBytes()
		if s.Kind == values.StringKind {
			if err := cv.charge(uint64(len(data))); err != nil {
				return zero, err
			}
			return values.String(string(data)), nil
		}
		if data == nil {
			return zero, nil
		}
		storage := collectionStorageFor(s, false)
		p, err := storage.allocate(uint64(len(data)), s.Elem.Cells, &cv.b.allocated, cv.b.limits)
		if err != nil {
			return zero, err
		}
		for i, n := range data {
			storage.scalarStore(storage.at(p, uint64(i), s.Elem.Cells), values.Uint(uint64(n)))
		}
		return values.SliceValue(p, uint32(len(data)), uint32(len(data))), nil
	case *kvo.EntityType:
		view := row2.Trusted(x.AsBytes())
		if view.Missing() {
			return zero, nil
		}
		if mutable {
			if err := cv.charge(recordHandleBytes + uint64(unsafe.Sizeof(editorState{}))); err != nil {
				return zero, err
			}
			return (*RecordScope)(nil).Edit(t.Model(), row2.Edit(view)), nil
		}
		// Materialized collections own their leaves before crossing any scope.
		return kvo.Type(t.Model()).Own(kvo.Encoded(view).WithModel(t.Model()).Value(), cv.b)
	}
	view := row2.Trusted(x.AsBytes())
	if view.Missing() {
		return zero, nil
	}
	n := uint64(view.Len())
	if n > math.MaxUint32 {
		return zero, ErrAllocation
	}
	it := view.Iter()
	if s.Kind == values.SliceKind {
		storage := collectionStorageFor(s, false)
		p, err := storage.allocate(n, s.Elem.Cells, &cv.b.allocated, cv.b.limits)
		if err != nil {
			return zero, err
		}
		for {
			i, _, item, ok := it.Next()
			if !ok {
				break
			}
			v, err := cv.decode(k.ItemType(), s.Elem, item.Decode(kvo.Encoding(k.ItemType())), mutable, depth+1)
			if err != nil {
				return zero, err
			}
			if storage.native != nil {
				if err := storeNative(cv.call(), s.Elem, storage.at(p, i, s.Elem.Cells), v, &cv.b.allocated, cv.b.limits); err != nil {
					return zero, err
				}
			} else {
				s.Elem.Store(values.CellAt(p, i*s.Elem.Cells), v)
			}
		}
		return values.SliceValue(p, uint32(n), uint32(n)), nil
	}
	if err := cv.charge(64 + n*128); err != nil {
		return zero, err
	}
	var m *values.Map
	var native, keyStorage, valueStorage unsafe.Pointer
	nativeMap := s.Native != nil && s.Native.Map != nil
	if nativeMap {
		if err := cv.charge(s.Key.Native.Size + s.Elem.Native.Size); err != nil {
			return zero, err
		}
		native = s.Native.Map.Make()
		keyStorage = s.Key.Native.New()
		valueStorage = s.Elem.Native.New()
	} else {
		m = values.NewMap(s.Key, cv.p.typeShapes)
	}
	for {
		num, text, item, ok := it.Next()
		if !ok {
			break
		}
		key := values.Uint(num)
		if s.Key.Kind == values.StringKind {
			if err := cv.charge(uint64(len(text))); err != nil {
				return zero, err
			}
			key = values.String(string(text))
		}
		v, err := cv.decode(k.ItemType(), s.Elem, item.Decode(kvo.Encoding(k.ItemType())), mutable, depth+1)
		if err != nil {
			return zero, err
		}
		if nativeMap {
			if err := storeNative(cv.call(), s.Key, keyStorage, key, &cv.b.allocated, cv.b.limits); err != nil {
				return zero, err
			}
			if err := storeNative(cv.call(), s.Elem, valueStorage, v, &cv.b.allocated, cv.b.limits); err != nil {
				return zero, err
			}
			s.Native.Map.Update(native, keyStorage, valueStorage)
			continue
		}
		h, err := s.Key.HashContext(cv.b.ctx, key, cv.p.typeShapes)
		if err != nil {
			return zero, err
		}
		if err = m.PutContext(cv.b.ctx, key, v, h); err != nil {
			return zero, err
		}
	}
	if nativeMap {
		return values.Value{P: native}, nil
	}
	return values.Value{P: unsafe.Pointer(m)}, nil
}

func (cv recordConverter) encode(k kvo.AnyType, s *values.Shape, v values.Value, depth int) (values.Value, error) {
	var zero values.Value
	if depth > 128 {
		return zero, errors.New("cyclic or deeply nested KVO field")
	}
	if err := cv.b.contextErr(); err != nil {
		return zero, err
	}
	switch t := k.(type) {
	case *kvo.WordType:
		return v, nil
	case *kvo.JSONType:
		limit := min(cv.b.limits.AllocationBytes-cv.b.allocated.bytes, math.MaxInt)
		raw, err := cv.p.EncodeJSON(cv.b.ctx, v, int(limit))
		if err != nil {
			return zero, err
		}
		if err := cv.charge(uint64(len(raw))); err != nil {
			return zero, err
		}
		return values.Bytes(raw), nil
	case *kvo.ScalarDataType:
		if t == kvo.TTimestamp {
			tm := v.AsTime()
			if v.P != nil && v.R != 12 {
				tm = tm.UTC()
				if err := cv.charge(values.NativeTimeAllocBytes(tm)); err != nil {
					return zero, err
				}
				v = values.Time(tm)
			}
			return v, nil
		}
		if s.Kind == values.StringKind {
			return v, nil
		}
		if v.P == nil {
			return zero, nil
		}
		if err := cv.charge(uint64(v.SliceLen())); err != nil {
			return zero, err
		}
		data := make([]byte, v.SliceLen())
		reader := readScalarSlice(s, v)
		for i := range data {
			data[i] = byte(reader.at(uint64(i)).R)
		}
		return values.Bytes(data), nil
	case *kvo.EntityType:
		if v.P == nil {
			return zero, nil
		}
		if s.Kind == values.TypedPairKind && mutableRepresentation(v) == nil {
			r, err := readRecordCell(v, t.Model())
			if err != nil {
				return zero, err
			}
			return values.Bytes(r.Snapshot().Raw()), nil
		}
		if native, ok := nativeMutableRecord(recordCell(v)); ok {
			return values.Bytes(native.Snapshot().Raw()), nil
		}
		row := mutableRecord(v)
		if row == nil || row.model != t.Model() {
			return zero, fmt.Errorf("invalid %s record", t.Name())
		}
		if row.state == nil && row.parent == nil {
			return values.Bytes(row.read.Snapshot().Raw()), nil
		}
		var scratch [8]row2.Step
		state, path, err := row.editPath(scratch[:0], cv.b)
		if err != nil {
			return zero, err
		}
		if err = cv.flush(state, path, depth+1); err != nil {
			return zero, err
		}
		if len(path) == 0 {
			return values.Bytes(state.edit.Snapshot().Raw()), nil
		}
		return state.edit.GetPathValue(path), nil
	}
	if v.P == nil || s.Kind == values.SliceKind && v.SliceReadData() == nil {
		return zero, nil
	}
	var w row2.Writer
	if s.Kind == values.SliceKind {
		w.Begin(0, "", row2.List, row2.Compact2)
		for i := range v.SliceLen() {
			child, err := cv.call().collectionElement(s, v, uint64(i))
			if err != nil {
				return zero, err
			}
			x, err := cv.encode(k.ItemType(), s.Elem, child, depth+1)
			if err != nil {
				return zero, err
			}
			if err := cv.charge(16 + row2.PayloadSize(x)); err != nil {
				return zero, err
			}
			w.PutPresent(0, "", x, kvo.Encoding(k.ItemType()))
		}
	} else {
		items, err := cv.call().mapEntries(s, v)
		if err != nil {
			return zero, err
		}
		if s.Key.Kind == values.StringKind {
			w.Begin(0, "", row2.Dictionary, row2.Compact2)
			slices.SortFunc(items, func(a, b *values.MapEntry) int { return strings.Compare(a.Key.AsString(), b.Key.AsString()) })
		} else {
			w.Begin(0, "", row2.Object, row2.Compact2)
			slices.SortFunc(items, func(a, b *values.MapEntry) int { return cmp.Compare(a.Key.R, b.Key.R) })
		}
		for _, a := range items {
			x, err := cv.encode(k.ItemType(), s.Elem, a.Value, depth+1)
			if err != nil {
				return zero, err
			}
			n, text := a.Key.R, ""
			if s.Key.Kind == values.StringKind {
				n, text = 0, a.Key.AsString()
			}
			if err := cv.charge(16 + uint64(len(text)) + row2.PayloadSize(x)); err != nil {
				return zero, err
			}
			w.PutPresent(n, text, x, kvo.Encoding(k.ItemType()))
		}
	}
	w.End()
	return values.Bytes(w.BytesWritten()), nil
}
