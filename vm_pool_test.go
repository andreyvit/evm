package evm_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

func TestAcquiredNestedVM(t *testing.T) {
	var outer *evm.VM
	var child, explode evm.Callable
	sig := evm.Signature{Params: []*values.Type{values.StringType}, Results: []*values.Type{values.StringType}}
	host := evm.Package{Path: "host", Functions: []evm.ExternalFunction{
		{Name: "Check", Call: func(vm *evm.VM) error {
			if vm != outer {
				t.Fatal("nested binding got another VM")
			}
			return nil
		}},
		{Name: "Explode", Call: func(*evm.VM) error { panic("host failed") }},
		{Name: "Visit", Signature: sig, Call: func(vm *evm.VM) error {
			if vm != outer {
				t.Fatal("binding got another VM")
			}
			arg := vm.CallArgument(0)
			slots := [2]values.Value{arg}
			func() {
				defer func() {
					if recover() != "host failed" {
						t.Fatal("missing panic")
					}
				}()
				_ = vm.Run(explode, nil)
			}()
			if err := vm.Run(child, slots[:]); err != nil {
				return err
			}
			if vm.CallArgument(0) != arg {
				t.Fatal("outer register window changed")
			}
			vm.SetCallResult(0, slots[1])
			return nil
		}},
	}}
	m, err := evmcompiler.Compile("reentry.go", `import "host"
 var n=40
 func descend(i int,s string)string{ if i==0 {host.Check();return s};return descend(i-1,s)}
 func Child(s string)string{n++;return descend(80,s)}
 func Explode(){host.Explode()}
 func Root(s string)(string,int){n++;r:=host.Visit(s);return r,n}`, host)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, host)
	if err != nil {
		t.Fatal(err)
	}
	child, err = p.LookupFunction("Child", &sig)
	if err != nil {
		t.Fatal(err)
	}
	explode, err = p.LookupFunction("Explode", &evm.Signature{})
	if err != nil {
		t.Fatal(err)
	}
	root, err := p.LookupFunction("Root", nil)
	if err != nil {
		t.Fatal(err)
	}
	outer = p.AcquireVM(t.Context())
	defer outer.Release()
	for range 2 {
		slots := [3]values.Value{values.String("outer")}
		if err := outer.Run(root, slots[:]); err != nil || slots[1].AsString() != "outer" || slots[2].R != 42 {
			t.Fatalf("slots %v: %v", slots, err)
		}
	}
}

func TestLeasedBudget(t *testing.T) {
	m := &evm.Module{Functions: []evm.Function{{Name: "Id", Signature: evm.Signature{Params: []*values.Type{values.Int64}, Results: []*values.Type{values.Int64}}, RCount: 1, Code: []evm.Instruction{evm.ABC(evm.OpReturn, 0, 0, 0)}}}}
	limits := evm.Limits{Steps: 2}
	p, err := (evm.LinkOptions{DefaultLimits: &limits}).Link(m)
	if err != nil {
		t.Fatal(err)
	}
	fn, err := p.LookupFunction("Id", nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := evm.Link(m)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := other.LookupFunction("Id", nil)
	if err != nil {
		t.Fatal(err)
	}
	limits.Steps = 500
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			vm := p.AcquireVM(t.Context())
			defer vm.Release()
			slots := [2]values.Value{values.Int(31)}
			if err := vm.Run(foreign, slots[:]); !errors.Is(err, evm.ErrFunction) {
				t.Error(err)
			}
			for range 2 {
				if err := vm.Run(fn, slots[:]); err != nil || slots[1].R != 31 {
					t.Error(slots, err)
					return
				}
			}
			slots[1] = values.Int(99)
			if err := vm.Run(fn, slots[:]); !errors.Is(err, evm.ErrSteps) || slots[1].R != 99 {
				t.Error(slots, err)
			}
			vm.SetBudget(evm.Limits{})
			if err := vm.Run(fn, slots[:]); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	vm := p.AcquireVM(t.Context())
	defer vm.Release()
	vm.SetBudget(evm.Limits{Duration: time.Nanosecond})
	time.Sleep(time.Millisecond)
	if err := vm.Run(fn, make([]values.Value, 2)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
func BenchmarkLeasedFilter(b *testing.B) {
	m, err := evmcompiler.Compile("filter.go", `func Match(n int64,rate float64,vip bool,tag string)(int64,bool){return int64(float64(n)*rate),vip && tag=="ok"}`)
	if err != nil {
		b.Fatal(err)
	}
	p, err := (evm.LinkOptions{DefaultLimits: &evm.Limits{}}).Link(m)
	if err != nil {
		b.Fatal(err)
	}
	fn, err := p.LookupFunction("Match", nil)
	if err != nil {
		b.Fatal(err)
	}
	slots := [6]values.Value{values.Int(31), values.Float(2), values.Bool(true), values.String("ok")}
	b.Run("batch", func(b *testing.B) {
		vm := p.AcquireVM(b.Context())
		defer vm.Release()
		b.ReportAllocs()
		for b.Loop() {
			if err := vm.Run(fn, slots[:]); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("acquire", func(b *testing.B) {
		ctx := b.Context()
		b.ReportAllocs()
		for b.Loop() {
			vm := p.AcquireVM(ctx)
			err := vm.Run(fn, slots[:])
			vm.Release()
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

func TestNativeRootDepth(t *testing.T) {
	p, err := evm.Link(&evm.Module{Functions: []evm.Function{{Name: "Entry", Code: []evm.Instruction{evm.ABC(evm.OpReturn, 0, 0, 0)}}}})
	if err != nil {
		t.Fatal(err)
	}
	typ, err := evm.NewNativeFunctionType(evm.Signature{})
	if err != nil {
		t.Fatal(err)
	}
	var fn evm.Callable
	v, err := p.NativeFunction(typ, func(vm *evm.VM) error {
		if err := vm.Run(fn, nil); !errors.Is(err, evm.ErrDepth) {
			t.Fatalf("nested native call: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fn, err = p.Callable(v)
	if err != nil {
		t.Fatal(err)
	}
	vm := p.AcquireVM(t.Context())
	defer vm.Release()
	vm.SetBudget(evm.Limits{Depth: 1})
	if err := vm.Run(fn, nil); err != nil {
		t.Fatalf("root native call: %v", err)
	}
}

func TestPackGuestCollections(t *testing.T) {
	m, err := evmcompiler.Compile("tags.go", `type Row struct { Tags map[string][]string; Counts [2]int }
	func Tags(r Row) string { return r.Tags["ada"][r.Counts[1]] }
	func Keep(tags []string) []string {return tags}`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m)
	if err != nil {
		t.Fatal(err)
	}
	fn, err := p.LookupFunction("Tags", nil)
	if err != nil {
		t.Fatal(err)
	}
	vm := p.AcquireVM(t.Context())
	defer vm.Release()
	row := p.Shape(fn.Signature().Params[0])
	tags, counts := row.Fields[0].Shape, row.Fields[1].Shape
	items, err := vm.PackAggregate(tags.Elem.Type, values.String("vip"), values.String("early"))
	if err != nil {
		t.Fatal(err)
	}
	index, err := vm.PackMap(tags.Type, values.String("ada"), items)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := vm.PackAggregate(counts.Type, values.Int(0), values.Int(1))
	if err != nil {
		t.Fatal(err)
	}
	r, err := vm.PackAggregate(row.Type, index, pair)
	if err != nil {
		t.Fatal(err)
	}
	slots := [2]values.Value{r}
	if err := vm.Run(fn, slots[:]); err != nil || slots[1].AsString() != "early" {
		t.Fatalf("Tags: %v, %v", slots[1], err)
	}
	fields, err := vm.UnpackAggregate(row.Type, r)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := vm.UnpackMap(tags.Type, fields[0])
	if err != nil || len(entries) != 2 || entries[0].AsString() != "ada" {
		t.Fatal(entries, err)
	}
	labels, err := vm.UnpackAggregate(tags.Elem.Type, entries[1])
	if err != nil || len(labels) != 2 || labels[1].AsString() != "early" {
		t.Fatal(labels, err)
	}
	if _, err := vm.PackAggregate(counts.Type, values.Int(0)); err == nil {
		t.Fatal("accepted wrong array length")
	}
	if _, err := vm.PackMap(tags.Type, values.String("ada")); err == nil {
		t.Fatal("accepted map key without a value")
	}
	vm.SetBudget(evm.Limits{AllocationBytes: 1})
	if _, err := vm.PackAggregate(tags.Elem.Type, values.String("vip")); !errors.Is(err, evm.ErrAllocation) {
		t.Fatal(err)
	}
}

func TestFailedRunChargesBatch(t *testing.T) {
	m, err := evmcompiler.Compile("failure.go", `func Fail(n int)int{return 5/n};func Read()int{return 1}`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m)
	if err != nil {
		t.Fatal(err)
	}
	fail, err := p.LookupFunction("Fail", nil)
	if err != nil {
		t.Fatal(err)
	}
	read, err := p.LookupFunction("Read", nil)
	if err != nil {
		t.Fatal(err)
	}
	vm := p.AcquireVM(t.Context())
	defer vm.Release()
	vm.SetBudget(evm.Limits{Steps: 12})
	slots := [2]values.Value{values.Int(0), values.Int(123)}
	for range 13 {
		err := vm.Run(fail, slots[:])
		if slots[1].R != 123 {
			t.Fatal("failed run changed a result")
		}
		if errors.Is(err, evm.ErrSteps) {
			if err := vm.Run(read, slots[:1]); !errors.Is(err, evm.ErrSteps) {
				t.Fatal(err)
			}
			return
		}
		if !errors.Is(err, evm.ErrDivideByZero) {
			t.Fatal(err)
		}
	}
	t.Fatal("failed runs did not spend the budget")
}
