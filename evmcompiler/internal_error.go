package evmcompiler

import (
	"fmt"
	"runtime/debug"
)

// InternalError reports a panic raised while compiling or formatting. Guest
// source reaches the compiler in running hosts, so entry points return the
// failure instead of unwinding the host. Stack helps locate the defect.
type InternalError struct {
	Value any
	Stack []byte
}

func (e *InternalError) Error() string { return fmt.Sprintf("internal compiler error: %v", e.Value) }

func (e *InternalError) Unwrap() error {
	err, _ := e.Value.(error)
	return err
}

// CatchInternalError is deferred by entry points that accept guest source.
func CatchInternalError(err *error) {
	if x := recover(); x != nil {
		*err = &InternalError{Value: x, Stack: debug.Stack()}
	}
}
