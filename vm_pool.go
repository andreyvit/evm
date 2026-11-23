package evm

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"
	"unsafe"

	"github.com/andreyvit/evm/values"
)

var defaultExecutionLimits = Limits{Steps: 1_000_000, AllocationBytes: 64 << 20, Allocations: 1_000_000, OutputBytes: 1 << 20}
var vmPool = sync.Pool{New: func() any { return new(VM) }}
var callbackBuffers = sync.Pool{New: func() any { return new(executionBuffers) }}

// AcquireVM leases a VM for this program and starts its default budget.
func (p *Program) AcquireVM(ctx context.Context) *VM {
	if ctx == nil {
		panic("AcquireVM requires a context")
	}
	vm := vmPool.Get().(*VM)
	vm.program, vm.baseContext = p, ctx
	vm.prepareRecordLists()
	vm.SetBudget(p.defaultLimits)
	return vm
}

func (vm *VM) prepareRecordLists() {
	n := vm.program.recordListSlots
	if n > cap(vm.recordLists) {
		vm.recordLists = make([]recordListIterator, n)
	} else {
		vm.recordLists = vm.recordLists[:n]
	}
}

// SetBudget starts a new allowance. Zero quotas are unlimited.
func (vm *VM) SetBudget(l Limits) {
	if vm.running || vm.baseContext == nil {
		panic("SetBudget requires an idle acquired VM")
	}
	if vm.cancel != nil {
		vm.cancel()
		vm.cancel = nil
	}
	for vm.budget != nil && vm.budget.scope != nil {
		vm.budget.scope.release()
	}
	vm.invalidLimits = l.validate()
	ctx := vm.baseContext
	if l.Depth == 0 {
		l.Depth = 1024
	}
	if l.RSlots == 0 {
		l.RSlots = 1 << 20
	}
	if l.PSlots == 0 {
		l.PSlots = 1 << 20
	}
	soft := func(n, hard uint64) uint64 {
		if n != 0 {
			return n
		}
		if hard != 0 {
			return hard
		}
		return math.MaxUint64
	}
	l.Allocations = soft(l.Allocations, l.HardAllocations)
	l.AllocationBytes = soft(l.AllocationBytes, l.HardAllocationBytes)
	l.Steps = soft(l.Steps, 0)
	if l.UnlimitedSteps {
		l.Steps = math.MaxUint64
	}
	l.OutputBytes = soft(l.OutputBytes, 0)
	h := max(l.Duration, l.HardDuration)
	if h > 0 {
		ctx, vm.cancel = context.WithTimeout(ctx, h)
	}
	vm.Context = ctx
	vm.meter = executionBudget{ctx: ctx, canCancel: ctx.Done() != nil, limits: l, remaining: l.Steps, started: time.Now(), duration: l.Duration, hardDuration: h, hardBytes: max(l.AllocationBytes, l.HardAllocationBytes), hardAllocs: max(l.Allocations, l.HardAllocations)}
	vm.budget = &vm.meter
}

func (b *executionBuffers) clear() {
	if b.direct != nil {
		clear(b.direct.p[:])
	}
	clear(b.p)
	b.p = b.p[:0]
	clear(b.deferArgs[:cap(b.deferArgs)])
	b.deferArgs = b.deferArgs[:0]
	clear(b.deferred[:cap(b.deferred)])
	b.deferred = b.deferred[:0]
	clear(b.panics[:cap(b.panics)])
	b.panics = b.panics[:0]
	clear(b.recordLists)
	b.recordListGeneration = 0
	b.stack = b.stack[:0]
	b.deferStacks = b.deferStacks[:0]
}

func (vm *VM) clearWindow() {
	vm.R, vm.P, vm.native = nil, nil, nil
	vm.usedR, vm.usedP = 0, 0
	vm.Position = SourcePosition{}
	vm.hostCaller, vm.hostPC = 0, 0
	if vm.direct != nil {
		clear(vm.direct.p[:])
	}
}

type suspendedCall struct {
	registers          executionBuffers
	segment            *executionBuffers
	native             *linkedNative
	r                  []uint64
	p                  []unsafe.Pointer
	usedR, usedP       int
	position           SourcePosition
	hostCaller, hostPC int
}

func (vm *VM) enterCallback() suspendedCall {
	saved := suspendedCall{registers: vm.executionBuffers, segment: callbackBuffers.Get().(*executionBuffers), native: vm.native, r: vm.R, p: vm.P, usedR: vm.usedR, usedP: vm.usedP, position: vm.Position, hostCaller: vm.hostCaller, hostPC: vm.hostPC}
	vm.executionBuffers = *saved.segment
	vm.deferGeneration = 1
	vm.prepareRecordLists()
	vm.clearWindow()
	vm.Position = saved.position
	return saved
}

func (s *suspendedCall) restore(vm *VM) {
	vm.clearWindow()
	vm.executionBuffers.clear()
	*s.segment = vm.executionBuffers
	callbackBuffers.Put(s.segment)
	vm.executionBuffers = s.registers
	vm.hostCaller, vm.hostPC = s.hostCaller, s.hostPC
	vm.R, vm.P, vm.native, vm.Position, vm.usedR, vm.usedP = s.r, s.p, s.native, s.position, s.usedR, s.usedP
}

// Release returns this VM's cleared buffers to the shared pool.
func (vm *VM) Release() {
	if vm.running || vm.baseContext == nil {
		panic("Release requires an idle acquired VM")
	}
	if vm.cancel != nil {
		vm.cancel()
	}
	if vm.callbackScope != nil {
		vm.callbackScope.vm = nil
	}
	for vm.budget.scope != nil {
		vm.budget.scope.release()
	}
	vm.executionBuffers.clear()
	clear(vm.globals)
	*vm = VM{executionBuffers: vm.executionBuffers, globals: vm.globals[:0]}
	vmPool.Put(vm)
}

// Run calls a resolved function. Slots hold the arguments followed by room for its results.
func (vm *VM) Run(fn Callable, slots []values.Value) error {
	if fn.callTarget == nil {
		return ErrNilFunction
	}
	if fn.program != vm.program {
		return ErrFunction
	}
	n := len(fn.signature.Params)
	if len(slots) != n+len(fn.signature.Results) {
		return fmt.Errorf("%s: expected %d argument and %d result slots, got %d", fn.Name(), n, len(fn.signature.Results), len(slots))
	}
	if vm.running {
		return vm.runValue(fn.guestValue(), slots[:n], slots[n:])
	}
	if vm.baseContext == nil {
		return ErrInvalidState
	}
	b := vm.budget
	if vm.invalidLimits != nil {
		return vm.invalidLimits
	}
	if b.err != nil {
		err := b.err
		b.err = nil
		return err
	}
	if b.remaining == 0 {
		return ErrSteps
	}
	vm.running = true
	vm.deferGeneration++
	defer func() {
		for b.scope != nil {
			b.scope.release()
		}
		b.records = nil
		b.err = nil
		vm.Context = b.ctx
		vm.clearWindow()
		vm.executionBuffers.clear()
		clear(vm.globals)
		vm.globals = vm.globals[:0]
		vm.running = false
	}()
	return vm.runBody(fn.index, fn.guestValue(), slots[:n], slots[n:], b)
}
