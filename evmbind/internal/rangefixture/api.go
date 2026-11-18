package rangefixture

// Range mirrors a host key range: borrowed byte bounds, flags and a value method.
type Range struct {
	Prefix   []byte
	Lower    []byte
	Upper    []byte
	LowerInc bool
	UpperInc bool
	Reverse  bool
}

func Open() Range                 { return Range{} }
func Inclusive(l, u []byte) Range { return Range{Lower: l, Upper: u, LowerInc: true, UpperInc: true} }
func WithPrefix(p []byte) Range   { return Range{Prefix: p} }
func (r Range) Reversed() Range   { r.Reverse = true; return r }
