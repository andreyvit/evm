package evm

import (
	"context"
	"errors"
)

var ErrCallbackInactive = errors.New("callback requires an active native call on its originating VM")
var ErrCallbackScopeOpen = errors.New("callback scope already open")
var ErrCallbackExpired = errors.New("callback scope has expired")

// CallbackScope retains the originating VM's execution state. It is not safe
// for concurrent use. Invoke it only on the goroutine driving that VM.
// A scope survives successful invocations and expires when the VM is reset.
type CallbackScope struct {
	vm      *VM
	context context.Context
	budget  Budget
}

func (vm *VM) rootVM() *VM {
	for vm.callbackParent != nil {
		vm = vm.callbackParent
	}
	return vm
}

// CallbackScope binds future native callbacks to this VM, including when this
// Call belongs to a nested callback. It does not retain the current Call window.
func (c *Call) CallbackScope() *CallbackScope {
	vm := c.vm.rootVM()
	if vm.callbackScope == nil {
		vm.callbackScope = &CallbackScope{vm: vm}
	}
	return vm.callbackScope
}

// WithCall supplies the originating VM's innermost active native call. The
// caller can encode arguments and use RunCallbackArgs with its existing budget,
// globals and context. Idle invocation requires an explicitly opened scope.
func (s *CallbackScope) WithCall(fn func(*Call) error) error {
	if s == nil || s.vm == nil {
		return ErrCallbackExpired
	}
	if s.vm.activeNativeCall != nil {
		return fn(s.vm.activeNativeCall)
	}
	if s.context != nil {
		return s.vm.RunNative(s.context, fn, s.budget)
	}
	return ErrCallbackInactive
}

func (c *Call) enterNativeCall() func() {
	vm := c.vm.rootVM()
	previous := vm.activeNativeCall
	vm.activeNativeCall = c
	return func() { vm.activeNativeCall = previous }
}

// OpenCallbackScope explicitly permits host calls to retained script functions
// and error methods while the VM is idle. Such calls reuse this VM, with the
// supplied context and per-call budget. Active calls keep their current budget.
// The host must serialize access and close the scope before reusing the VM for
// unrelated work. Reset also closes it. An implicit scope created by an earlier
// Run is upgraded in place. As with Run, an execution failure leaves the VM
// failed until Reset; a failing idle callback can therefore end this session.
func (vm *VM) OpenCallbackScope(ctx context.Context, budget Budget) (*CallbackScope, error) {
	if vm.running {
		return nil, ErrReentry
	}
	if ctx == nil {
		return nil, errors.New("callback scope requires context")
	}
	if vm.callbackScope == nil {
		vm.callbackScope = &CallbackScope{vm: vm}
	}
	scope := vm.callbackScope
	if scope.context != nil {
		return nil, ErrCallbackScopeOpen
	}
	scope.context, scope.budget = ctx, budget
	return scope, nil
}

// Close ends the host callback lifetime. Previously exported functions and
// errors remain values, but invoking their script methods fails after Close.
func (s *CallbackScope) Close() error {
	if s == nil || s.vm == nil {
		return nil
	}
	if s.vm.running {
		return ErrReentry
	}
	vm := s.vm
	s.vm = nil
	s.context = nil
	vm.callbackScope = nil
	vm.guestErrors = nil
	return nil
}
