package packagefunctionfixture

import "errors"

type Bounds struct{ Lower, Upper []byte }
type Counter struct{ Value int }

func RangeOf(lower, upper []byte) Bounds { return Bounds{lower, upper} }
func NewCounter(n int) *Counter          { return &Counter{n} }
func Increment(c *Counter, n int) int    { c.Value += n; return c.Value }
func Double(n int) int                   { return n * 2 }
func Visit(c *Counter, yield func(int) bool) int {
	if yield(c.Value) {
		c.Value++
	}
	return c.Value
}
func Split(v []int) (int, error) {
	if len(v) == 0 {
		return 0, errors.New("empty")
	}
	return v[0], nil
}

func Reset(c *Counter) { c.Value = 0 }

func Sum(start int, values ...int) int {
	for _, value := range values {
		start += value
	}
	return start
}
func SetFirst(values ...int) {
	if len(values) > 0 {
		values[0] = 42
	}
}
func (c *Counter) Add(values ...int) int { c.Value = Sum(c.Value, values...); return c.Value }

type Adder interface{ Add(values ...int) int }

func AsAdder(c *Counter) Adder { return c }

type Group struct {
	Children []Adder
	First    Adder
	Named    map[string]Adder
}

func NewGroup(children ...Adder) *Group { return &Group{Children: children} }
