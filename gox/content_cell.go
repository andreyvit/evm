package gox

import (
	"math"
	"unsafe"

	"github.com/andreyvit/evm/values"
)

var contentCell = newContentCell()

type contentCellCodec struct {
	*values.CellStruct
	plain, trusted values.RepresentationID
}

func newContentCell() *contentCellCodec {
	c := &contentCellCodec{}
	t := values.NewOwnedType("github.com/andreyvit/evm/gox.Content", values.Pair, func(v values.Value, b values.OwnershipBudget) (values.Value, error) {
		text, err := values.StringType.Own(values.String(c.text(v)), b)
		if err != nil {
			return values.Value{}, err
		}
		return c.pack(Content{text.AsString(), v.Representation() == c.trusted}), nil
	})
	c.plain = values.RegisterRepresentation(&values.Shape{Type: t, Kind: values.TypedPairKind, Cells: 1})
	c.trusted = values.RegisterRepresentation(&values.Shape{Type: t, Kind: values.TypedPairKind, Cells: 1})
	c.CellStruct = &values.CellStruct{
		Type: t,
		Get: func(v values.Value, i int) values.Value {
			switch i {
			case 0:
				return values.String(c.text(v))
			case 1:
				return values.Bool(v.Representation() == c.trusted)
			default:
				panic("invalid Content field")
			}
		},
		Pack:        func(v [2]values.Value) values.Value { return c.pack(Content{v[0].AsString(), v[1].R != 0}) },
		LoadNative:  func(p unsafe.Pointer) values.Value { return c.pack(*(*Content)(p)) },
		StoreNative: func(p unsafe.Pointer, v values.Value) { *(*Content)(p) = c.unpack(v) },
	}
	return c
}

func (c *contentCellCodec) pack(x Content) values.Value {
	length := contentLength(uint64(len(x.Content)))
	id := c.plain
	if x.TrustedHTML {
		id = c.trusted
	}
	return values.TypedPair(id, unsafe.Pointer(unsafe.StringData(x.Content)), length)
}
func (c *contentCellCodec) unpack(v values.Value) Content {
	return Content{c.text(v), v.Representation() == c.trusted}
}
func (c *contentCellCodec) text(v values.Value) string {
	// Zero cells and interface zero carriers both have an empty payload.
	if v.P == nil {
		return ""
	}
	return unsafe.String((*byte)(v.P), v.Auxiliary())
}

func contentLength(length uint64) uint32 {
	if length > math.MaxUint32 {
		panic("gox.Content exceeds maximum byte length")
	}
	return uint32(length)
}
