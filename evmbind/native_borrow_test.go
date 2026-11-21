package evmbind_test

import (
	"context"
	"reflect"
	"testing"
	"testing/fstest"
	"unsafe"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/nativebind"
	"github.com/andreyvit/evm/values"
)

type BorrowedHeaders struct {
	S   string
	Buf []byte
	M   map[string]int32
}

func TestBorrowedHeadersRoundTrip(t *testing.T) {
	files := fstest.MapFS{"host.go": {Data: []byte(`package host
type BorrowedHeaders struct {S string; Buf []byte; M map[string]int32}
func Get() BorrowedHeaders
func Check(r BorrowedHeaders) bool
`)}}
	row := BorrowedHeaders{"a", []byte("abc"), map[string]int32{"k": 4}}
	schema, err := evmbind.New("host", files, evmbind.Options{Functions: map[string]any{
		"Get":   func() BorrowedHeaders { return row },
		"Check": func(r BorrowedHeaders) bool { return r.S == "a" && string(r.Buf) == "a" && r.M["k"] == 5 },
	}})
	if err != nil {
		t.Fatal(err)
	}
	pkg := schema.Bind(nil)
	for i := range pkg.Functions {
		f := &pkg.Functions[i]
		if f.Name == "Get" {
			f.Direct = evm.DirectBinding{}
			f.Call = func(c *evm.VM) error {
				c.SetCallResult(0, nativebind.Load(reflect.TypeOf(row), unsafe.Pointer(&row)))
				return nil
			}
		}
	}
	module, err := evmcompiler.Compile("use.go", `import "host"
func Run() bool {
	r := host.Get()
	r.M["k"]++
	clear(r.Buf)
	r.Buf = r.Buf[:1]
	copy(r.Buf,r.S)
	return r.S[0] == r.Buf[0] && host.Check(r)
}`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, pkg)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err := evmtest.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if !out[0].AsBool() || len(row.Buf) != 3 || row.Buf[1] != 0 || row.M["k"] != 5 {
		t.Fatal(out, row)
	}
}
