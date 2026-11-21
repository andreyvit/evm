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
// also accepts calls while idle and acquires a fresh VM after release.
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
	if vm == nil {
		vm = s.program.AcquireVM(s.ctx)
		s.vm = vm
		vm.callbackScope = s
		defer vm.Release()
	}
	v, err := vm.program.NativeFunction(nativeEntryType, fn)
	if err != nil {
		return err
	}
	target, err := vm.program.Callable(v)
	if err != nil {
		return err
	}
	return vm.Run(target, nil)
}

func (vm *VM) enterNativeCall() func() {
	previous := vm.inNativeCall
	vm.inNativeCall = true
	return func() { vm.inNativeCall = previous }
}

// OpenCallbackScope keeps exported Go adapters callable until Close. After
// Release they use the same program and Go context with a fresh VM budget.
func (vm *VM) OpenCallbackScope() (*CallbackScope, error) {
	if vm.baseContext == nil {
		return nil, ErrInvalidState
	}
	if vm.running {
		return nil, ErrReentry
	}

	if vm.callbackScope == nil {
		vm.callbackScope = &CallbackScope{vm: vm}
	}
	scope := vm.callbackScope
	if scope.open {
		return nil, ErrCallbackScopeOpen
	}
	scope.open = true
	scope.program, scope.ctx = vm.program, vm.baseContext
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
