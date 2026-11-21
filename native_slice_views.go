package evm

import (
	"github.com/andreyvit/evm/nativebind"
	"github.com/andreyvit/evm/values"
	"maps"
	"reflect"
	"slices"
	"unsafe"
)

var borrowedBytesLayout = func() *values.NativeLayout {
	layout := nativebind.Layout(reflect.TypeFor[[]byte](), nil, false)
	layout.Slice.Element = nativebind.Layout(reflect.TypeFor[byte](), nil, false)
	return layout
}()

// BorrowBytes returns a transaction-scoped []byte. Declare the result in
// OwnershipEffects.Borrowed. Writes and escapes detach all its aliases.
func (c *Call) BorrowBytes(data []byte) (values.Value, error) {
	return values.BorrowByteSlice(data, borrowedBytesLayout, c.budget)
}

func (c *Call) resliceView(v values.Value, lo, length, capacity uint32) (values.Value, error) {
	if lo == 0 && length == v.SliceLen() && capacity == v.SliceCap() {
		return v, nil
	}
	return v.SliceView(lo, length, capacity, c.budget)
}

type nativeViewKey struct {
	typ    *values.Type
	layout reflect.Type
}

// NativeSlice retains native backing without changing the logical slice shape.
func (c *Call) NativeSlice(layout, element *values.NativeLayout, data unsafe.Pointer, length, capacity uint32) (values.Value, error) {
	if binding, ok := c.program.nativeSliceLayouts[layout]; ok {
		layout, element = binding.Layout, binding.Element
	}
	if err := c.ChargeAllocation(uint64(unsafe.Sizeof(values.NativeSliceView{}))); err != nil {
		return values.Value{}, err
	}
	return values.NativeSliceValue(&values.NativeSliceView{Data: data, Layout: layout, Element: element, Length: length, Capacity: capacity}), nil
}

func nativeViewShape(s *values.Shape, layout *values.NativeLayout) *values.Shape {
	copy := *s
	copy.Native = layout
	if s.Kind == values.ArrayKind {
		copy.Elem = nativeViewShape(s.Elem, layout.Array.Element)
	}
	return &copy
}

type nativeViewTable struct {
	tags   map[nativeViewKey]uint64
	shapes []*values.Shape
	arrays map[nativeArrayViewKey]nativeArrayViewLayouts
}

var emptyNativeViewTable nativeViewTable

func (p *Program) viewTable() *nativeViewTable {
	if table := p.nativeViews.Load(); table != nil {
		return table
	}
	return &emptyNativeViewTable
}
func (table *nativeViewTable) clone() *nativeViewTable {
	tags := maps.Clone(table.tags)
	if tags == nil {
		tags = make(map[nativeViewKey]uint64)
	}
	arrays := maps.Clone(table.arrays)
	if arrays == nil {
		arrays = make(map[nativeArrayViewKey]nativeArrayViewLayouts)
	}
	return &nativeViewTable{tags, slices.Clone(table.shapes), arrays}
}
func (p *Program) physicalShape(s *values.Shape, layout *values.NativeLayout) (uint64, *values.Shape) {
	key := nativeViewKey{s.Type, layout.GoType}
	table := p.viewTable()
	if tag := table.tags[key]; tag != 0 {
		return tag, table.shapes[tag-2]
	}
	p.nativeViewMu.Lock()
	defer p.nativeViewMu.Unlock()
	table = p.viewTable()
	if tag := table.tags[key]; tag != 0 {
		return tag, table.shapes[tag-2]
	}
	next := table.clone()
	tag := uint64(len(next.shapes)) + 2
	shape := nativeViewShape(s, layout)
	next.shapes = append(next.shapes, shape)
	next.tags[key] = tag
	p.nativeViews.Store(next)
	return tag, shape
}
func (p *Program) nativeViewAddress(s *values.Shape, layout *values.NativeLayout, data unsafe.Pointer) values.Value {
	if layout.Address != nil {
		return layout.Address.Encode(data)
	}
	tag, _ := p.physicalShape(s, layout)
	return values.Value{R: tag, P: data}
}
func (p *Program) nativeViewAddressShape(tag uint64) *values.Shape {
	table := p.viewTable()
	if tag < 2 || tag-2 >= uint64(len(table.shapes)) {
		return nil
	}
	return table.shapes[tag-2]
}

func (c *Call) nativeViewElement(s *values.Shape, view *values.NativeSliceView, index uint64) (values.Value, error) {
	_, shape := c.program.physicalShape(s.Elem, view.Element)
	return loadNative(c, shape, unsafe.Add(view.Data, index*view.Layout.Slice.ElementSize), &c.budget.allocated, c.budget.limits)
}

func (c *Call) copySliceViews(s *values.Shape, dst, src values.Value, start, count uint64) error {
	if count == 0 {
		return nil
	}
	if err := dst.OwnBytes(); err != nil {
		return err
	}
	dv, sv := dst.ReadNativeSlice(), src.ReadNativeSlice()
	if dv != nil && sv != nil && dv.Layout.GoType.Elem() == sv.Layout.GoType.Elem() {
		dv.Layout.Slice.Copy(unsafe.Add(dv.Data, start*dv.Layout.Slice.ElementSize), sv.Data, uint32(count))
		return nil
	}
	// Decode before writing, preserving overlap and leaving native slots unchanged
	// if a row conversion fails.
	p, err := c.AllocCells(count)
	if err != nil {
		return err
	}
	rows := values.CellSlice(p, count)
	var destination *values.Shape
	if dv != nil {
		_, destination = c.program.physicalShape(s.Elem, dv.Element)
	}
	for i := uint64(0); i < count; i++ {
		row, err := c.collectionElement(s, src, i)
		if err != nil {
			return err
		}
		if dv != nil {
			row, err = nativeStoredValue(c, destination, row, &c.budget.allocated, c.budget.limits)
			if err != nil {
				return err
			}
		}
		rows[i] = row
	}
	st := collectionStorageFor(s, false)
	for i, row := range rows {
		if dv != nil {
			dv.Element.Store(unsafe.Add(dv.Data, (start+uint64(i))*dv.Layout.Slice.ElementSize), row)
		} else if st.native != nil {
			if err := c.StoreNative(s.Elem, st.at(dst.P, start+uint64(i), s.Elem.Cells), row); err != nil {
				return err
			}
		} else {
			s.Elem.Store(values.CellAt(dst.P, (start+uint64(i))*s.Elem.Cells), row)
		}
	}
	return nil
}

func (c *Call) appendSliceViews(s *values.Shape, dst, src values.Value) (values.Value, error) {
	length, capacity := uint64(dst.SliceLen()), uint64(dst.SliceCap())
	total := length + uint64(src.SliceLen())
	if total > 1<<32-1 {
		return values.Value{}, ErrBounds
	}
	view := dst.ReadNativeSlice()
	if total > capacity {
		capacity = min(uint64(1<<32-1), max(total, capacity*2))
		if view != nil {
			st := collectionStorage{native: view.Layout.Slice}
			p, err := st.allocate(capacity, 1, &c.budget.allocated, c.budget.limits)
			if err != nil {
				return values.Value{}, err
			}
			view.Layout.Slice.Copy(p, view.Data, uint32(length))
			value, err := c.NativeSlice(view.Layout, view.Element, p, uint32(length), uint32(capacity))
			if err != nil {
				return values.Value{}, err
			}
			dst = value
		} else {
			st := collectionStorageFor(s, false)
			p, err := st.allocate(capacity, s.Elem.Cells, &c.budget.allocated, c.budget.limits)
			if err != nil {
				return values.Value{}, err
			}
			st.copy(p, dst.P, length, s.Elem.Cells)
			dst = values.SliceValue(p, uint32(length), uint32(capacity))
		}
	}
	if err := c.copySliceViews(s, dst, src, length, uint64(src.SliceLen())); err != nil {
		return values.Value{}, err
	}
	if dst.HasNativeSlice() {
		return c.resliceView(dst, 0, uint32(total), uint32(capacity))
	}
	return values.SliceValue(dst.P, uint32(total), uint32(capacity)), nil
}

type nativeSliceLogicalKey struct {
	logical *values.Type
	element reflect.Type
}

type nativeSliceType struct {
	logical *values.Type
	shape   *values.Shape
	decl    *NativeSliceDecl
}

// linkNativeSlices gives each native slice declaration its shape in this
// program. The declarations themselves belong to the environment.
func (p *Program) linkNativeSlices(e *LinkEnvironment) {
	p.nativeSliceLayouts = e.nativeSliceLayouts
	p.nativeSliceLogicalTypes = e.nativeSliceLogicalTypes
	p.nativeSliceTypes = make(map[reflect.Type]nativeSliceType, len(e.nativeSlices))
	for _, binding := range e.nativeSlices {
		binding.shape = p.Shape(binding.logical)
		p.nativeSliceTypes[binding.decl.Layout.GoType] = binding
	}
}

type nativeArrayViewKey struct {
	element *values.NativeLayout
	length  uint64
}
type nativeArrayViewLayouts struct{ array, slice *values.NativeLayout }

func (p *Program) nativeArrayView(view *values.NativeSliceView, length uint64) nativeArrayViewLayouts {
	key := nativeArrayViewKey{view.Element, length}
	if old, ok := p.viewTable().arrays[key]; ok {
		return old
	}
	p.nativeViewMu.Lock()
	defer p.nativeViewMu.Unlock()
	if old, ok := p.viewTable().arrays[key]; ok {
		return old
	}
	// Slicing an array yields the unnamed []T, even when the original
	// slice had a defined type. The array view retains the same backing.
	slice := view.Layout
	if slice.GoType.Name() != "" {
		slice = nativebind.Layout(reflect.SliceOf(slice.GoType.Elem()), nil, false)
		slice.Slice.Element = view.Element
	}
	array := view.Layout.Slice.Array(length)
	array.Array.Element = view.Element
	layouts := nativeArrayViewLayouts{array, slice}
	next := p.viewTable().clone()
	next.arrays[key] = layouts
	p.nativeViews.Store(next)
	return layouts
}
func (p *Program) nativeArraySlice(shape *values.Shape) *values.NativeLayout {
	key := nativeArrayViewKey{shape.Elem.Native, shape.Len}
	if old, ok := p.viewTable().arrays[key]; ok {
		return old.slice
	}
	p.nativeViewMu.Lock()
	defer p.nativeViewMu.Unlock()
	if old, ok := p.viewTable().arrays[key]; ok {
		return old.slice
	}
	layout := nativebind.Layout(reflect.SliceOf(shape.Elem.Native.GoType), nil, false)
	layout.Slice.Element = shape.Elem.Native
	next := p.viewTable().clone()
	next.arrays[key] = nativeArrayViewLayouts{array: shape.Native, slice: layout}
	p.nativeViews.Store(next)
	return layout
}
