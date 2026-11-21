// Package evmtest provides named-entry fixtures for compiler and binding tests.
package evmtest

import (
	"context"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/values"
)

type Session struct {
	*evm.VM
	Limits  evm.Limits
	p       *evm.Program
	ctx     context.Context
	running bool
}

func New(p *evm.Program) *Session {
	ctx := context.Background()
	return &Session{VM: p.AcquireVM(ctx), p: p, ctx: ctx}
}
func (s *Session) Reset() error {
	if s.running {
		return evm.ErrReentry
	}
	s.Release()
	s.VM = s.p.AcquireVM(s.ctx)
	return nil
}
func (s *Session) prepare(ctx context.Context, l evm.Limits) {
	if ctx != s.ctx {
		output, coverage := s.Output, s.Coverage()
		s.Release()
		s.VM = s.p.AcquireVM(ctx)
		s.ctx = ctx
		s.Output = output
		_ = s.SetCoverage(coverage)
	}
	if l.Steps == 0 {
		l.Steps = s.Limits.Steps
	}
	if l.Steps == 0 {
		l.Steps = 1_000_000
	}
	if l.Allocations == 0 {
		l.Allocations = s.Limits.Allocations
	}
	if l.Allocations == 0 {
		l.Allocations = 1_000_000
	}
	if l.AllocationBytes == 0 {
		l.AllocationBytes = s.Limits.AllocationBytes
	}
	if l.AllocationBytes == 0 {
		l.AllocationBytes = 64 << 20
	}
	l.Depth, l.RSlots, l.PSlots = s.Limits.Depth, s.Limits.RSlots, s.Limits.PSlots
	l.OutputBytes = s.Limits.OutputBytes
	s.SetBudget(l)
}
func (s *Session) Run(ctx context.Context, name string, in, out []values.Value) error {
	return s.RunWithBudget(ctx, name, in, out, evm.Limits{})
}
func (s *Session) RunWithBudget(ctx context.Context, name string, in, out []values.Value, l evm.Limits) error {
	fn, err := s.p.LookupFunction(name, nil)
	if err != nil {
		return err
	}
	return s.RunFunctionWithBudget(ctx, &fn, in, out, l)
}
func (s *Session) RunFunction(ctx context.Context, fn *evm.Callable, in, out []values.Value) error {
	return s.RunFunctionWithBudget(ctx, fn, in, out, evm.Limits{})
}
func (s *Session) RunFunctionWithBudget(ctx context.Context, fn *evm.Callable, in, out []values.Value, l evm.Limits) error {
	if s.running {
		return evm.ErrReentry
	}
	s.prepare(ctx, l)
	s.running = true
	defer func() { s.running = false }()
	var local [12]values.Value
	count := len(in) + len(out)
	var slots []values.Value
	if count <= len(local) {
		slots = local[:count]
	} else {
		slots = make([]values.Value, count)
	}
	copy(slots, in)
	if fn == nil {
		return evm.ErrNilFunction
	}
	if err := s.VM.Run(*fn, slots); err != nil {
		return err
	}
	copy(out, slots[len(in):])
	return nil
}

var nativeType, _ = evm.NewNativeFunctionType(evm.Signature{})

func (s *Session) RunNative(ctx context.Context, fn evm.Native, l evm.Limits) error {
	v, err := s.p.NativeFunction(nativeType, fn)
	if err != nil {
		return err
	}
	target, err := s.p.Callable(v)
	if err != nil {
		return err
	}
	return s.RunFunctionWithBudget(ctx, &target, nil, nil, l)
}
func Callback(vm *evm.VM, v values.Value, in, out []values.Value) error {
	fn, err := vm.Program().Callable(v)
	if err != nil {
		return err
	}
	slots := make([]values.Value, len(in)+len(out))
	copy(slots, in)
	if err := vm.Run(fn, slots); err != nil {
		return err
	}
	copy(out, slots[len(in):])
	return nil
}
