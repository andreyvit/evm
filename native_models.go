package evm

import (
	"fmt"
	"reflect"

	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/values"
)

type nativeModelBinding struct {
	record    *RecordType
	binding   *kvo.Binding
	ambiguous bool
	value     bool
}

func (p *Program) linkNativeModels(packages []Package) error {
	p.borrowRecords = make(map[*values.Type]RecordType)
	p.nativeModels = make(map[reflect.Type]nativeModelBinding)
	p.nativeRecordModels = make(map[*values.Type]*kvo.Model)
	for _, pkg := range packages {
		for _, r := range pkg.Records {
			p.borrowRecords[r.Pointer] = *r
			p.borrowRecords[r.Value] = *r
			if r.Mutable || p.typeIDs[r.Pointer] == 0 && p.typeIDs[r.Value] == 0 {
				continue
			}
			if shape := p.Shape(r.Value); shape != nil {
				var compiled reflect.Type
				if shape.Native != nil {
					compiled = shape.Native.GoType
				}
				if compiled != r.NativeAllocation {
					return fmt.Errorf("native allocation for model %s changed since compilation", r.Name)
				}
			}
			if r.NativeAllocation != nil && p.typeIDs[r.Value] != 0 {
				t := r.NativeAllocation
				b := nativeModelBinding{record: r, value: true}
				if old, exists := p.nativeModels[t]; exists && (old.ambiguous || old.record.Model != r.Model) {
					b.ambiguous = true
				}
				p.nativeModels[t] = b
			}
			if p.typeIDs[r.Pointer] == 0 {
				continue
			}
			p.nativeRecordModels[r.Pointer] = r.Model
			entity, ok := r.Model.Type().(*kvo.EntityType)
			if !ok {
				continue
			}
			for _, t := range entity.NativeTypes() {
				ptr := reflect.PointerTo(t)
				b := nativeModelBinding{record: r, binding: kvo.Bind(t, r.Model)}
				if old, exists := p.nativeModels[ptr]; exists && (old.ambiguous || old.record.Model != r.Model) {
					b.ambiguous = true
				}
				p.nativeModels[ptr] = b
			}
		}
	}
	return nil
}

func (c *VM) modelAnyValue(x any) (values.Value, bool, error) {
	if r, ok := x.(kvo.Record); ok {
		s := c.program.Shape(kvo.Type(r.Model()))
		if s == nil {
			return values.Value{}, true, fmt.Errorf("record model is not linked")
		}
		v, err := c.packInterface(s, r.Value())
		return v, true, err
	}
	rv := reflect.ValueOf(x)
	b, ok := c.program.nativeModels[rv.Type()]
	if !ok {
		return values.Value{}, false, nil
	}
	if b.ambiguous {
		return values.Value{}, true, fmt.Errorf("native type %s has multiple linked models; pass an explicit KVO record", rv.Type())
	}
	if b.value {
		if err := c.ChargeAllocation(uint64(rv.Type().Size())); err != nil {
			return values.Value{}, true, err
		}
		native := reflect.New(rv.Type())
		native.Elem().Set(rv)
		cell, err := c.AllocCells(1)
		if err != nil {
			return values.Value{}, true, err
		}
		*(*values.Value)(cell) = values.Value{P: native.UnsafePointer()}
		v, err := c.packInterface(c.program.Shape(b.record.Value), values.Value{P: cell})
		return v, true, err
	}
	r := b.binding.Record(rv)
	v, err := c.packInterface(c.program.Shape(b.record.Pointer), r.Value())
	return v, true, err
}
