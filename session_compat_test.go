package evm

import (
	"context"
	"github.com/andreyvit/evm/values"
)

type testSession struct {
	*VM
	Limits Limits
}

func New(p *Program) *testSession {
	return &testSession{VM: p.AcquireVM(context.Background()), Limits: p.defaultLimits}
}
func (s *testSession) Reset() error {
	p, ctx := s.program, s.baseContext
	s.Release()
	s.VM = p.AcquireVM(ctx)
	return nil
}
func (s *testSession) Run(ctx context.Context, name string, in, out []values.Value) error {
	return s.RunWithBudget(ctx, name, in, out, Budget{})
}
func (s *testSession) RunWithBudget(ctx context.Context, name string, in, out []values.Value, l Budget) error {
	fn, err := s.program.LookupFunction(name, nil)
	if err != nil {
		return err
	}
	if ctx != s.baseContext {
		p, output, coverage := s.program, s.Output, s.coverage
		s.Release()
		s.VM = p.AcquireVM(ctx)
		s.Output, s.coverage = output, coverage
	}
	if l == (Limits{}) {
		l = s.Limits
	}
	s.SetBudget(l)
	return s.call(fn, in, out)
}
func (s *testSession) call(fn Callable, in, out []values.Value) error {
	var local [12]values.Value
	n := len(in) + len(out)
	var slots []values.Value
	if n <= len(local) {
		slots = local[:n]
	} else {
		slots = make([]values.Value, n)
	}
	copy(slots, in)
	if err := s.VM.Run(fn, slots); err != nil {
		return err
	}
	copy(out, slots[len(in):])
	return nil
}
func (s *testSession) RunFunction(ctx context.Context, fn *Callable, in, out []values.Value) error {
	return s.call(*fn, in, out)
}
func (s *testSession) RunNative(ctx context.Context, fn Native, l Limits) error {
	v, err := s.program.NativeFunction(nativeEntryType, fn)
	if err != nil {
		return err
	}
	f, err := s.program.Callable(v)
	if err != nil {
		return err
	}
	s.SetBudget(l)
	return s.VM.Run(f, nil)
}
