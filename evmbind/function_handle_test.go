//go:build !evmreflect

package evmbind_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/functionbindings"
	"github.com/andreyvit/evm/evmbind/internal/functionfixture"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
)

type functionHost struct {
	nativeBox                 functionfixture.NativeBox
	slot                      functionfixture.FunctionSlot
	number, text, value, rows *evm.Callable
	retained                  *evm.Callable
}

func (h *functionHost) NativeSlot(_ *evm.VM) *functionfixture.NativeBox { return &h.nativeBox }
func (h *functionHost) EchoNative(_ *evm.VM, fn functionfixture.NativeUnary) functionfixture.NativeUnary {
	return fn
}

func (h *functionHost) Keep(_ *evm.VM, fn *evm.FunctionOf[func(int) int])       { h.retained = fn }
func (h *functionHost) KeepAny(_ *evm.VM, fn *evm.FunctionOf[any])              { h.retained = fn }
func (h *functionHost) Get(_ *evm.VM) *evm.FunctionOf[func(int) int]            { return h.retained }
func (h *functionHost) GetWrong(_ *evm.VM) *evm.FunctionOf[func(string) string] { return h.retained }

func (h *functionHost) Slot(_ *evm.VM) *functionfixture.FunctionSlot { return &h.slot }
func (h *functionHost) EchoSlot(_ *evm.VM, s functionfixture.FunctionSlot) functionfixture.FunctionSlot {
	return s
}

func (h *functionHost) Register(_ *evm.VM, fn *evm.FunctionOf[func(int) int]) { h.number = fn }
func (h *functionHost) RegisterText(_ *evm.VM, fn *evm.FunctionOf[func(string) string]) {
	h.text = fn
}

func (h *functionHost) RegisterValue(_ *evm.VM, fn *evm.FunctionOf[func(int) any]) {
	h.value = fn
}

func (h *functionHost) Strings(_ *evm.VM, values []string) {}

func (h *functionHost) RegisterRows(_ *evm.VM, fn *evm.FunctionOf[func(func([]string), func([]any))]) {
	h.rows = fn
}

func TestTypedFunctionBindings(t *testing.T) {
	files := os.DirFS("internal/functionfixture")
	generated, err := evmbind.Generate("callbacks", "github.com/andreyvit/evm/evmbind/internal/functionfixture", "functionbindings", files, evmbind.Options{})
	if err != nil {
		t.Fatal(err)
	}
	existing, err := os.ReadFile("internal/functionbindings/bindings_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, existing) {
		t.Fatal("regenerate function bindings")
	}
	for _, static := range []bool{false, true} {
		opt := evmbind.Options{Host: reflect.TypeFor[*functionHost](), Types: []reflect.Type{reflect.TypeFor[functionfixture.FunctionSlot]()}}
		if static {
			opt.Mode = evmbind.StaticOnly
			opt.Static = &functionbindings.Bindings
		}
		schema, err := evmbind.New("callbacks", files, opt)
		if err != nil {
			t.Fatal(err)
		}
		module, err := evmcompiler.Compile("functions.go", `import "callbacks"
func BorrowedArguments() bool {
	box := callbacks.NativeSlot()
	data := []byte{1,2,3}
	var saved []byte
	box.Visit(data, func(b []byte) { saved = b[1:] })
	data[1] = 9
	if saved[0] != 2 { return false }
	var written byte
	box.Bytes(data)(func(b []byte) bool { a:=b[1:]; a[0]++; written=b[1]; return false })
	box.Visit(data, box.Writer())
	return written == 10 && data[1] == 9 && data[0] == 1
}
func Twice(n int)int{return n*2}
func Text(s string)string{return s+"!"}
func Value(n int)any{return n}
func Register(){callbacks.Strings([]string{"shared"});callbacks.Register(Twice);callbacks.RegisterText(Text);callbacks.RegisterValue(Value);callbacks.RegisterRows(Rows)}
func Rows(headers func([]string), yield func([]any)){headers([]string{"name","number"}); yield([]any{"hello",uint64(9007199254740993)})}
func Clear(){callbacks.Register(nil);callbacks.RegisterText(nil)}
func Keep(){n:=40;callbacks.Keep(func(x int)int{n+=x;return n})}
func KeepAny(){n:=40;callbacks.KeepAny(func(x int)int{n+=x;return n})}
func BadAny(){callbacks.KeepAny(42)}
func NilAny(){var f func(int)int;callbacks.KeepAny(f)}
func Use()int{return callbacks.Get()(2)}
func ClearRetained()bool{callbacks.Keep(nil);return callbacks.Get()==nil}
func WrongResult(){callbacks.GetWrong()}
func Field()int{
 s:=callbacks.Slot()
 n:=10
 s.Fn=func(x int)int{n+=x;return n}
 a:=s.Fn(2)
 c:=*s
 b:=callbacks.EchoSlot(c).Fn(3)
 p:=&s.Fn
 *p=func(x int)int{return x+100}
 return a+b+(*p)(4)
}
func ClearField()bool{s:=callbacks.Slot();s.Fn=nil;return s.Fn==nil}
func ReadField()int{return callbacks.Slot().Fn(1)}
func NativeField()int {
 s:=callbacks.NativeSlot();n:=20
 s.Apply=func(x int)int{n+=x;return n}
 return callbacks.EchoNative(s.Apply)(2)
}
func ReadNative()int{return callbacks.NativeSlot().Apply(1)}


`, schema.Package())
		if err != nil {
			t.Fatal(err)
		}
		host := &functionHost{}
		program, err := evm.Link(module, schema.Bind(host))
		if err != nil {
			t.Fatal(err)
		}
		var borrowed [1]values.Value
		if err := evmtest.New(program).Run(t.Context(), "BorrowedArguments", nil, borrowed[:]); err != nil || borrowed[0].R != 1 {
			t.Fatalf("borrowed callback arguments (static=%v): result %v, error %v", static, borrowed, err)
		}
		if err := evmtest.New(program).Run(context.Background(), "Register", nil, nil); err != nil {
			t.Fatal(err)
		}
		if host.rows == nil {
			t.Fatal("missing row callback")
		}
		var gotHeaders []string
		var gotNumber uint64
		var gotText string
		var args []values.Value
		for i, callback := range []evm.Native{
			func(c *evm.VM) error {
				v := c.CallArgument(0)
				shape := program.Shape(host.rows.Signature().Params[0]).Params[0]
				for i := uint64(0); i < uint64(v.SliceLen()); i++ {
					cell, err := c.SliceElement(shape, v, uint64(i))
					if err != nil {
						return err
					}
					gotHeaders = append(gotHeaders, cell.AsString())
				}
				return nil
			},
			func(c *evm.VM) error {
				v := c.CallArgument(0)
				shape := program.Shape(host.rows.Signature().Params[1]).Params[0]
				var cells [2]values.Value
				for i := range cells {
					cell, err := c.SliceElement(shape, v, uint64(i))
					if err != nil {
						return err
					}
					cells[i] = cell
				}
				if v.SliceLen() != 2 {
					t.Fatalf("row width: %d", len(cells))
				}
				_, text, err := program.UnpackInterface(cells[0])
				if err != nil {
					return err
				}
				gotText = text.AsString()
				shape, number, err := program.UnpackInterface(cells[1])
				if err != nil {
					return err
				}
				if shape.Type != values.Uint64 {
					t.Fatalf("number type: %s", shape.Type.Name())
				}
				gotNumber = number.R
				return nil
			},
		} {
			shape := program.Shape(host.rows.Signature().Params[i])
			typ, err := evm.NewNativeFunctionType(evm.Signature{Params: []*values.Type{shape.Params[0].Type}})
			if err != nil {
				t.Fatal(err)
			}
			v, err := program.NativeFunction(typ, callback)
			if err != nil {
				t.Fatal(err)
			}
			args = append(args, v)
		}
		if err := evmtest.New(program).Run(context.Background(), host.rows.Name(), args, nil); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gotHeaders, []string{"name", "number"}) || gotText != "hello" || gotNumber != 9007199254740993 {
			t.Fatalf("rows: %v %q %d", gotHeaders, gotText, gotNumber)
		}
		var result [1]values.Value
		if host.number == nil || host.text == nil {
			t.Fatal("missing retained handles")
		}
		if err := evmtest.New(program).Run(context.Background(), host.number.Name(), []values.Value{values.Int(21)}, result[:]); err != nil {
			t.Fatal(err)
		}
		if result[0].AsInt() != 42 {
			t.Fatal(result[0])
		}
		if err := evmtest.New(program).Run(context.Background(), host.text.Name(), []values.Value{values.String("hello")}, result[:]); err != nil {
			t.Fatal(err)
		}
		if result[0].AsString() != "hello!" {
			t.Fatal(result[0])
		}
		if host.value == nil {
			t.Fatal("missing any-result callable")
		}
		if err := evmtest.New(program).Run(context.Background(), host.value.Name(), []values.Value{values.Int(42)}, result[:]); err != nil {
			t.Fatal(err)
		}
		shape, value, err := program.UnpackInterface(result[0])
		if err != nil || shape.Type != values.IntType || value.AsInt() != 42 {
			t.Fatalf("any result: %v %v %v", shape, value, err)
		}
		if err := evmtest.New(program).Run(context.Background(), "Clear", nil, nil); err != nil {
			t.Fatal(err)
		}
		if host.number != nil || host.text != nil {
			t.Fatal("nil callback did not clear handles")
		}
		if err := evmtest.New(program).Run(context.Background(), "BadAny", nil, nil); err == nil {
			t.Fatal("non-function accepted")
		}
		if err := evmtest.New(program).Run(context.Background(), "NilAny", nil, nil); err != nil || host.retained != nil {
			t.Fatalf("typed nil: %v", err)
		}
		if err := evmtest.New(program).Run(context.Background(), "KeepAny", nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := evmtest.New(program).Run(context.Background(), "Use", nil, result[:]); err != nil || result[0].AsInt() != 42 {
			t.Fatalf("dynamic closure: %v %v", result, err)
		}
		if err := evmtest.New(program).Run(context.Background(), "Keep", nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := evmtest.New(program).Run(context.Background(), "Use", nil, result[:]); err != nil || result[0].AsInt() != 42 {
			t.Fatalf("returned closure: %v %v", result, err)
		}
		if err := evmtest.New(program).Run(context.Background(), "WrongResult", nil, nil); err == nil {
			t.Fatal("mismatched result signature accepted")
		}
		foreign, err := evm.Link(module, schema.Bind(host))
		if err != nil {
			t.Fatal(err)
		}
		if err := evmtest.New(foreign).Run(context.Background(), "Use", nil, result[:]); err == nil {
			t.Fatal("foreign handle accepted")
		}
		if err := evmtest.New(program).Run(context.Background(), "ClearRetained", nil, result[:]); err != nil || result[0].R != 1 {
			t.Fatalf("nil retained function: %v %v", result, err)
		}
		if err := evmtest.New(program).Run(context.Background(), "Field", nil, result[:]); err != nil || result[0].AsInt() != 131 {
			t.Fatalf("native field: %v %v", result, err)
		}
		if err := evmtest.New(foreign).Run(context.Background(), "ReadField", nil, result[:]); err == nil {
			t.Fatal("foreign field accepted")
		}
		if err := evmtest.New(program).Run(context.Background(), "ClearField", nil, result[:]); err != nil || result[0].R != 1 || host.slot.Fn != nil {
			t.Fatalf("clear native field: %v %v", result, err)
		}
		nativeVM := evmtest.New(program)
		if err := nativeVM.Run(context.Background(), "NativeField", nil, result[:]); err != nil || result[0].AsInt() != 22 {
			t.Fatalf("native function field: %v %v", result, err)
		}
		if err := nativeVM.RunNative(context.Background(), func(*evm.VM) error {
			if got := host.nativeBox.Apply(3); got != 25 {
				t.Fatalf("native retained call: %d", got)
			}
			return nil
		}, evm.Budget{}); err != nil {
			t.Fatal(err)
		}
		if err := evmtest.New(foreign).Run(context.Background(), "ReadNative", nil, result[:]); !errors.Is(err, evm.ErrCallbackInactive) {
			t.Fatalf("foreign callback: %v", err)
		}

		if _, err := evmcompiler.Compile("wrong_rows.go", `import "callbacks";func Wrong(h func([]string), rows func([]string)){};func Run(){callbacks.RegisterRows(Wrong)}`, schema.Package()); err == nil {
			t.Fatal("incompatible row callback compiled")
		}
		if _, err := evmcompiler.Compile("wrong.go", `import "callbacks";func Wrong(s string)int{return 0};func Run(){callbacks.Register(Wrong)}`, schema.Package()); err == nil {
			t.Fatal("wrong callback compiled")
		}
	}
}
