package evmcompiler_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/nativebind"
	"github.com/andreyvit/evm/values"
)

func TestNativeGenericReceiverMethods(t *testing.T) {
	host := evm.Package{Path: "host", Source: `package host
 type Index[K any] struct{Key K}
 func (i *Index[K]) Read() K
 `}
	for _, tc := range []struct {
		name   string
		value  any
		scalar *values.Shape
	}{
		{"Number", &sourceNativeIndex[int]{Key: 42}, &values.Shape{Type: values.IntType, Kind: values.IntKind, Bits: 64, Cells: 1}},
		{"Text", &sourceNativeIndex[string]{Key: "native"}, &values.Shape{Type: values.StringType, Kind: values.StringKind, Cells: 1}},
	} {
		native := reflect.TypeOf(tc.value).Elem()
		shape := &values.Shape{Type: values.NewType("host.Index["+tc.scalar.Type.Name()+"]", values.Pointer), Kind: values.StructKind, Cells: 1, Fields: []values.Field{{Name: "Key", Shape: tc.scalar}}, Native: nativebind.Layout(native, nativebind.BindFields(native, nil, nativebind.Automatic), false)}
		ptr := values.NewPointerShape(shape)
		ptr.Type = values.NewType("*"+shape.Type.Name(), values.Pair)
		ptr.Native = nativebind.Layout(reflect.PointerTo(native), nil, true)
		host.Types = append(host.Types, evm.TypeDecl{SourceName: "Index", TypeArgs: []*values.Type{tc.scalar.Type}, Shape: shape}, evm.TypeDecl{Shape: ptr})
		host.Values = append(host.Values, evm.ExternalValue{Name: tc.name, Type: ptr.Type, Read: func(c *evm.Call) error {
			c.SetResult(0, values.Value{R: values.NativeAddress, P: reflect.ValueOf(tc.value).UnsafePointer()})
			return nil
		}})
		host.Methods = append(host.Methods, evm.ExternalMethod{Name: "Read", Receiver: ptr.Type, Signature: evm.Signature{Results: []*values.Type{tc.scalar.Type}}, Call: func(c *evm.Call) error {
			v := reflect.NewAt(native, c.Argument(0).P).Elem().Field(0)
			if v.Kind() == reflect.String {
				c.SetResult(0, values.String(v.String()))
			} else {
				c.SetResult(0, values.Int(v.Int()))
			}
			return nil
		}})
	}
	for _, tc := range []struct {
		name, want string
		mutate     func(*evm.Package)
	}{
		{"missing binding", "no native Read", func(p *evm.Package) { p.Methods = p.Methods[:1] }},
		{"wrong signature", "native source signature mismatch", func(p *evm.Package) { p.Methods[0].Signature.Results = []*values.Type{values.StringType} }},
		{"duplicate", "duplicate native method", func(p *evm.Package) { p.Methods = append(p.Methods, p.Methods[0]) }},
		{"receiver mismatch", "receiver mismatch", func(p *evm.Package) { p.Source = strings.ReplaceAll(p.Source, "*Index[K]", "Index[K]") }},
		{"instance factory", "cannot combine", func(p *evm.Package) {
			p.Methods[0].Call = nil
			p.Methods[0].Signature = evm.Signature{}
			p.Methods[0].Instantiate = func([]*values.Type) (evm.ExternalFunction, error) { panic("unexpected factory") }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := host
			p.Methods = append([]evm.ExternalMethod(nil), host.Methods...)
			tc.mutate(&p)
			_, err := evmcompiler.Compile("invalid.go", `import "host";func Run(){host.Number.Read();host.Text.Read()}`, p)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
	module, err := evmcompiler.Compile("generic-receiver.go", `import "host"
 type NumberReader interface{Read()int}
 type TextReader interface{Read()string}
 func Run()bool {
  n:=host.Number.Read;s:=host.Text.Read
  var nr NumberReader=host.Number;var sr TextReader=host.Text
  return n()==42 && s()=="native" && nr.Read()==42 && sr.Read()=="native" && (*host.Index[int]).Read(host.Number)==42
 }`, host)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, host)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evm.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if out[0].R != 1 {
		t.Fatal(out)
	}
}
