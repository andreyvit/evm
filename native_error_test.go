package evm_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"testing/fstest"
	"time"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
)

type nativeTestError int

func (e nativeTestError) Error() string { return fmt.Sprint(int(e)) }

type nativeTestSliceError []int

func (e nativeTestSliceError) Error() string { return "slice" }

type nativeTestMatchError struct{ target error }

func (e nativeTestMatchError) Error() string        { return "match" }
func (e nativeTestMatchError) Is(target error) bool { return target == e.target }

type nativeTestPanicError struct{ N int }

func (e *nativeTestPanicError) Error() string {
	if e.N != 0 {
		panic("boom")
	}
	return "ok"
}

type nativeEmptyError bool

func (e nativeEmptyError) Error() string { return "empty" }
func (e nativeEmptyError) Unwrap() []error {
	if e {
		return []error{}
	}
	return nil
}

type nativeTestNilError struct{}

func (e *nativeTestNilError) Error() string { return "nil pointer" }

func TestNativeErrorHandoff(t *testing.T) {
	sentinel := errors.New("sentinel")
	var typedNil *nativeTestNilError
	native := []error{nil, sentinel, fmt.Errorf("wrapped: %w", sentinel), errors.Join(sentinel, errors.New("other")), nativeTestError(7), typedNil, nativeTestSliceError{1}, nativeTestMatchError{sentinel}, (*nativeTestPanicError)(nil), &nativeTestPanicError{1}, nativeEmptyError(false), nativeEmptyError(true)}
	returned := 0
	pkg := evm.Package{Path: "host", Functions: []evm.ExternalFunction{
		{Name: "AnyGet", Signature: evm.Signature{Params: []*values.Type{values.IntType}, Results: []*values.Type{values.AnyType}}, Call: func(c *evm.VM) error {
			v, err := c.AnyValue(native[c.CallArgument(0).AsInt()])
			if err != nil {
				return err
			}
			c.SetCallResult(0, v)
			return nil
		}},
		{Name: "Get", Signature: evm.Signature{Params: []*values.Type{values.IntType}, Results: []*values.Type{values.ErrorType}}, Call: func(c *evm.VM) error { return c.SetError(0, native[c.CallArgument(0).AsInt()]) }},
		{Name: "Check", Signature: evm.Signature{Params: []*values.Type{values.ErrorType, values.IntType}}, Call: func(c *evm.VM) error {
			x, err := c.NativeAny(c.CallArgument(0))
			if err != nil {
				return err
			}
			i := int(c.CallArgument(1).AsInt())
			if i != 6 && x != native[i] {
				return fmt.Errorf("native identity changed at %d", i)
			}
			if i == 6 {
				if _, ok := x.(nativeTestSliceError); !ok {
					return fmt.Errorf("native slice error changed: %T", x)
				}
			}
			returned++
			return nil
		}},
	}}
	module, err := evmcompiler.Compile("errors.go", `import("host";"errors";"fmt")
 func Run(){
  for i:=0;i<8;i++{host.Check(host.Get(i),i)}
  if host.AnyGet(1).(error)!=host.Get(1){panic("any/error conversion differs")}
  if host.Get(10).(interface{Unwrap() []error}).Unwrap()!=nil || host.Get(11).(interface{Unwrap() []error}).Unwrap()==nil{panic("empty/nil unwrap")}
  if host.Get(0)!=nil || host.Get(5)==nil {panic("nil error")}
  if host.Get(1)!=host.Get(1) || host.Get(4)!=host.Get(4) {panic("error equality")}
  if errors.Is(host.Get(6),host.Get(6)){panic("uncomparable target")}
  if !errors.Is(host.Get(7),host.Get(1)){panic("native custom Is")}
  if !errors.Is(host.Get(2),host.Get(1)) || !errors.Is(host.Get(3),host.Get(1)) {panic("wrapped identity")}
  if errors.Unwrap(host.Get(2))!=host.Get(1) || errors.Unwrap(host.Get(3))!=nil {panic("unwrap")}
  if _,ok:=host.Get(1).(interface{Unwrap() error});ok{panic("unexpected unwrap")}
  multi,ok:=host.Get(3).(interface{Unwrap() []error});if !ok || multi.Unwrap()[0]!=host.Get(1){panic("multi unwrap")}
  if fmt.Sprintf("%v",host.Get(2))!="wrapped: sentinel" || fmt.Sprintf("%T",host.Get(1))!="*errors.errorString"{panic("format")}
  if fmt.Sprintf("%v",host.Get(8))!="<nil>" {panic("typed nil formatting")}
  if fmt.Sprintf("%v",host.Get(9))!="%!v(PANIC=Error method: boom)" {panic("panic formatting")}
  m:=map[error]int{host.Get(1):9};if m[host.Get(1)]!=9{panic("map identity")}
 }
 func PanicNative(){panic(host.Get(1))}
 func DirectPanic()(caught bool){defer func(){caught=recover()=="boom"}();host.Get(9).Error();return}
 func ComparePanic()(caught bool){defer func(){caught=recover()!=nil}();a:=host.Get(6);_ = a==a;return}
 `, pkg)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, pkg)
	if err != nil {
		t.Fatal(err)
	}
	if err := evmtest.New(program).Run(context.Background(), "Run", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := evmtest.New(program).Run(context.Background(), "PanicNative", nil, nil); !errors.Is(err, sentinel) {
		t.Fatal("native panic error identity lost", err)
	}
	var out [1]values.Value
	if err := evmtest.New(program).Run(context.Background(), "ComparePanic", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if out[0].R != 1 {
		t.Fatal("uncomparable native error did not panic")
	}
	if err := evmtest.New(program).Run(context.Background(), "DirectPanic", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if out[0].R != 1 || returned != 8 {
		t.Fatal("incomplete error handoff", out, returned)
	}
}

type retainingNativeError struct {
	target error
	same   bool
}

func (e *retainingNativeError) Error() string { return "matcher" }
func (e *retainingNativeError) Is(target error) bool {
	e.same = e.target != nil && e.target == target
	e.target = target
	return target.Error() == "first"
}

func TestNativeErrorGuestTargetRetention(t *testing.T) {
	native := &retainingNativeError{}
	sentinel := errors.New("retained sentinel")
	var wrapped, joined error
	pkg := evm.Package{Path: "host", Functions: []evm.ExternalFunction{
		{Name: "Sentinel", Signature: evm.Signature{Results: []*values.Type{values.ErrorType}}, Call: func(c *evm.VM) error { return c.SetError(0, sentinel) }},
		{Name: "Keep", Signature: evm.Signature{Params: []*values.Type{values.ErrorType, values.ErrorType}}, Call: func(c *evm.VM) error {
			a, err := c.NativeAny(c.CallArgument(0))
			if err != nil {
				return err
			}
			b, err := c.NativeAny(c.CallArgument(1))
			if err != nil {
				return err
			}
			wrapped = a.(error)
			joined = b.(error)
			return nil
		}},

		{Name: "Invoke", Signature: evm.Signature{Params: []*values.Type{values.ErrorType}}, Call: func(c *evm.VM) error {
			x, err := c.NativeAny(c.CallArgument(0))
			if err != nil {
				return err
			}
			_ = x.(error).Error()
			return nil
		}},

		{Name: "Matcher", Signature: evm.Signature{Results: []*values.Type{values.ErrorType}}, Call: func(c *evm.VM) error { return c.SetError(0, native) }},
		{Name: "Held", Signature: evm.Signature{Results: []*values.Type{values.ErrorType}}, Call: func(c *evm.VM) error { return c.SetError(0, native.target) }},
	}}
	module, err := evmcompiler.Compile("guest_errors.go", `import("host";"errors";"fmt")
 var text string
 var current *Guest
 type ViaNative struct{}
 func(e *ViaNative)Error()string{host.Invoke(&ViaNative{});return "never"}
 func RecursiveNative(){host.Invoke(&ViaNative{})}
 func NativePanic()(caught bool){defer func(){caught=recover()=="guest boom"}();host.Invoke(&Bad{});return}
 type Recurse struct{}
 func(e *Recurse)Error()string{errors.Is(host.Matcher(),&Recurse{});return "first"}
 func Recursive(){errors.Is(host.Matcher(),&Recurse{})}
 type Bad struct{}
 func(e *Bad)Error()string{panic("guest boom")}
 func PanicGuest()(caught bool){defer func(){caught=recover()=="guest boom"}();errors.Is(host.Matcher(),&Bad{});return}
 type Guest struct{message string}
 func(e *Guest)Error()string{return e.message}
 func Run(){
  text="first";g:=&Guest{message:text};current=g
  if !errors.Is(host.Matcher(),current){panic("guest target")}
  if host.Held()!=current{panic("guest roundtrip")}
  if !errors.Is(host.Matcher(),current){panic("repeated guest target")}
  g.message="later"
  host.Keep(fmt.Errorf("wrapped: %w",host.Sentinel()),errors.Join(host.Sentinel(),errors.New("extra")))
 }
 `, pkg)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, pkg)
	if err != nil {
		t.Fatal(err)
	}
	vm := evmtest.New(program)
	scope, err := vm.OpenCallbackScope(program.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer scope.Close()
	if err := vm.Run(context.Background(), "Run", nil, nil); err != nil {
		t.Fatal(err)
	}
	var recovered [1]values.Value
	saved := native.target
	same := native.same
	if err := vm.Run(context.Background(), "PanicGuest", nil, recovered[:]); err != nil {
		t.Fatal(err)
	}
	if recovered[0].R != 1 {
		t.Fatal("guest Error panic was not recovered")
	}
	if err := vm.Run(context.Background(), "NativePanic", nil, recovered[:]); err != nil {
		t.Fatal(err)
	}
	if recovered[0].R != 1 {
		t.Fatal("guest panic through native function was not recovered")
	}
	native.target = saved
	native.same = same
	if !native.same {
		t.Fatal("guest pointer identity changed on native side")
	}
	if !errors.Is(wrapped, sentinel) || errors.Unwrap(wrapped) != sentinel || !errors.Is(joined, sentinel) {
		t.Fatal("retained guest error chain lost native sentinel", wrapped, joined)
	}
	if native.target == nil || native.target.Error() != "later" {
		t.Fatal("retained guest error lost its state")
	}
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	if err := vm.Reset(); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if err, ok := recover().(error); !ok || !errors.Is(err, evm.ErrCallbackExpired) {
				t.Fatalf("expired guest error: %v", err)
			}
		}()
		_ = native.target.Error()
	}()
	depthVM := evmtest.New(program)
	depthVM.Limits.Depth = 8
	if err := depthVM.Run(context.Background(), "Recursive", nil, nil); !errors.Is(err, evm.ErrDepth) {
		t.Fatal("native/guest recursion bypassed depth limit", err)
	}
	depthVM = evmtest.New(program)
	depthVM.Limits.Depth = 8
	if err := depthVM.Run(context.Background(), "RecursiveNative", nil, nil); !errors.Is(err, evm.ErrDepth) {
		t.Fatal("generic native recursion bypassed depth limit", err)
	}
}

type NativeErrorBox struct{ Value any }

func TestNativeErrorFieldRetention(t *testing.T) {
	schema, err := evmbind.New("box", fstest.MapFS{"box.go": {Data: []byte("package box; type NativeErrorBox struct { Value any }")}}, evmbind.Options{Selective: true, Types: []reflect.Type{reflect.TypeFor[NativeErrorBox]()}})
	if err != nil {
		t.Fatal(err)
	}
	box := &NativeErrorBox{}
	host := evm.Package{Path: "host", Functions: []evm.ExternalFunction{
		{Name: "Box", Signature: evm.Signature{Results: []*values.Type{schema.NativeType(reflect.TypeFor[*NativeErrorBox]())}}, Call: func(c *evm.VM) error { v, err := schema.NativePointer(box); c.SetCallResult(0, v); return err }},
		{Name: "Read", Signature: evm.Signature{Results: []*values.Type{values.StringType}}, Call: func(c *evm.VM) error { c.SetCallResult(0, values.String(box.Value.(error).Error())); return nil }},
	}}
	module, err := evmcompiler.Compile("box.go", `import "host"
 type Guest struct{}
 func (*Guest)Error()string{return "retained"}
 func Store(){host.Box().Value=&Guest{}}
 func Read()string{return host.Read()}
 `, schema.Package(), host)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, schema.Package(), host)
	if err != nil {
		t.Fatal(err)
	}
	vm := evmtest.New(program)
	scope, err := vm.OpenCallbackScope(program.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer scope.Close()
	if err := vm.Run(context.Background(), "Store", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := box.Value.(error).Error(); got != "retained" {
		t.Fatal(got)
	}
	var out [1]values.Value
	if err := vm.Run(context.Background(), "Read", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if got := out[0].AsString(); got != "retained" {
		t.Fatal(got)
	}
	if got := box.Value.(error).Error(); got != "retained" {
		t.Fatal(got)
	}
}

func TestOpenCallbackScopePerCallBudget(t *testing.T) {
	module, err := evmcompiler.Compile("scope_budget.go", `
type LoopError struct{ N int }
func (e *LoopError) Error() string {
	sum := 0
	for i := 0; i < e.N; i++ {
		sum += i
	}
	if sum < 0 {
		return "neg"
	}
	return "ok"
}
func Make(n int) error { return &LoopError{N: n} }
func Fast() int { return 7 }
`)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module)
	if err != nil {
		t.Fatal(err)
	}
	makeFn, err := program.LookupFunction("Make", &evm.Signature{
		Params:  []*values.Type{values.IntType},
		Results: []*values.Type{values.ErrorType},
	})
	if err != nil {
		t.Fatal(err)
	}
	fastFn, err := program.LookupFunction("Fast", &evm.Signature{
		Results: []*values.Type{values.IntType},
	})
	if err != nil {
		t.Fatal(err)
	}

	vm := program.AcquireVM(context.Background())
	if _, err := vm.OpenCallbackScope(evm.Limits{Duration: -time.Second}); err == nil {
		vm.Release()
		t.Fatal("expected invalid limits error from OpenCallbackScope")
	}

	// Small per-call budget: enough for N=5 (per call), not enough for N=100.
	scope, err := vm.OpenCallbackScope(evm.Limits{Steps: 60})
	if err != nil {
		vm.Release()
		t.Fatal(err)
	}
	defer scope.Close()

	// Run Make(5) and Make(100) using vm's own (larger) budget.
	vm.SetBudget(evm.Limits{Steps: 1000})
	slotsSmall := [2]values.Value{values.Int(5)}
	if err := vm.Run(makeFn, slotsSmall[:]); err != nil {
		vm.Release()
		t.Fatal(err)
	}
	smallErr, err := vm.NativeError(slotsSmall[1])
	if err != nil {
		vm.Release()
		t.Fatal(err)
	}
	slotsLarge := [2]values.Value{values.Int(100)}
	if err := vm.Run(makeFn, slotsLarge[:]); err != nil {
		vm.Release()
		t.Fatal(err)
	}
	largeErr, err := vm.NativeError(slotsLarge[1])
	if err != nil {
		vm.Release()
		t.Fatal(err)
	}

	// While vm is idle: each call gets a fresh 60-step budget (three calls succeed
	// even though 3*cost > 60), while largeErr exceeds the 60-step per-call budget.
	for i := 0; i < 3; i++ {
		if got := smallErr.Error(); got != "ok" {
			vm.Release()
			t.Fatalf("idle call %d: got %q", i, got)
		}
	}
	func() {
		defer func() {
			err, ok := recover().(error)
			if !ok || !errors.Is(err, evm.ErrSteps) {
				t.Fatalf("expected ErrSteps on idle callback, got %v", err)
			}
		}()
		_ = largeErr.Error()
	}()

	// Originating idle VM is still usable with its own budget untouched.
	var fastOut [1]values.Value
	if err := vm.Run(fastFn, fastOut[:]); err != nil || fastOut[0].AsInt() != 7 {
		vm.Release()
		t.Fatalf("originating VM after idle callback: %v, %v", fastOut, err)
	}

	// After vm.Release(): same per-call budget applies.
	vm.Release()
	for i := 0; i < 3; i++ {
		if got := smallErr.Error(); got != "ok" {
			t.Fatalf("post-release call %d: got %q", i, got)
		}
	}
	func() {
		defer func() {
			err, ok := recover().(error)
			if !ok || !errors.Is(err, evm.ErrSteps) {
				t.Fatalf("expected ErrSteps on post-release callback, got %v", err)
			}
		}()
		_ = largeErr.Error()
	}()
}
