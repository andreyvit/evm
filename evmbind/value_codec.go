package evmbind

import (
	"fmt"
	"reflect"
	"unsafe"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/values"
)

// ValueCodec converts one Go type declared by a binding schema.
type ValueCodec[T any] struct {
	schema *Schema
	typ    *values.Type
	node   *node
	layout *values.Shape
	pack   func(T) values.Value
	unpack func(values.Value) (T, error)
}

// Codec resolves the schema type once. Pointer codecs preserve object identity.
func Codec[T any](s *Schema) (*ValueCodec[T], error) {
	t := reflect.TypeFor[T]()
	c := &ValueCodec[T]{schema: s, typ: s.NativeType(t)}
	if n, ok := s.nativeModels[t]; ok {
		if n == nil {
			return nil, fmt.Errorf("multiple model bindings for %v", t)
		}
		c.typ, c.node = n.model.row.Pointer, n
		return c, nil
	}
	if ptr := s.pointers[t]; ptr != nil {
		c.typ = ptr.vmType
		c.pack = func(x T) values.Value { return values.Value{P: ptr.pointer(x)} }
		c.unpack = func(v values.Value) (T, error) { return ptr.load(v.P).(T), nil }
		return c, nil
	}
	if c.typ == nil {
		// A model value uses a different layout from its pointer, even if
		// both bindings use the same native Go struct.
		for n, nt := range s.nodesByNative {
			if nt != t || n.kind != nodeRecord || n.model.row.Mutable || n.shape == nil {
				continue
			}
			if c.node != nil && c.typ != n.shape.Type {
				return nil, fmt.Errorf("multiple model bindings for %v", t)
			}
			c.typ, c.node = n.shape.Type, n
		}
		if c.node != nil {
			return c, nil
		}
		return nil, fmt.Errorf("no binding for %v", t)
	}
	for n, nt := range s.nodesByNative {
		if nt == t && n.shape != nil && n.shape.Type == c.typ {
			c.node = n
			break
		}
	}
	if t.Kind() == reflect.Pointer && s.fields[t.Elem()] != nil {
		c.pack = func(x T) values.Value {
			p := *(*unsafe.Pointer)(unsafe.Pointer(&x))
			if p == nil {
				return values.Value{}
			}
			return values.Value{R: values.NativeAddress, P: p}
		}
		c.unpack = func(v values.Value) (T, error) {
			var out T
			if v.P != nil && v.R != values.NativeAddress {
				return out, fmt.Errorf("expected native storage for %v", t)
			}
			*(*unsafe.Pointer)(unsafe.Pointer(&out)) = v.P
			return out, nil
		}
	}
	if c.node != nil && c.node.shape.Native != nil && (c.node.kind == nodeStruct || c.node.kind == nodeSlice && !isModelSlice(c.node) || c.node.kind == nodeMap) {
		c.layout = c.node.shape
	}
	if c.node == nil && c.pack == nil {
		return nil, fmt.Errorf("no value conversion for %v", t)
	}
	return c, nil
}
func (c *ValueCodec[T]) Type() *values.Type { return c.typ }

// Pack borrows a live Go struct pointer. Use PackIn for budgeted conversions.
func (c *ValueCodec[T]) Pack(x T) values.Value {
	if c.pack == nil {
		panic("this codec requires PackIn")
	}
	return c.pack(x)
}
func (c *ValueCodec[T]) Unpack(v values.Value) (T, error) {
	if c.unpack == nil {
		var zero T
		return zero, fmt.Errorf("this codec requires UnpackIn")
	}
	return c.unpack(v)
}
func (c *ValueCodec[T]) PackIn(vm *evm.VM, x T) (v values.Value, err error) {
	if c.pack != nil {
		return c.pack(x), nil
	}
	if c.layout != nil {
		return vm.LoadNative(c.layout, unsafe.Pointer(&x))
	}
	defer Catch(c.typ.Name(), &err)
	return encode(&Frame{VM: vm, s: c.schema}, c.node, reflect.ValueOf(&x).Elem()), nil
}
func (c *ValueCodec[T]) UnpackIn(vm *evm.VM, v values.Value) (x T, err error) {
	if c.unpack != nil {
		return c.unpack(v)
	}
	if c.layout != nil {
		err = vm.StoreNative(c.layout, unsafe.Pointer(&x), v)
		return x, err
	}
	defer Catch(c.typ.Name(), &err)
	a := decode(&Frame{VM: vm, s: c.schema}, c.node, v, c.schema.nodesByNative)
	reflect.ValueOf(&x).Elem().Set(a)
	return x, nil
}
