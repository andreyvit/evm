package kvo

import (
	"reflect"

	"github.com/andreyvit/evm/kvo/codec/msgpack"
	"github.com/andreyvit/evm/kvo/codec/row2"
	"github.com/andreyvit/evm/values"
)

// TryDecodeRecord decodes a record into an addressable native struct. Encoded
// data is detached by the native decoder. A native record of the same Go type
// uses ordinary struct assignment, including its reference fields; use Clone
// first when an independent native data graph is required.
//
// MessagePack retains unmodeled fields when the native source and destination
// types match. Conversion between different native types uses modeled fields
// through the row2 codec, including its representation constraints. Same-type
// conversion preserves Go fields, even if the source uses another property
// mapping. On a decode error, dst may have been cleared or partly written.
func (b *Binding) TryDecodeRecord(r Record, dst reflect.Value) error {
	return decodeRecord(b.root, b, r, dst)
}

// TryDecodeRecord has the same semantics as Binding.TryDecodeRecord and uses
// this projection's field mapping when decoding modeled data.
func (p *Projection) TryDecodeRecord(r Record, dst reflect.Value) error {
	return decodeRecord(p.root, nil, r, dst)
}

func decodeRecord(target *nativePlan, binding *Binding, r Record, dst reflect.Value) (err error) {
	defer func() {
		if x := recover(); x != nil {
			err = recoveredError("KVO record decode", x)
		}
	}()
	if !dst.IsValid() || !dst.CanSet() || dst.Type() != target.typ || r.Missing() {
		return row2.ErrType
	}
	if model := r.Model(); model != nil && model != target.model {
		return row2.ErrType
	}
	source := r.expanded()
	if source.object != nil && source.plan != nil && source.plan.typ == target.typ {
		dst.Set(source.objectValue())
		return nil
	}
	if !source.msg.Missing() && source.plan != nil && source.plan.typ == target.typ {
		dst.SetZero()
		return msgpack.TryDecode(source.msg.Raw(), dst)
	}
	raw := r.Snapshot().Raw()
	if binding != nil {
		return binding.TryDecode(raw, dst)
	}
	dst.SetZero()
	target.read(dst, values.Bytes(raw), true, 0)
	return nil
}
