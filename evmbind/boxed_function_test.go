package evmbind_test

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmcompiler"
)

func TestSignaturePolymorphicNamedFunction(t *testing.T) {
	var fn *evm.NamedFunction
	var ordinary any
	schema, err := evmbind.New("host", fstest.MapFS{"api.go": {Data: []byte(`package host
 import "github.com/andreyvit/evm"
 func Register(fn *evm.NamedFunctionOf[any])
 func Typed(fn *evm.NamedFunctionOf[func()])
 func Void(fn func())
 func Ordinary(value any)
 func Native(n int)int
 `)}}, evmbind.Options{Selective: true, Functions: map[string]any{
		"Register": func(v *evm.NamedFunction) { fn = v },
		"Typed":    func(v *evm.NamedFunction) { fn = v },
		"Void":     func(v func()) { v() },
		"Ordinary": func(v any) { ordinary = v },
		"Native":   func(n int) int { return n },
	}})
	if err != nil {
		t.Fatal(err)
	}
	pkg := schema.Bind(nil)
	for _, tc := range []struct {
		name, body, want string
		bad              bool
	}{
		{"int", "host.Register(Number)", "Number", false},
		{"string", "host.Register(Text)", "Text", false},
		{"typed", "host.Typed(Empty);host.Void(Empty)", "Empty", false},
		{"method", "host.Register(T{}.Method)", "", true},
		{"typed nil", "var f func(int)int;host.Register(f)", "", true},
		{"nil", "host.Register(nil)", "", false},
		{"nonfunction", "host.Register(42)", "", true},
		{"native", "host.Register(host.Native)", "", true},
		{"closure", "x:=1;host.Register(func()int{return x})", "", true},
		{"ordinary", "host.Ordinary(Number)", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fn = nil
			ordinary = nil
			module, err := evmcompiler.Compile("functions.go", `import "host";type T struct{};func(T)Method(){};func Empty(){};func Number(n int)int{return n};func Text(s string)string{return s};func Run(){`+tc.body+`}`, pkg)
			if err != nil {
				t.Fatal(err)
			}
			program, err := evm.Link(module, pkg)
			if err != nil {
				t.Fatal(err)
			}
			err = evm.New(program).Run(context.Background(), "Run", nil, nil)
			if (err != nil) != tc.bad {
				t.Fatalf("error %v, want failure %v", err, tc.bad)
			}
			if tc.want != "" && (fn == nil || fn.Name != tc.want) {
				t.Fatalf("handle %#v", fn)
			}
			if tc.name == "nil" && fn != nil {
				t.Fatal("nil became nonnil")
			}
			if tc.name == "ordinary" {
				if _, ok := ordinary.(*evm.GuestValue); !ok {
					t.Fatalf("ordinary any converted to %T", ordinary)
				}
			}
		})
	}
}
