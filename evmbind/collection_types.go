package evmbind

import (
	"sync"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/values"
)

type collectionTypeKey struct {
	kind      nodeKind
	key, elem *values.Type
}

var collectionTypes sync.Map

// Unnamed Go pointers and collections have structural identity. Intern by
// element/key identities; named collections keep their declaring package.
func collectionType(n *node) *values.Type {
	key := collectionTypeKey{kind: n.kind, elem: n.elem.shape.Type}
	if n.kind == nodeMap {
		key.key = n.key.shape.Type
	}
	return key.typeValue()
}

func (key collectionTypeKey) typeValue() *values.Type {
	if key.kind == nodeMap && key.key == values.StringType && key.elem == values.AnyType {
		return evm.JSONObjectType
	}
	if key.kind == nodeSlice && key.elem == values.AnyType {
		return evm.AnySliceType
	}
	if key.kind == nodeSlice && key.elem == values.Uint8 {
		return evm.ByteSliceType
	}
	name := "[]" + key.elem.Name()
	repr := values.Pair
	if key.kind == nodePointer {
		name = "*" + key.elem.Name()
	}
	if key.kind == nodeMap {
		name = "map[" + key.key.Name() + "]" + key.elem.Name()
		repr = values.Pointer
	}
	if old, ok := collectionTypes.Load(key); ok {
		return old.(*values.Type)
	}
	value, _ := collectionTypes.LoadOrStore(key, values.NewType(name, repr))
	return value.(*values.Type)
}
