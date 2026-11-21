package evmcompiler_test

import (
	"context"
	"testing"
	"unsafe"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

func TestBorrowedByteOperations(t *testing.T) {
	data := []byte("abcd")
	var last values.Value
	s := &values.Shape{Type: evm.ByteSliceType, Kind: values.SliceKind, Cells: 1, Elem: &values.Shape{Type: values.Uint8, Kind: values.UintKind, Bits: 8, Cells: 1}}
	pkg := evm.Package{Path: "host", Types: []evm.TypeDecl{{Shape: s}}, Functions: []evm.ExternalFunction{
		{Name: "Get", Signature: evm.Signature{Results: []*values.Type{evm.ByteSliceType}}, Ownership: &evm.OwnershipEffects{NoRetain: true, NoInvalidate: true, Borrowed: []evm.BorrowSource{{Domain: "bytes"}}}, Call: func(c *evm.Call) error {
			v, err := c.BorrowBytes(data)
			if err == nil {
				last = v
				c.SetResult(0, v)
			}
			return err
		}},
		{Name: "End", Ownership: &evm.OwnershipEffects{NoRetain: true, Ends: []evm.ScopeEnd{{Domain: "bytes"}}}, Call: func(*evm.Call) error { data[1] = '!'; return nil }},
	}}
	const src = `import "host"
type Bytes []byte
func Read() int { b:=host.Get()[1:]; return int(b[0])+len(b)+cap(b) }
func Write() int { b:=host.Get(); a:=b[1:3]; b[2]='X'; return int(a[1]) }
func Clear() int { b:=host.Get(); clear(b[1:3]); return int(b[1])+int(b[3]) }
func Copy() int { b:=host.Get(); copy(b[1:],b); return int(b[2]) }
func Text() int { b:=host.Get(); copy(b[1:],"xy");return int(b[2]) }
func CopyFrom() int { b:=host.Get(); dst:=make([]byte,2);copy(dst,b[1:]);return int(dst[1]) }
func AppendInPlace() int { b:=host.Get(); a:=append(b[:1],"xy"...);return int(a[1])+int(b[2]) }
func Grow() int { b:=host.Get(); a:=append(b,'X');return int(a[4])+int(b[1]) }
func Pointer() int { b:=host.Get(); p:=&b[1]; *p='Q';return int(b[1]) }
func Array() int { b:=host.Get(); a:=(*[2]byte)(b);a[0]='Q';return int(b[0]) }
func Joined() []byte { b:=host.Get(); return append(b[:2],""...) }
func Escape() any { b:=Bytes(host.Get()[1:]);return b }
var kept []byte
func Global() int { kept=host.Get()[1:]; host.End(); return int(kept[0]) }
func InMap() int { m:=map[int]any{1:Bytes(host.Get())};host.End();return int(m[1].(Bytes)[1]) }
func Capture() func() byte { a:=host.Get()[1:];return func() byte{return a[0]} }
func Indirect() int { get:=host.Get; a:=get();host.End();return int(a[1]) }
func AcrossEnd() int { b:=Bytes(host.Get())[1:];host.End();return int(b[0]) }
`
	for _, debug := range []bool{false, true} {
		mod, err := (evmcompiler.Options{NoOptimization: debug}).Compile("bytes.go", src, pkg)
		if err != nil {
			t.Fatal(err)
		}
		p, err := evm.Link(mod, pkg)
		if err != nil {
			t.Fatal(err)
		}
		vm := evm.New(p)
		for _, tc := range []struct {
			name  string
			want  uint64
			owned bool
		}{
			{"Read", 'b' + 6, false}, {"Write", 'X', true}, {"Clear", 'd', true}, {"Copy", 'b', true}, {"Text", 'y', true}, {"CopyFrom", 'c', false}, {"AppendInPlace", 'x' + 'y', true}, {"Grow", 'X' + 'b', false}, {"Pointer", 'Q', true}, {"Array", 'Q', true}, {"Joined", 0, true}, {"Escape", 0, true}, {"AcrossEnd", 'b', true}, {"Global", 'b', true}, {"InMap", 'b', true}, {"Capture", 0, true}, {"Indirect", 'b', true},
		} {
			copy(data, "abcd")
			var out [1]values.Value
			if err := vm.Run(context.Background(), tc.name, nil, out[:]); err != nil {
				t.Errorf("%s debug=%v: %v", tc.name, debug, err)
				continue
			}
			if tc.want != 0 && out[0].R != tc.want {
				t.Errorf("%s debug=%v: got %v want %d", tc.name, debug, out[0], tc.want)
			}
			if owned := last.SliceReadData() != unsafe.Pointer(&data[0]); owned != tc.owned {
				t.Errorf("%s debug=%v: owned=%v want %v", tc.name, debug, owned, tc.owned)
			}
			wantSource := "abcd"
			switch tc.name {
			case "AcrossEnd", "Global", "InMap", "Indirect":
				wantSource = "a!cd"
			}
			if string(data) != wantSource {
				t.Errorf("%s changed source to %q", tc.name, data)
			}
		}
	}
}
