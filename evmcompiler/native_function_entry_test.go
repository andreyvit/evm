package evmcompiler_test

import (
	"fmt"

	"context"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
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
	if err := evmtest.New(p).Run(context.Background(), "Make", nil, result); err != nil {
		t.Fatal(err)
	}
	fnValue, err := p.Callable(result[0])
	fn := &fnValue
	if err != nil {
		t.Fatal(err)
	}
	output := []any{"unchanged"}
	for _, want := range []int{42, 44} {
		if err := callWithGoValues(evmtest.New(p), context.Background(), fn, []any{2}, output, evm.Budget{Steps: 10000}); err != nil {
			t.Fatal(err)
		}
		if output[0] != want {
			t.Fatalf("result: %#v", output)
		}
	}
	if err := callWithGoValues(evmtest.New(p), context.Background(), fn, []any{"wrong"}, output, evm.Budget{}); err == nil {
		t.Fatal("accepted wrong argument type")
	}
	if output[0] != 44 {
		t.Fatal("failed call changed output")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := callWithGoValues(evmtest.New(p), ctx, fn, []any{2}, output, evm.Budget{}); err == nil {
		t.Fatal("ignored cancellation")
	}
	if err := callWithGoValues(evmtest.New(p), context.Background(), fn, []any{2}, output, evm.Budget{AllocationBytes: 1}); err == nil {
		t.Fatal("ignored conversion allocation limit")
	}
	if err := evmtest.New(p).Run(context.Background(), "Spin", nil, result); err != nil {
		t.Fatal(err)
	}
	spinValue, err := p.Callable(result[0])
	spin := &spinValue
	if err != nil {
		t.Fatal(err)
	}
	if err := callWithGoValues(evmtest.New(p), context.Background(), spin, nil, nil, evm.Budget{Steps: 100}); err == nil {
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
	if err := evmtest.New(p).Run(context.Background(), "New", nil, result[:]); err != nil {
		t.Fatal(err)
	}
	guest := &evm.GuestValue{Program: p, Value: result[0]}
	if err := evmtest.New(p).Run(context.Background(), "Echo", nil, result[:]); err != nil {
		t.Fatal(err)
	}
	echoValue, err := p.Callable(result[0])
	echo := &echoValue
	if err != nil {
		t.Fatal(err)
	}
	output := []any{"unchanged"}
	if err := callWithGoValues(evmtest.New(p), context.Background(), echo, []any{guest}, output, evm.Budget{}); err != nil {
		t.Fatal(err)
	}
	back, ok := output[0].(*evm.GuestValue)
	if !ok || back.Program != p || back.Value != guest.Value {
		t.Fatal("interface identity lost")
	}
	if err := callWithGoValues(evmtest.New(p), context.Background(), echo, []any{nil}, output, evm.Budget{}); err != nil {
		t.Fatal(err)
	}
	if output[0] != nil {
		t.Fatal("nil interface changed")
	}
	if err := callWithGoValues(evmtest.New(p), context.Background(), echo, []any{3}, output, evm.Budget{}); err == nil {
		t.Fatal("accepted nonimplementing value")
	}
	if err := evmtest.New(p).Run(context.Background(), "NoError", nil, result[:]); err != nil {
		t.Fatal(err)
	}
	noErrorValue, err := p.Callable(result[0])
	noError := &noErrorValue
	if err != nil {
		t.Fatal(err)
	}
	if err := callWithGoValues(evmtest.New(p), context.Background(), noError, nil, output, evm.Budget{}); err != nil {
		t.Fatal(err)
	}
	if output[0] != nil {
		t.Fatal("nil error changed")
	}
	if err := evmtest.New(p).Run(context.Background(), "FailureValue", nil, result[:]); err != nil {
		t.Fatal(err)
	}
	failure := &evm.GuestValue{Program: p, Value: result[0]}
	if err := evmtest.New(p).Run(context.Background(), "EchoError", nil, result[:]); err != nil {
		t.Fatal(err)
	}
	echoErrorValue, err := p.Callable(result[0])
	echoError := &echoErrorValue
	if err != nil {
		t.Fatal(err)
	}
	nativeVM := evmtest.New(p)
	scope, err := nativeVM.OpenCallbackScope()
	if err != nil {
		t.Fatal(err)
	}
	defer scope.Close()
	if err := callWithGoValues(nativeVM, context.Background(), echoError, []any{failure}, output, evm.Budget{}); err != nil {
		t.Fatal(err)
	}
	nativeError, ok := output[0].(error)
	if !ok || nativeError.Error() != "failed" {
		t.Fatal("guest error did not become a callable native error")
	}
	if err := evmtest.New(p).Run(context.Background(), "SameError", nil, result[:]); err != nil {
		t.Fatal(err)
	}
	sameErrorValue, err := p.Callable(result[0])
	sameError := &sameErrorValue
	if err != nil {
		t.Fatal(err)
	}
	if err := callWithGoValues(evmtest.New(p), context.Background(), sameError, []any{failure, nativeError}, output, evm.Budget{}); err != nil {
		t.Fatal(err)
	}
	if output[0] != true {
		t.Fatal("error interface identity lost")
	}
	if err := callWithGoValues(evmtest.New(p), context.Background(), echoError, []any{guest}, output, evm.Budget{}); err == nil {
		t.Fatal("accepted value without Error method")
	}
}

func callWithGoValues(s *evmtest.Session, ctx context.Context, fn *evm.Callable, args, out []any, l evm.Limits) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if fn == nil {
		return evm.ErrNilFunction
	}
	s.SetBudget(l)
	sig := fn.Signature()
	if len(out) != len(sig.Results) {
		return fmt.Errorf("wrong result count")
	}
	in, err := s.NativeArguments(sig.Params, args)
	if err != nil {
		return err
	}
	slots := append(in, make([]values.Value, len(out))...)
	if err := s.VM.Run(*fn, slots); err != nil {
		return err
	}
	results := make([]any, len(out))
	for i, t := range sig.Results {
		v := slots[len(in)+i]
		shape := s.Program().Shape(t)
		if t != values.AnyType && (shape == nil || shape.Kind != values.InterfaceKind) {
			switch t {
			case values.Int64:
				results[i] = v.AsInt()
				continue
			case values.StringType:
				results[i] = v.AsString()
				continue
			case values.BoolType:
				results[i] = v.AsBool()
				continue
			case values.IntType:
				results[i] = int(v.AsInt())
				continue
			}
			v, err = s.Program().PackInterface(t, v)
			if err != nil {
				return err
			}
		}
		results[i], err = s.NativeAny(v)
		if err != nil {
			return err
		}
	}
	copy(out, results)
	return nil
}
