package evm

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"time"
	"unsafe"

	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/values"
)

// GuestValue retains a guest-only value with its owning program. Returning it
// to that program preserves its type and reference identity without JSON.
type GuestValue struct {
	Program *Program
	Value   values.Value
}

// MarshalJSON serializes a retained guest value at a native JSON output boundary.
// It uses the same supported types and cycle checks as explicit VM JSON encoding.
func (g *GuestValue) MarshalJSON() ([]byte, error) {
	return g.Program.EncodeJSON(context.Background(), g.Value, math.MaxInt)
}

// String formats retained JSON-shaped values at native text output boundaries.
// Unsupported values produce a diagnostic rather than exposing VM handles.
func (g *GuestValue) String() string {
	if g == nil {
		return "<nil>"
	}
	value, err := g.Program.JSONValue(context.Background(), g.Value, math.MaxInt)
	if err != nil {
		return "<EVM value: " + err.Error() + ">"
	}
	return fmt.Sprint(value)
}

var nativeScalarTypes = map[*values.Type]reflect.Type{
	values.Boolean:  reflect.TypeFor[bool](),
	values.TimeType: reflect.TypeFor[time.Time](),
	values.IntType:  reflect.TypeFor[int](), values.Int8: reflect.TypeFor[int8](), values.Int16: reflect.TypeFor[int16](), values.Int32: reflect.TypeFor[int32](), values.Int64: reflect.TypeFor[int64](),
	values.UintType: reflect.TypeFor[uint](), values.Uint8: reflect.TypeFor[uint8](), values.Uint16: reflect.TypeFor[uint16](), values.Uint32: reflect.TypeFor[uint32](), values.Uint64: reflect.TypeFor[uint64](),
	values.Float32Type: reflect.TypeFor[float32](), values.Float64: reflect.TypeFor[float64](), values.StringType: reflect.TypeFor[string](),
}

var nativeScalarVMTypes = func() map[reflect.Type]*values.Type {
	out := make(map[reflect.Type]*values.Type, len(nativeScalarTypes))
	for vm, native := range nativeScalarTypes {
		out[native] = vm
	}
	return out
}()

func nativeAnyLayout(s *values.Shape) bool {
	if s.Native == nil || s.Native.GoType == nil {
		return false
	}
	switch s.Kind {
	case values.MapKind:
		return s.Native.Map != nil
	case values.SliceKind:
		return s.Native.Slice != nil
	case values.InterfaceKind, values.FunctionKind:
		return false
	}
	return true
}

// NativeAny converts a guest interface to native Go. Registered native types
// keep their Go types and backing storage. Model row pointers return their live
// native pointer, or a kvo.Record for encoded data; native-backed row values
// return Go value copies. Guest-only types retain a handle.
func (c *Call) NativeAny(v values.Value) (any, error) {
	s, x, err := c.program.UnpackInterface(v)
	if err != nil || s == nil {
		return nil, err
	}
	if s.Kind == values.TypedPairKind && s.CellStruct == nil {
		if native, ok := nativeMutableRecord(recordCell(x)); ok {
			return native.NativePointer(), nil
		}
		r, ok := kvo.TryFromValue(x)
		if !ok && x.P == nil {
			if model := c.program.nativeRecordModels[s.Type]; model != nil {
				r, ok = kvo.Record{}.WithModel(model), true
			}
		}
		if ok {
			if ptr := r.NativePointer(); ptr != nil {
				return ptr, nil
			}
			if err := c.ChargeAllocation(uint64(unsafe.Sizeof(r))); err != nil {
				return nil, err
			}
			return r, nil
		}
	}
	if s.Kind == values.SliceKind {
		if view := x.ReadNativeSlice(); view != nil {
			if err := c.ChargeAllocation(view.Layout.Size); err != nil {
				return nil, err
			}
			typ := view.Layout.GoType
			if declared := c.program.nativeSliceLogicalTypes[nativeSliceLogicalKey{s.Type, typ.Elem()}]; declared != nil {
				typ = declared
			}
			if err := x.OwnBytes(); err != nil {
				return nil, err
			}
			return reflect.SliceAt(typ.Elem(), x.SliceReadData(), int(view.Capacity)).Slice(0, int(view.Length)).Convert(typ).Interface(), nil
		}
	}
	if s.Kind == values.NativeValueKind {
		return x.AsNativeValue(), nil
	}
	if x, ok, err := c.nativeScalarAny(s.Type, x); ok || err != nil {
		return x, err
	}
	if nativeAnyLayout(s) {
		typ := s.Native.GoType
		if s.Kind == values.PointerKind && (x.P == nil || x.R == values.NativeAddress || s.Type.Repr() == values.Pointer) {
			return reflect.NewAt(typ.Elem(), x.P).Interface(), nil
		}
		if err := c.ChargeAllocation(s.Native.Size); err != nil {
			return nil, err
		}
		p := s.Native.New()
		if err := storeNative(c, s, p, x, &c.budget.allocated, c.budget.limits); err != nil {
			return nil, err
		}
		return reflect.NewAt(typ, p).Elem().Interface(), nil
	}
	if c.program.hasInterfaceMethod(v, "Error", Signature{Results: []*values.Type{values.StringType}}) {
		return c.adaptGuestError(v)
	}
	if err := c.ChargeAllocation(uint64(unsafe.Sizeof(GuestValue{}))); err != nil {
		return nil, err
	}
	return &GuestValue{Program: c.program, Value: v}, nil
}

// AnyValue converts native Go data into a guest interface. Native reference
// values retain their backing storage; guest handles must belong to this program.
func (c *Call) AnyValue(x any) (values.Value, error) {
	if err, ok := x.(error); ok {
		return c.ErrorValue(err)
	}
	return c.anyValue(x)
}

// TypedInterfaceValue preserves the concrete identity promised by a typed
// native interface. AnyValue separately preserves native error adaptation.
func (c *Call) TypedInterfaceValue(x any) (values.Value, error) {
	if c.program.nativeGoShapes[reflect.TypeOf(x)] != nil {
		return c.anyValue(x)
	}
	return c.AnyValue(x)
}

func (c *Call) anyValue(x any) (values.Value, error) {
	if x == nil {
		return values.Value{}, nil
	}
	if guest, ok := x.(*GuestValue); ok {
		if guest == nil {
			return values.Value{}, nil
		}
		if guest.Program != c.program {
			return values.Value{}, fmt.Errorf("guest value belongs to another program")
		}
		return guest.Value, nil
	}
	if v, ok, err := c.modelAnyValue(x); ok {
		return v, err
	}
	rv := reflect.ValueOf(x)
	if binding, ok := c.program.nativeSliceTypes[rv.Type()]; ok {
		if binding.shape == nil {
			return values.Value{}, fmt.Errorf("native type %s is not linked", rv.Type())
		}
		if uint64(rv.Cap()) > 1<<32-1 {
			return values.Value{}, ErrBounds
		}
		v, err := c.NativeSlice(binding.decl.Layout, binding.decl.Element, rv.UnsafePointer(), uint32(rv.Len()), uint32(rv.Cap()))
		if err != nil {
			return values.Value{}, err
		}
		return c.packInterface(binding.shape, v)
	}
	if typ := nativeScalarVMTypes[rv.Type()]; typ != nil {
		s := c.program.Shape(typ)
		if s == nil {
			return values.Value{}, fmt.Errorf("native type %s is not linked", rv.Type())
		}
		value, err := c.nativeScalar(rv)
		if err != nil {
			return values.Value{}, err
		}
		return c.packInterface(s, value)
	}
	if s := c.program.nativeGoShapes[rv.Type()]; s != nil && nativeAnyLayout(s) {
		if s.Kind == values.PointerKind {
			return c.packInterface(s, values.Value{R: values.NativeAddress, P: rv.UnsafePointer()})
		}
		if err := c.ChargeAllocation(s.Native.Size); err != nil {
			return values.Value{}, err
		}
		p := s.Native.New()
		reflect.NewAt(rv.Type(), p).Elem().Set(rv)
		value, err := loadNative(c, s, p, &c.budget.allocated, c.budget.limits)
		if err != nil {
			return values.Value{}, err
		}
		return c.packInterface(s, value)
	}
	return values.Value{}, fmt.Errorf("unregistered native type %T", x)
}

// NativeAnyCodec is shared by all host packages exposing ordinary any fields.
// NativeLayout supplies the address of a Go interface, not its ABI words.
var NativeAnyCodec = &NativeValueCodec{
	Decode: func(c *Call, v values.Value) (values.Value, error) { return c.AnyValue(*(*any)(v.P)) },
	Encode: func(c *Call, v values.Value) (values.Value, error) {
		x, err := c.NativeAny(v)
		if err != nil {
			return values.Value{}, err
		}
		if err := c.ChargeAllocation(uint64(unsafe.Sizeof(x))); err != nil {
			return values.Value{}, err
		}
		return values.Value{P: unsafe.Pointer(&x)}, nil
	},
}

// NativeInterfaceCodec bridges a native method-interface slot without changing
// the concrete object's storage. Guest-only implementations cannot inhabit a
// Go interface; values must have a native type implementing the interface.
func NativeInterfaceCodec(t reflect.Type) *NativeValueCodec {
	if t.Kind() != reflect.Interface {
		panic("native interface codec requires an interface type")
	}
	return &NativeValueCodec{
		Decode: func(c *Call, v values.Value) (values.Value, error) {
			return c.anyValue(reflect.NewAt(t, v.P).Elem().Interface())
		},
		Encode: func(c *Call, v values.Value) (values.Value, error) {
			shape, _, err := c.program.UnpackInterface(v)
			if err != nil {
				return values.Value{}, err
			}
			// NativeAny adapts guest errors for ordinary error boundaries. A
			// method-interface slot must not silently acquire such a proxy.
			if shape != nil && !nativeAnyLayout(shape) && shape.Kind != values.TypedPairKind {
				return values.Value{}, fmt.Errorf("guest type %s does not implement native interface %s", shape.Type.Name(), t)
			}
			x, err := c.NativeAny(v)
			if err != nil {
				return values.Value{}, err
			}
			if x != nil && !reflect.TypeOf(x).AssignableTo(t) {
				return values.Value{}, fmt.Errorf("native value %T does not implement %s", x, t)
			}
			if err := c.ChargeAllocation(uint64(t.Size())); err != nil {
				return values.Value{}, err
			}
			cell := reflect.New(t)
			if x != nil {
				cell.Elem().Set(reflect.ValueOf(x))
			}
			return values.Value{P: cell.UnsafePointer()}, nil
		},
	}
}

// nativeScalar converts a Go value whose type is in nativeScalarVMTypes.
func (c *Call) nativeScalar(rv reflect.Value) (values.Value, error) {
	switch rv.Kind() {
	case reflect.Struct:
		t := rv.Interface().(time.Time)
		if err := c.ChargeAllocation(values.NativeTimeAllocBytes(t)); err != nil {
			return values.Value{}, err
		}
		return values.NativeTime(t), nil
	case reflect.Bool:
		return values.Bool(rv.Bool()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return values.Int(rv.Int()), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return values.Uint(rv.Uint()), nil
	case reflect.Float32:
		return values.Float32(float32(rv.Float())), nil
	case reflect.Float64:
		return values.Float(rv.Float()), nil
	case reflect.String:
		return values.String(rv.String()), nil
	}
	return values.Value{}, fmt.Errorf("native type %s is not a scalar", rv.Type())
}

// nativeScalarAny converts a VM value of a scalar or time type to the Go
// value of the matching Go type. It needs no linked interface type.
func (c *Call) nativeScalarAny(t *values.Type, x values.Value) (any, bool, error) {
	typ := nativeScalarTypes[t]
	if typ == nil {
		return nil, false, nil
	}
	if err := c.ChargeAllocation(uint64(typ.Size())); err != nil {
		return nil, false, err
	}
	switch t {
	case values.TimeType:
		return x.AsTime(), true, nil
	case values.Boolean:
		return x.R != 0, true, nil
	case values.IntType:
		return int(x.R), true, nil
	case values.Int8:
		return int8(x.R), true, nil
	case values.Int16:
		return int16(x.R), true, nil
	case values.Int32:
		return int32(x.R), true, nil
	case values.Int64:
		return int64(x.R), true, nil
	case values.UintType:
		return uint(x.R), true, nil
	case values.Uint8:
		return uint8(x.R), true, nil
	case values.Uint16:
		return uint16(x.R), true, nil
	case values.Uint32:
		return uint32(x.R), true, nil
	case values.Uint64:
		return x.R, true, nil
	case values.Float32Type:
		return x.AsFloat32(), true, nil
	case values.Float64:
		return x.AsFloat(), true, nil
	case values.StringType:
		return x.AsString(), true, nil
	}
	return nil, false, nil
}
