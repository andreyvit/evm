package evmcompiler_test

import (
	"context"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

func TestRetainedFunctionNativeEntry(t *testing.T) {
	module, err := evmcompiler.Compile("entry.go", `
 func Make()func(int)any { n:=40;return func(x int)any{n+=x;return n} }
 func Spin()func(){return func(){for{}}}
 `)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(module)
	if err != nil {
		t.Fatal(err)
	}
	result := make([]values.Value, 1)
	if err := evm.New(p).Run(context.Background(), "Make", nil, result); err != nil {
		t.Fatal(err)
	}
	fn, err := p.FunctionValue(result[0])
	if err != nil {
		t.Fatal(err)
	}
	output := []any{"unchanged"}
	for _, want := range []int{42, 44} {
		if err := evm.New(p).RunFunctionNative(context.Background(), fn, []any{2}, output, evm.Budget{Steps: 10000}); err != nil {
			t.Fatal(err)
		}
		if output[0] != want {
			t.Fatalf("result: %#v", output)
		}
	}
	if err := evm.New(p).RunFunctionNative(context.Background(), fn, []any{"wrong"}, output, evm.Budget{}); err == nil {
		t.Fatal("accepted wrong argument type")
	}
	if output[0] != 44 {
		t.Fatal("failed call changed output")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := evm.New(p).RunFunctionNative(ctx, fn, []any{2}, output, evm.Budget{}); err == nil {
		t.Fatal("ignored cancellation")
	}
	if err := evm.New(p).RunFunctionNative(context.Background(), fn, []any{2}, output, evm.Budget{AllocationBytes: 1}); err == nil {
		t.Fatal("ignored conversion allocation limit")
	}
	if err := evm.New(p).Run(context.Background(), "Spin", nil, result); err != nil {
		t.Fatal(err)
	}
	spin, err := p.FunctionValue(result[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(p).RunFunctionNative(context.Background(), spin, nil, nil, evm.Budget{Steps: 100}); err == nil {
		t.Fatal("ignored execution budget")
	}

}

func TestRetainedFunctionNativeInterfaces(t *testing.T) {
	module, err := evmcompiler.Compile("interfaces.go", `
 type Named interface{Name()string}
 type Thing struct{}
 func(*Thing)Name()string{return "thing"}
 func New()any{return &Thing{}}
 func Echo()func(Named)Named{return func(v Named)Named{return v}}
 func NoError()func()error{return func()error{return nil}}
 type Failure struct{}
 func(*Failure)Error()string{return "failed"}
 func FailureValue()any{return &Failure{}}
 func EchoError()func(error)error{return func(err error)error{return err}}
 func SameError()func(error,error)any{return func(a,b error)any{return a==b}}
 `)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(module)
	if err != nil {
		t.Fatal(err)
	}
	var result [1]values.Value
	if err := evm.New(p).Run(context.Background(), "New", nil, result[:]); err != nil {
		t.Fatal(err)
	}
	guest := &evm.GuestValue{Program: p, Value: result[0]}
	if err := evm.New(p).Run(context.Background(), "Echo", nil, result[:]); err != nil {
		t.Fatal(err)
	}
	echo, err := p.FunctionValue(result[0])
	if err != nil {
		t.Fatal(err)
	}
	output := []any{"unchanged"}
	if err := evm.New(p).RunFunctionNative(context.Background(), echo, []any{guest}, output, evm.Budget{}); err != nil {
		t.Fatal(err)
	}
	back, ok := output[0].(*evm.GuestValue)
	if !ok || back.Program != p || back.Value != guest.Value {
		t.Fatal("interface identity lost")
	}
	if err := evm.New(p).RunFunctionNative(context.Background(), echo, []any{nil}, output, evm.Budget{}); err != nil {
		t.Fatal(err)
	}
	if output[0] != nil {
		t.Fatal("nil interface changed")
	}
	if err := evm.New(p).RunFunctionNative(context.Background(), echo, []any{3}, output, evm.Budget{}); err == nil {
		t.Fatal("accepted nonimplementing value")
	}
	if err := evm.New(p).Run(context.Background(), "NoError", nil, result[:]); err != nil {
		t.Fatal(err)
	}
	noError, err := p.FunctionValue(result[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(p).RunFunctionNative(context.Background(), noError, nil, output, evm.Budget{}); err != nil {
		t.Fatal(err)
	}
	if output[0] != nil {
		t.Fatal("nil error changed")
	}
	if err := evm.New(p).Run(context.Background(), "FailureValue", nil, result[:]); err != nil {
		t.Fatal(err)
	}
	failure := &evm.GuestValue{Program: p, Value: result[0]}
	if err := evm.New(p).Run(context.Background(), "EchoError", nil, result[:]); err != nil {
		t.Fatal(err)
	}
	echoError, err := p.FunctionValue(result[0])
	if err != nil {
		t.Fatal(err)
	}
	nativeVM := evm.New(p)
	scope, err := nativeVM.OpenCallbackScope(context.Background(), evm.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	defer scope.Close()
	if err := nativeVM.RunFunctionNative(context.Background(), echoError, []any{failure}, output, evm.Budget{}); err != nil {
		t.Fatal(err)
	}
	nativeError, ok := output[0].(error)
	if !ok || nativeError.Error() != "failed" {
		t.Fatal("guest error did not become a callable native error")
	}
	if err := evm.New(p).Run(context.Background(), "SameError", nil, result[:]); err != nil {
		t.Fatal(err)
	}
	sameError, err := p.FunctionValue(result[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := evm.New(p).RunFunctionNative(context.Background(), sameError, []any{failure, nativeError}, output, evm.Budget{}); err != nil {
		t.Fatal(err)
	}
	if output[0] != true {
		t.Fatal("error interface identity lost")
	}
	if err := evm.New(p).RunFunctionNative(context.Background(), echoError, []any{guest}, output, evm.Budget{}); err == nil {
		t.Fatal("accepted value without Error method")
	}
}
