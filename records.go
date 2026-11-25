// Immutable KVO records travel as cells and borrow storage until the compiler
// inserts an ownership copy. Mutable handles retain their editor and materialized field values.
package evm

import (
	"context"
	"fmt"
	"go/token"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"
	"unsafe"

	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/kvo/codec/row2"
	"github.com/andreyvit/evm/values"
)

// VMFieldBase separates VM layout fields from persistent KVO property codes.
const VMFieldBase = kvo.RuntimePropBase

type RecordType struct {
	// NativeAllocation selects the native struct storage for guest-created
	// collections of this row. Nil keeps generic record storage.
	NativeAllocation reflect.Type

	// SchemaView exposes the properties supported by the current KVO codec.
	SchemaView bool
	Name       string
	Model      *kvo.Model
	Mutable    bool
	Value      *values.Type // guest Model value, distinct from its read handle
	Pointer    *values.Type // guest *Model: Pair for immutable cells and editable rows
}

// ValueModel exposes the sub-objects of a KVO model as values of a struct type
// that the same package declares. A property of that model then reads as the
// struct, by value, on any read-only row, encoded or backed by a Go struct:
// the VM packs the two stored words into the struct's cell. Nothing is decoded
// into a Go struct and nothing is allocated, so the struct's methods, compiled
// from package source, run on stored data directly. A missing sub-object reads
// as the zero value.
//
// The struct must keep its value copies in one cell (values.CellStruct), and
// each of its two fields must be a scalar property that fits one word.
// Editable rows do not expose such a property yet.
type ValueModel struct {
	Model *kvo.Model
	Type  *values.Type
	// Props are the property codes of the struct's two fields, in field order.
	Props [2]uint64
}

// valueModels indexes the declarations by struct type and checks each against
// the struct's shape, so that a read can trust the cell it builds.
func valueModels(packages []Package) (map[*values.Type]*ValueModel, error) {
	var result map[*values.Type]*ValueModel
	for i := range packages {
		pkg := &packages[i]
		for j := range pkg.ValueModels {
			vm := &pkg.ValueModels[j]
			if vm.Model == nil || vm.Type == nil {
				return nil, fmt.Errorf("%s: incomplete value model", pkg.Path)
			}
			var shape *values.Shape
			for _, d := range pkg.Types {
				if d.Shape != nil && d.Shape.Type == vm.Type {
					shape = d.Shape
				}
			}
			if shape == nil || shape.CellStruct == nil || len(shape.Fields) != 2 {
				return nil, fmt.Errorf("%s: value model %s needs a two-field struct with cell storage declared by the same package", pkg.Path, vm.Type.Name())
			}
			for k, code := range vm.Props {
				prop := vm.Model.PropByCode(code)
				if code >= VMFieldBase || prop == nil || RecordWordType(prop) == nil || !recordShapeMatches(prop.AnyType(), shape.Fields[k].Shape) {
					return nil, fmt.Errorf("%s: field %d of value model %s does not match a one-word property of %s", pkg.Path, k, vm.Type.Name(), vm.Model.Name())
				}
			}
			if result == nil {
				result = make(map[*values.Type]*ValueModel)
			}
			if result[vm.Type] != nil {
				return nil, fmt.Errorf("%s: duplicate value model %s", pkg.Path, vm.Type.Name())
			}
			for _, other := range result {
				if other.Model == vm.Model {
					return nil, fmt.Errorf("%s: model %s already has a value type", pkg.Path, vm.Model.Name())
				}
			}
			result[vm.Type] = vm
		}
	}
	return result, nil
}

// WithoutRecords returns p without the row types of the given models and
// without their methods. A host uses it for value models: scripts read those
// sub-objects as struct values and never as rows.
func WithoutRecords(p Package, models ...*kvo.Model) Package {
	dropped := make(map[*values.Type]bool)
	records := make([]*RecordType, 0, len(p.Records))
	for _, r := range p.Records {
		if r != nil && slices.Contains(models, r.Model) {
			dropped[r.Pointer], dropped[r.Value] = true, true
			continue
		}
		records = append(records, r)
	}
	p.Records = records
	p.Methods = slices.DeleteFunc(slices.Clone(p.Methods), func(m ExternalMethod) bool { return dropped[m.Receiver] })
	return p
}

// ValueModels validates the value model declarations of packages and returns
// them by model. Compilers use it to type properties.
func ValueModels(packages []Package) (map[*kvo.Model]*ValueModel, error) {
	byType, err := valueModels(packages)
	if err != nil || len(byType) == 0 {
		return nil, err
	}
	result := make(map[*kvo.Model]*ValueModel, len(byType))
	for _, vm := range byType {
		result[vm.Model] = vm
	}
	return result, nil
}

// Only direct pointer-result imports need this index. A schema can expose
// many more models than a script uses.
func recordResults(packages []Package) map[*values.Type]*RecordType {
	records := make(map[*values.Type]*RecordType)
	for _, pkg := range packages {
		for _, r := range pkg.Records {
			if r != nil {
				records[r.Pointer] = r
			}
		}
	}
	return records
}

func NewRecordType(name string, model *kvo.Model, mutable bool) *RecordType {
	typ := kvo.Type(model)
	if mutable {
		typ = mutableModelFor(model).typ
	}
	return &RecordType{Name: name, Model: model, Mutable: mutable, Pointer: typ, Value: values.NewType(name, values.Pointer)}
}

// ModelPackage declares read and edit handles for every entity in a completed
// KVO schema. Register once and share its type identities across compilations.
// Database operations are supplied separately by the host.
func ModelPackage(path string, schemas ...*kvo.Schema) (Package, error) {
	pkg := Package{Path: path}
	anyShape := &values.Shape{Type: values.AnyType, Kind: values.InterfaceKind, Cells: 1}
	stringShape := &values.Shape{Type: values.StringType, Kind: values.StringKind, Cells: 1}
	object := &values.Shape{Type: JSONObjectType, Kind: values.MapKind, Cells: 1, Key: stringShape, Elem: anyShape}
	pkg.Types = append(pkg.Types, TypeDecl{Shape: object})
	names := map[string]string{}
	var models []*kvo.Model
	for _, schema := range schemas {
		models = append(models, schema.Models()...)
	}
	for _, model := range models {
		var b strings.Builder
		upper := true
		for _, r := range model.Name() {
			if r == '_' {
				upper = true
				continue
			}
			if upper {
				r = unicode.ToUpper(r)
				upper = false
			}
			b.WriteRune(r)
		}
		base := b.String()
		if override := model.RowName(); override != "" {
			base = override
		}
		if !token.IsIdentifier(base) || !token.IsExported(base) {
			return Package{}, fmt.Errorf("invalid guest model name %q from %q", base, model.Name())
		}
		for _, mutable := range []bool{false, true} {
			name := base
			if mutable {
				name += "Edit"
			}
			if prev, exists := names[name]; exists {
				return Package{}, fmt.Errorf("KVO models %q and %q both declare %s", prev, model.Name(), name)
			}
			names[name] = model.Name()
			r := NewRecordType(name, model, mutable)
			r.SchemaView = true
			pkg.Records = append(pkg.Records, r)
			pkg.Methods = append(pkg.Methods, ExternalMethod{Name: "ToJSON", Receiver: r.Pointer, Signature: Signature{Results: []*values.Type{object.Type}}, Call: func(c *VM) error { return c.recordJSON(r) }})
		}
	}
	return pkg, nil
}

// Record returns the package's handle for the given model and access mode.
func (p *Package) Record(model *kvo.Model, mutable bool) *RecordType {
	for _, r := range p.Records {
		if r.Model == model && r.Mutable == mutable {
			return r
		}
	}
	return nil
}

// RecordWordType maps a scalar KVO property to its script type.
func RecordWordType(p kvo.PropImpl) *values.Type {
	t, ok := p.AnyType().(*kvo.WordType)
	if !ok {
		return nil
	}
	return RecordScalarType(t)
}

// RecordScalarType supplies the VM type for KVO leaves.
func RecordScalarType(t kvo.AnyType) *values.Type {
	if t == kvo.TTimestamp {
		return values.TimeType
	}
	if _, ok := t.(*kvo.JSONType); ok {
		return values.AnyType
	}
	if d, ok := t.(*kvo.ScalarDataType); ok && d.NativeType().Kind() == reflect.String {
		return values.StringType
	}
	w, ok := t.(*kvo.WordType)
	if !ok {
		return nil
	}
	switch w.NativeType().Kind() {
	case reflect.Bool:
		return values.Boolean
	case reflect.Int:
		return values.IntType
	case reflect.Int64:
		return values.Int64
	case reflect.Int32:
		return values.Int32
	case reflect.Int16:
		return values.Int16
	case reflect.Int8:
		return values.Int8
	case reflect.Uint:
		return values.UintType
	case reflect.Uint64:
		return values.Uint64
	case reflect.Uint32:
		return values.Uint32
	case reflect.Uint16:
		return values.Uint16
	case reflect.Uint8:
		return values.Uint8
	case reflect.Float32:
		return values.Float32Type
	case reflect.Float64:
		return values.Float64
	case reflect.Struct:
		if w.NativeType() == reflect.TypeFor[time.Time]() {
			return values.Uint64
		}
	}
	return nil
}

type recordScopeKey struct{}
type RecordScope struct {
	editors map[*kvo.Editor]*editorState
	closed  bool
	local   [8]localRecordSlot
	reads   map[recordSite]*borrowedRecord
}
type editorState struct {
	edit    *kvo.Editor
	backing any
	fields  []*recordFieldValue
}
type localRecordSlot struct {
	site recordSite
	row  *borrowedRecord
}
type borrowedRecord struct {
	read   kvo.Record
	state  *editorState
	model  *kvo.Model
	parent *borrowedRecord
	step   row2.Step
}

type recordContext struct {
	context.Context
	scope *RecordScope
}

func (c *recordContext) Value(key any) any {
	if _, ok := key.(recordScopeKey); ok {
		return c.scope
	}
	return c.Context.Value(key)
}

// WithRecords shares editor caches and local child slots during a call.
// Closing it releases those caches without invalidating held row handles.
func WithRecords(ctx context.Context) (context.Context, *RecordScope) {
	if ctx == nil {
		panic("nil parent context")
	}
	s := new(RecordScope)
	return &recordContext{Context: ctx, scope: s}, s
}

func scopeFromContext(ctx context.Context) *RecordScope {
	if c, ok := ctx.(*recordContext); ok {
		return c.scope
	}
	// WithCancel or another context value can wrap the record scope.
	scope, _ := ctx.Value(recordScopeKey{}).(*RecordScope)
	return scope
}

func (s *RecordScope) Close() { *s = RecordScope{closed: true} }

func newMutableRecord() *borrowedRecord {
	return new(borrowedRecord)
}
func (s *RecordScope) Read(model *kvo.Model, m row2.View) values.Value {
	return s.ReadRecord(model, kvo.Encoded(m))
}
func (s *RecordScope) ReadRecord(model *kvo.Model, m kvo.Record) values.Value {
	if m.NativeModel() != nil && m.NativeModel() != model {
		panic("incorrect native record model")
	}
	if m.Missing() {
		return values.Value{}
	}
	return m.WithModel(model).Value()
}

// A call site in a recursive frame needs its own live view.
type recordSite struct {
	vm           *VM
	function, pc int
	depth        int
}

func (vm *VM) recordSite(f frame) recordSite {
	return recordSite{vm, f.function, f.pc - 1, len(vm.stack)}
}

const recordHandleBytes = uint64(unsafe.Sizeof(borrowedRecord{}))

func (s *RecordScope) resultSlot(site recordSite, b *executionBudget) (values.Value, *borrowedRecord, error) {
	if s == nil || s.closed {
		if err := charge(recordHandleBytes, &b.allocated, b.limits); err != nil {
			return values.Value{}, nil, err
		}
		r := newMutableRecord()
		return values.Value{P: unsafe.Pointer(r)}, r, nil
	}
	// Bytecode layout can put several hot sites in the same initial slot.
	// Probe the small table before allocating an overflow map.
	var free *localRecordSlot
	if site.vm != nil {
		for i := range s.local {
			slot := &s.local[(site.pc+i)&(len(s.local)-1)]
			if slot.site == site {
				return values.Value{P: unsafe.Pointer(slot.row)}, slot.row, nil
			}
			if slot.row == nil {
				free = slot
				break
			}
		}
		if free == nil {
			if r := s.reads[site]; r != nil {
				return values.Value{P: unsafe.Pointer(r)}, r, nil
			}
		}
	}
	if err := charge(recordHandleBytes, &b.allocated, b.limits); err != nil {
		return values.Value{}, nil, err
	}
	r := newMutableRecord()
	if free != nil {
		// Put the new site first. In a scan, the entry call runs once and
		// the back-edge call can then hit its home slot on every row.
		home := &s.local[site.pc&(len(s.local)-1)]
		*free = *home
		home.site, home.row = site, r
	} else if site.vm != nil {
		if s.reads == nil {
			s.reads = make(map[recordSite]*borrowedRecord)
		}
		s.reads[site] = r
	}
	return values.Value{P: unsafe.Pointer(r)}, r, nil
}

// SetReadRecordResult returns one immutable KVO row from a host function.
// The compiler owns the payload when it escapes its borrowing boundary.
func (c *VM) SetReadRecordResult(model *kvo.Model, m row2.View) error {
	return c.SetRecordResult(model, kvo.Encoded(m))
}
func (c *VM) SetRecordResult(model *kvo.Model, m kvo.Record) error {
	_, err := c.recordResult(model, m, true)
	return err
}

// RecordValue returns the immutable row cell without allocating a handle.
func (c *VM) RecordValue(model *kvo.Model, m kvo.Record) (values.Value, error) {
	return c.recordResult(model, m, false)
}

func (c *VM) recordResult(model *kvo.Model, m kvo.Record, window bool) (values.Value, error) {
	if m.NativeModel() != nil && m.NativeModel() != model {
		return values.Value{}, fmt.Errorf("incorrect native record model")
	}
	v := values.Value{}
	if !m.Missing() {
		v = m.WithModel(model).Value()
	}
	if window {
		c.SetCallResult(0, v)
	}
	return v, nil
}

// SetEditRecordResult returns a mutable KVO row with the editor's native lifetime.
func (c *VM) SetEditRecordResult(model *kvo.Model, e *kvo.Editor) error {
	return c.SetBackedEditRecordResult(model, e, nil)
}

// SetBackedEditRecordResult keeps the native backing with the virtual row.
func (c *VM) SetBackedEditRecordResult(model *kvo.Model, e *kvo.Editor, backing any) error {
	scope := c.budget.recordScope()
	if e != nil {
		n := recordHandleBytes
		if scope == nil || scope.closed || scope.editors[e] == nil {
			n += uint64(unsafe.Sizeof(editorState{}))
		}
		if err := c.ChargeAllocation(n); err != nil {
			return err
		}
	}
	c.SetCallResult(0, scope.editRecord(model, e, backing))
	return nil
}

func (s *RecordScope) Edit(model *kvo.Model, m *row2.Editor) values.Value {
	if m == nil {
		return values.Value{}
	}
	return s.EditRecord(model, kvo.EditEncoded(m))
}
func (s *RecordScope) EditRecord(model *kvo.Model, m *kvo.Editor) values.Value {
	return s.editRecord(model, m, nil)
}
func (s *RecordScope) editRecord(model *kvo.Model, m *kvo.Editor, backing any) values.Value {
	if m != nil && m.NativeModel() != nil && m.NativeModel() != model {
		panic("incorrect native record model")
	}
	if m == nil {
		return values.Value{}
	}
	var state *editorState
	if s != nil && !s.closed {
		state = s.editors[m]
	}
	if state == nil {
		state = &editorState{edit: m.WithModel(model), backing: backing}
		if s != nil && !s.closed {
			if s.editors == nil {
				s.editors = make(map[*kvo.Editor]*editorState)
			}
			s.editors[m] = state
		}
	} else if backing != nil {
		state.backing = backing
	}
	r := newMutableRecord()
	r.state, r.model = state, model
	return editorValue(r)
}
func mutableRecord(v values.Value) *borrowedRecord {
	v = recordCell(v)
	if v.P == nil {
		return nil
	}
	m := mutableRepresentation(v)
	if m == nil || v.Representation() != m.editorID() {
		return nil
	}
	r := (*borrowedRecord)(v.P)
	if r.model != m.model {
		return nil
	}
	return r
}

func (s *RecordScope) Mutable(v values.Value, model *kvo.Model) (*row2.Editor, error) {
	e, err := s.MutableRecord(v, model)
	if err != nil {
		return nil, err
	}
	if e.Packed() == nil {
		return nil, fmt.Errorf("expected an encoded record")
	}
	return e.Packed(), nil
}
func (s *RecordScope) MutableRecord(v values.Value, model *kvo.Model) (*kvo.Editor, error) {
	r := mutableRecord(v)
	if r == nil || r.model != model || r.state == nil || r.parent != nil {
		return nil, fmt.Errorf("expected a mutable root record")
	}
	return r.state.edit, nil
}

// MutableRecord resolves the native editor and backing without a session lookup.
func (c *VM) MutableRecord(v values.Value, model *kvo.Model) (*kvo.Editor, any, error) {
	r := mutableRecord(v)
	if r == nil || r.model != model || r.state == nil || r.parent != nil {
		return nil, nil, fmt.Errorf("expected a mutable root record")
	}
	return r.state.edit, r.state.backing, nil
}

// Resolve child views on use so a read of a missing child does not create it.
// Iterative resolution keeps guest-controlled nesting off the Go call stack.
func (r *borrowedRecord) editPath(path []row2.Step, b *executionBudget) (*editorState, []row2.Step, error) {
	for r.parent != nil {
		if len(path)&255 == 255 {
			if err := b.contextErr(); err != nil {
				return nil, nil, err
			}
		}
		if len(path) == cap(path) {
			if err := charge(uint64(2*cap(path))*uint64(unsafe.Sizeof(row2.Step{})), &b.allocated, b.limits); err != nil {
				return nil, nil, err
			}
		}
		path = append(path, r.step)
		r = r.parent
	}
	slices.Reverse(path)
	return r.state, path, nil
}

func (b *executionBudget) recordScope() *RecordScope {
	if b.records == nil {
		b.records = scopeFromContext(b.ctx)
	}
	return b.records
}

// One workspace per linked indexing site, reused across rows. A new list
// expression invalidates cached iteration; neither records nor rows get slots.
// Sequential range reads are O(n), not a fresh scan from element zero each time.
type recordListIterator struct {
	owner      values.Value
	generation uint64
	list       kvo.ListValue
	iterator   kvo.ListValueIterator
	next       int
	length     int
}

func (vm *VM) nextRecordListGeneration() {
	vm.recordListGeneration++
	if vm.recordListGeneration == 0 {
		clear(vm.recordLists)
	}
}

func (vm *VM) recordOp(f frame, op, second *ExtendedOp, b *executionBudget) error {
	if !op.Record.Mutable {
		if op.Flags&RecordListDirect == 0 {
			switch op.Kind {
			case ExtendedRecordListLen:
				list, ok := op.listAccess.TryCell(vm.read(f, op.In[0]))
				if !ok {
					return fmt.Errorf("invalid KVO list cell")
				}
				vm.write(f, op.Out[0], values.Int(int64(list.Len())))
				return nil
			case ExtendedRecordListIndex:
				if vm.recordLists == nil {
					n := vm.program.recordListSlots
					if err := charge(uint64(n)*uint64(unsafe.Sizeof(recordListIterator{})), &b.allocated, b.limits); err != nil {
						return err
					}
					vm.recordLists = make([]recordListIterator, n)
				}
				value := vm.read(f, op.In[0])
				cache := &vm.recordLists[op.listSlot]
				if cache.owner != value || cache.generation != vm.recordListGeneration {
					list, ok := op.listAccess.TryCell(value)
					if !ok {
						return fmt.Errorf("invalid KVO list cell")
					}
					cache.owner, cache.generation = value, vm.recordListGeneration
					cache.list = list.View()
					cache.iterator = cache.list.Iter()
					cache.length, cache.next = cache.list.Len(), 0
				}
				i := vm.read(f, op.In[1]).AsInt()
				if i < 0 || uint64(i) >= uint64(cache.length) {
					return ErrBounds
				}
				if int(i) < cache.next {
					cache.iterator = cache.list.Iter()
					cache.next = 0
				}
				for {
					v, _ := cache.iterator.Next()
					cache.next++
					if cache.next == int(i)+1 {
						vm.write(f, op.Out[0], v)
						return nil
					}
				}
			}
		}
		input := vm.read(f, op.In[0])
		if op.Flags&RecordValueReceiver != 0 && op.Source.Native != nil {
			layout := op.Source.Native
			p := layout.Compare.Zero
			if input.P != nil {
				if snapshot := (*values.Value)(values.CellAt(input.P, layout.OpaqueCell-1)).P; snapshot != nil {
					p = snapshot
				}
			}
			input = layout.Address.Encode(p)
		}
		r, err := readRecordCell(input, op.Record.Model)
		if err != nil {
			return err
		}
		if r.Missing() {
			if op.Flags&RecordValueReceiver == 0 && (input.R != 0 || input.P == nil) {
				return fmt.Errorf("missing %s record", op.Record.Name)
			}
			// An addressable zero row has a cell, even though its record is empty.
			switch op.Kind {
			case ExtendedRecordListIndex:
				return ErrBounds
			case ExtendedGetChildKey:
				return fmt.Errorf("missing %s record", recordDisplayName(op.Source))
			default:
				vm.write(f, op.Out[0], values.Value{})
				return nil
			}
		}
		switch op.Kind {
		case ExtendedRecordListLen:
			vm.write(f, op.Out[0], values.Int(int64(op.listAccess.Get(r).Len())))
			return nil
		case ExtendedRecordListIndex:
			list := op.listAccess.Get(r)
			i := vm.read(f, op.In[1]).AsInt()
			if i < 0 || uint64(i) >= uint64(list.Len()) {
				return ErrBounds
			}
			vm.write(f, op.Out[0], list.At(int(i)))
			return nil
		case ExtendedRecordList:
			vm.nextRecordListGeneration()
			vm.write(f, op.Out[0], op.listAccess.Get(r).Value())
			return nil
		}
		if op.Kind == ExtendedGetChildKey {
			child := r.Child(op.step)
			if child.Missing() {
				return fmt.Errorf("missing %s record", recordDisplayName(op.Source))
			}
			vm.r[f.rb+int(op.Out[0].Slot)] = child.GetValue(op.ChildKey).R
			if second != nil {
				vm.r[f.rb+int(second.Out[0].Slot)] = child.GetValue(second.ChildKey).R
			}
			return nil
		}
		if model := op.valueModel; model != nil {
			v := values.Value{}
			if child := r.Child(op.step); !child.Missing() {
				v = op.Shape.CellStruct.Pack([2]values.Value{child.GetValue(model.Props[0]), child.GetValue(model.Props[1])})
			}
			vm.write(f, op.Out[0], v)
			return nil
		}
		if op.childModel != nil {
			child := r.Child(op.step)
			v := values.Value{}
			if !child.Missing() {
				v = child.Value()
			}
			vm.write(f, op.Out[0], v)
			return nil
		}
		if op.Shape.Type.Repr() != values.Raw {
			return vm.recordField(f, op, &borrowedRecord{read: r}, false, b)
		}
		vm.r[f.rb+int(op.Out[0].Slot)] = r.GetValue(op.Index).R
		return nil
	}
	input := recordCell(vm.read(f, op.In[0]))
	if native, ok := nativeMutableRecord(input); ok {
		return vm.nativeMutableField(f, op, native, b)
	}
	scope := b.recordScope()
	r := mutableRecord(input)
	if r == nil || r.model != op.Record.Model {
		return fmt.Errorf("invalid %s record", op.Record.Name)
	}
	writable := r.parent != nil || r.state != nil
	if op.Record.Mutable != writable {
		return fmt.Errorf("wrong access mode for %s", op.Record.Name)
	}
	if op.childModel != nil {
		var site recordSite
		if vm.program.functions[f.function].localRead(f.pc - 1) {
			site = vm.recordSite(f)
		}
		_, child, err := scope.resultSlot(site, b)
		if err != nil {
			return err
		}
		child.model = op.childModel
		child.parent, child.step = r, op.step
		child.state = nil
		vm.write(f, op.Out[0], editorValue(child))
		return nil
	}
	if op.Shape.Type.Repr() != values.Raw {
		return vm.recordField(f, op, r, writable, b)
	}
	var scratch [8]row2.Step
	scratch[0] = op.step
	state, path, err := r.editPath(scratch[:1], b)
	if err != nil {
		return err
	}
	if op.Kind == ExtendedSetKey {
		return state.edit.TryFastSetPathValueWithWriter(path, values.Uint(vm.r[f.rb+int(op.In[1].Slot)]), nil)
	} else {
		vm.r[f.rb+int(op.Out[0].Slot)] = state.edit.GetPathValue(path).R
	}
	return nil
}

// An explicit Go struct copy stores its immutable record cell in VM memory.
// Ordinary read handles already carry the cell and take the fast path.
func recordCell(v values.Value) values.Value {
	if v.R == 0 && v.P != nil {
		return *(*values.Value)(v.P)
	}
	return v
}

// Record reads a guest immutable row, including an explicit value copy.
// The returned record retains its existing native representation and lifetime.
// Both a nil row pointer and a zero row value have missing record contents.
// Callers needing pointer identity must also inspect the original argument.
func (c *VM) Record(v values.Value, model *kvo.Model) (kvo.Record, error) {
	return readRecordCell(v, model)
}

func readRecordCell(v values.Value, model *kvo.Model) (kvo.Record, error) {
	return RecordValue(v, model)
}

// RecordValue reads an immutable row or its explicit value copy.
func RecordValue(v values.Value, model *kvo.Model) (kvo.Record, error) {
	v = recordCell(v)
	if v.P == nil {
		return kvo.Record{}, nil
	}
	r, ok := kvo.TryFromValue(v)
	if !ok || r.Model() != model {
		return kvo.Record{}, fmt.Errorf("incorrect record model")
	}
	return r, nil
}

func recordDisplayName(s *values.Shape) string {
	name := strings.TrimPrefix(s.DisplayName, "*")
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	return name
}
