package callbackfixture

type Runner struct {
	Saved  func(int) error
	Closed int
}

func (r *Runner) Call(fn func(int) error) error           { defer func() { r.Closed++ }(); return fn(7) }
func (r *Runner) Suppress(fn func(int) error)             { defer func() { recover(); r.Closed++ }(); fn(7) }
func (r *Runner) Store(fn func(int) error)                { r.Saved = fn }
func (r *Runner) Echo(fn func(int) error) func(int) error { return fn }
func (r *Runner) OnThread(fn func(int) error) error {
	done := make(chan error)
	go func() { done <- fn(7) }()
	return <-done
}
