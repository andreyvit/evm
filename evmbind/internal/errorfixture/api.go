package errorfixture

import "fmt"

type ErrorBox struct {
	Err    error
	Errors map[string]error
}

func (b *ErrorBox) Set(err error) error { b.Err = err; return err }

func (b *ErrorBox) Wrap(err error) error { return fmt.Errorf("wrapped: %w", err) }
