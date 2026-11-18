package evm

import (
	"fmt"
	"reflect"
	"sync"
	"unsafe"

	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/values"
)

// Editable row identities, like immutable KVO identities, belong to the model.
// Representation metadata is shared; no registry contains individual row pointers.
type mutableModel struct {
	model  *kvo.Model
	typ    *values.Type
	once   sync.Once
	editor values.RepresentationID
	mu     sync.Mutex
	native map[reflect.Type]values.RepresentationID
}

var mutableModels sync.Map          // *kvo.Model -> *mutableModel
var mutableRepresentations sync.Map // RepresentationID -> *mutableModel

func mutableModelFor(model *kvo.Model) *mutableModel {
	if m, ok := mutableModels.Load(model); ok {
		return m.(*mutableModel)
	}
	m := &mutableModel{model: model, typ: values.NewType("*"+model.Name()+"Edit", values.Pair)}
	actual, _ := mutableModels.LoadOrStore(model, m)
	return actual.(*mutableModel)
}
func (m *mutableModel) editorID() values.RepresentationID {
	m.once.Do(func() {
		m.editor = values.RegisterRepresentation(&values.Shape{Type: m.typ, Kind: values.TypedPairKind, Cells: 1, PointerLike: true})
		mutableRepresentations.Store(m.editor, m)
	})
	return m.editor
}
func editorValue(r *borrowedRecord) values.Value {
	return values.TypedPair(mutableModelFor(r.model).editorID(), unsafe.Pointer(r), 0)
}
func mutableRepresentation(v values.Value) *mutableModel {
	m, _ := mutableRepresentations.Load(v.Representation())
	if m == nil {
		return nil
	}
	return m.(*mutableModel)
}

// NativeMutableRecordValue exposes the live native pointer as an editable row.
// Encoded records require explicit conversion before entering this boundary.
func NativeMutableRecordValue(model *kvo.Model, r kvo.Record) (values.Value, error) {
	if r.Missing() {
		return values.Value{}, nil
	}
	ptr := r.NativePointer()
	if ptr == nil || r.Model() != model {
		return values.Value{}, fmt.Errorf("editable native row requires native model backing")
	}
	t := reflect.TypeOf(ptr)
	r = kvo.Bind(t.Elem(), model).Record(reflect.ValueOf(ptr))
	m := mutableModelFor(model)
	m.mu.Lock()
	id := m.native[t]
	if id == 0 {
		id = values.RegisterRepresentation(&values.Shape{Type: m.typ, Kind: values.TypedPairKind, Cells: 1, PointerLike: true, NativePointerType: t})
		if m.native == nil {
			m.native = make(map[reflect.Type]values.RepresentationID)
		}
		m.native[t] = id
		mutableRepresentations.Store(id, m)
	}
	m.mu.Unlock()
	return values.TypedPair(id, r.Value().P, uint32(r.Value().Representation())), nil
}
func nativeMutableRecord(v values.Value) (kvo.Record, bool) {
	v = recordCell(v)
	m := mutableRepresentation(v)
	if m == nil || v.Representation().Shape().NativePointerType == nil {
		return kvo.Record{}, false
	}
	r, ok := kvo.TryFromValue(values.TypedPair(values.RepresentationID(uint32(v.R)), v.P, 0))
	return r, ok && r.Model() == m.model
}

// NativeMutableRecord accepts only native-backed editable rows. An editor handle
// may have pending changes and must not be silently unwrapped into a Go pointer.
func NativeMutableRecord(v values.Value, model *kvo.Model) (kvo.Record, error) {
	if v.P == nil {
		return kvo.Record{}, nil
	}
	r, ok := nativeMutableRecord(v)
	if !ok || r.Model() != model {
		return kvo.Record{}, fmt.Errorf("editable row requires native backing for %s", model.Name())
	}
	return r, nil
}
