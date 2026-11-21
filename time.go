package evm

import (
	"github.com/andreyvit/evm/nativebind"
	"github.com/andreyvit/evm/values"
	"reflect"
	"time"
	"unsafe"
)

// TimePackage exposes time.Time and its native storage. It is linked
// automatically, so record fields and host methods share the same nominal type.
func TimePackage() Package {
	p := Package{Path: "time", Types: []TypeDecl{{Name: "Time", Shape: &values.Shape{Type: values.TimeType, Kind: values.TimestampKind, Cells: 1, Native: nativebind.Layout(reflect.TypeFor[time.Time](), nil, true)}, NativeCodec: nativeTimeCodec}}}
	p.Constants = append(p.Constants,
		ExternalConstant{Name: "RFC3339", Type: values.StringType, Value: values.String(time.RFC3339)},
		ExternalConstant{Name: "RFC3339Nano", Type: values.StringType, Value: values.String(time.RFC3339Nano)})
	p.Methods = append(p.Methods, ExternalMethod{Name: "Format", Receiver: values.TimeType, Ownership: &OwnershipEffects{NoRetain: true, NoInvalidate: true}, Signature: Signature{Params: []*values.Type{values.StringType}, Results: []*values.Type{values.StringType}}, Call: func(c *VM) error {
		text := c.CallArgument(0).AsTime().Format(c.CallArgument(1).AsString())
		if err := c.ChargeAllocation(uint64(len(text))); err != nil {
			return err
		}
		c.SetCallResult(0, values.String(text))
		return nil
	}})
	add := func(name string, params []*values.Type, result *values.Type, f func(*VM) values.Value) {
		p.Methods = append(p.Methods, ExternalMethod{Ownership: &OwnershipEffects{NoRetain: true, NoInvalidate: true}, Name: name, Receiver: values.TimeType, Signature: Signature{Params: params, Results: []*values.Type{result}}, Call: func(c *VM) error { c.SetCallResult(0, f(c)); return nil }})
	}
	add("IsZero", nil, values.Boolean, func(c *VM) values.Value { return values.Bool(c.CallArgument(0).AsTime().IsZero()) })
	for _, name := range []string{"Before", "After", "Equal"} {
		add(name, []*values.Type{values.TimeType}, values.Boolean, func(c *VM) values.Value {
			a, b := c.CallArgument(0).AsTime(), c.CallArgument(1).AsTime()
			switch name {
			case "Before":
				return values.Bool(a.Before(b))
			case "After":
				return values.Bool(a.After(b))
			default:
				return values.Bool(a.Equal(b))
			}
		})
	}
	for _, name := range []string{"Unix", "UnixMilli", "UnixMicro", "UnixNano"} {
		add(name, nil, values.Int64, func(c *VM) values.Value {
			t := c.CallArgument(0).AsTime()
			switch name {
			case "Unix":
				return values.Int(t.Unix())
			case "UnixMilli":
				return values.Int(t.UnixMilli())
			case "UnixMicro":
				return values.Int(t.UnixMicro())
			default:
				return values.Int(t.UnixNano())
			}
		})
	}
	return p
}

// WithTimePackage puts the built-in time package first. A supplied package
// with path "time" extends it: its declarations join the built-in ones, so a
// host can add types, functions and further Time methods.
func WithTimePackage(packages []Package) ([]Package, error) {
	out := make([]Package, 0, len(packages)+1)
	out = append(out, TimePackage())
	for _, p := range packages {
		if p.Path != "time" {
			out = append(out, p)
			continue
		}
		merged, err := MergePackages(out[0], p)
		if err != nil {
			return nil, err
		}
		out[0] = merged[0]
	}
	return out, nil
}

// Native slots contain Go time.Time; logical VM values own their snapshots.
var nativeTimeCodec = &NativeValueCodec{
	Decode: func(c *VM, v values.Value) (values.Value, error) {
		t := *(*time.Time)(v.P)
		if err := c.ChargeAllocation(values.NativeTimeAllocBytes(t)); err != nil {
			return values.Value{}, err
		}
		return values.NativeTime(t), nil
	},
	Encode: func(c *VM, v values.Value) (values.Value, error) {
		if v == (values.Value{}) {
			return v, nil
		}
		if p := v.NativeTimeAddress(); p != nil {
			return values.Value{P: p}, nil
		}
		if err := c.ChargeAllocation(uint64(unsafe.Sizeof(time.Time{}))); err != nil {
			return values.Value{}, err
		}
		p := new(time.Time)
		*p = v.AsTime()
		return values.Value{P: unsafe.Pointer(p)}, nil
	},
}
