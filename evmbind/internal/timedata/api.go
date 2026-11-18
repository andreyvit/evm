package timedata

import "time"

type Time struct{ Label string }

type Context struct {
	Locals []Time
	At     time.Time
	List   []time.Time
	Named  map[time.Time]string
}

func (c *Context) Echo(t time.Time) time.Time      { return t }
func (c *Context) Pointer(t *time.Time) *time.Time { return t }
