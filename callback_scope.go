package evm

import (
	"context"
	"errors"
)

var ErrCallbackInactive = errors.New("callback requires an active native call on its originating VM")
var ErrCallbackScopeOpen = errors.New("callback scope already open")
var ErrCallbackExpired = errors.New("callback scope has expired")

// CallbackScope gives native Go adapters access to guest callbacks. The host
// must serialize its use. An implicit scope expires when the owner releases the VM.
type CallbackScope struct {
	program *Program
	ctx     context.Context
	vm      *VM
	limits  Limits
	open    bool
}

// CallbackScope returns the scope used by guest callbacks exported from this VM.
func (vm *VM) CallbackScope() *CallbackScope {
	if vm.callbackScope == nil {
		vm.callbackScope = &CallbackScope{vm: vm}
	}
	return vm.callbackScope
}

// WithCall borrows the active VM from a binding. An explicitly opened scope
// also accepts calls outside an active host binding, running each call on a
// fresh VM lease with its configured per-call budget.
func (s *CallbackScope) WithCall(fn func(*VM) error) error {
	if s == nil {
		return ErrCallbackExpired
	}
	vm := s.vm
	if vm != nil && vm.inNativeCall {
		return fn(vm)
	}
	if !s.open {
		if vm == nil {
			return ErrCallbackExpired
		}
		return ErrCallbackInactive
	}
	cbVM := s.program.AcquireVM(s.ctx)
	cbVM.SetBudget(s.limits)
	prev := s.vm
	s.vm = cbVM
	cbVM.callbackScope = s
	defer func() {
		cbVM.callbackScope = nil
		s.vm = prev
		cbVM.Release()
	}()
	v, err := cbVM.program.NativeFunction(nativeEntryType, fn)
	if err != nil {
		return err
	}
	target, err := cbVM.program.Callable(v)
	if err != nil {
		return err
	}
	return cbVM.Run(target, nil)
}

func (vm *VM) enterNativeCall() func() {
	previous := vm.inNativeCall
	vm.inNativeCall = true
	return func() { vm.inNativeCall = previous }
}

// OpenCallbackScope keeps exported Go adapters callable until Close. Each
// callback invoked outside an active host binding—whether the originating VM
// is idle or already released—runs on a fresh VM lease with the same program
// and Go context and a fresh per-call budget of limits (zero quotas are
// unlimited, matching VM.SetBudget; pass program.DefaultLimits() to reuse the
// program's default policy).
func (vm *VM) OpenCallbackScope(limits Limits) (*CallbackScope, error) {
	if vm.baseContext == nil {
		return nil, ErrInvalidState
	}
	if vm.running {
		return nil, ErrReentry
	}
	if err := limits.validate(); err != nil {
		return nil, err
	}

	if vm.callbackScope == nil {
		vm.callbackScope = &CallbackScope{vm: vm}
	}
	scope := vm.callbackScope
	if scope.open {
		return nil, ErrCallbackScopeOpen
	}
	scope.open = true
	scope.program, scope.ctx, scope.limits = vm.program, vm.baseContext, limits
	return scope, nil
}

// Close ends the host callback lifetime. Previously exported functions and
// errors remain values, but invoking their script methods fails after Close.
func (s *CallbackScope) Close() error {
	if s == nil {
		return nil
	}
	if s.vm != nil {
		if s.vm.running {
			return ErrReentry
		}
		s.vm.callbackScope, s.vm.guestErrors = nil, nil
	}
	*s = CallbackScope{}
	return nil
}
