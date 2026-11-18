package strings

func Clone(s string) string
func ToLower(s string) string
func ToUpper(s string) string
func ToTitle(s string) string
func TrimSpace(s string) string
func Compare(s string, sub string) int
func Count(s string, sub string) int
func Index(s string, sub string) int
func LastIndex(s string, sub string) int
func IndexAny(s string, sub string) int
func LastIndexAny(s string, sub string) int
func Contains(s string, sub string) bool
func ContainsAny(s string, sub string) bool
func EqualFold(s string, sub string) bool
func HasPrefix(s string, sub string) bool
func HasSuffix(s string, sub string) bool
func ContainsRune(s string, r rune) bool
func IndexRune(s string, r rune) int
func IndexByte(s string, b byte) int
func LastIndexByte(s string, b byte) int
func Cut(s string, sep string) (string, string, bool)
func CutPrefix(s string, sep string) (string, bool)
func CutSuffix(s string, sep string) (string, bool)
func Fields(s string) []string
func Join(elems []string, sep string) string
func Repeat(s string, count int) string
func Replace(s string, old string, new string, n int) string
func ReplaceAll(s string, old string, new string) string
func Split(s string, sep string) []string
func SplitAfter(s string, sep string) []string
func SplitN(s string, sep string, n int) []string
func SplitAfterN(s string, sep string, n int) []string
func Trim(s string, cut string) string
func TrimLeft(s string, cut string) string
func TrimRight(s string, cut string) string
func TrimPrefix(s string, cut string) string
func TrimSuffix(s string, cut string) string

// runeWidth returns the number of bytes the first rune of s occupies. An
// invalid byte counts as one.
func runeWidth(s string) int {
	for i := range s {
		if i > 0 {
			return i
		}
	}
	for _, r := range s {
		if r == 0xFFFD && !(len(s) >= 3 && s[:3] == "\uFFFD") {
			return 1
		}
	}
	return len(s)
}

// IndexFunc returns the index of the first code point satisfying f, or -1.
func IndexFunc(s string, f func(rune) bool) int {
	for i, r := range s {
		if f(r) {
			return i
		}
	}
	return -1
}

// LastIndexFunc returns the index of the last code point satisfying f, or -1.
func LastIndexFunc(s string, f func(rune) bool) int {
	last := -1
	for i, r := range s {
		if f(r) {
			last = i
		}
	}
	return last
}

// ContainsFunc reports whether any code point in s satisfies f.
func ContainsFunc(s string, f func(rune) bool) bool { return IndexFunc(s, f) >= 0 }

// FieldsFunc splits s at each run of code points satisfying f.
func FieldsFunc(s string, f func(rune) bool) []string {
	var fields []string
	start := -1
	for i, r := range s {
		if f(r) {
			if start >= 0 {
				fields = append(fields, s[start:i])
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		fields = append(fields, s[start:])
	}
	return fields
}

// Map returns a copy of s with its characters modified by mapping. A
// negative result drops the character.
func Map(mapping func(rune) rune, s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if m := mapping(r); m >= 0 {
			out = append(out, m)
		}
	}
	return string(out)
}

// TrimLeftFunc returns s without the leading code points satisfying f.
func TrimLeftFunc(s string, f func(rune) bool) string {
	for i, r := range s {
		if !f(r) {
			return s[i:]
		}
	}
	return ""
}

// TrimRightFunc returns s without the trailing code points satisfying f.
func TrimRightFunc(s string, f func(rune) bool) string {
	end := 0
	for i, r := range s {
		if !f(r) {
			end = i + runeWidth(s[i:])
		}
	}
	return s[:end]
}

// TrimFunc returns s without the leading and trailing code points
// satisfying f.
func TrimFunc(s string, f func(rune) bool) string {
	return TrimRightFunc(TrimLeftFunc(s, f), f)
}

// CutLast slices s around the last instance of sep.
func CutLast(s, sep string) (before, after string, found bool) {
	if i := LastIndex(s, sep); i >= 0 {
		return s[:i], s[i+len(sep):], true
	}
	return s, "", false
}

// ToValidUTF8 returns a copy of s with each run of invalid UTF-8 byte
// sequences replaced by the replacement string.
func ToValidUTF8(s, replacement string) string {
	var out []byte
	invalid := false
	for i, r := range s {
		if r == 0xFFFD && runeWidth(s[i:]) == 1 {
			if !invalid {
				out = append(out, replacement...)
				invalid = true
			}
			continue
		}
		invalid = false
		out = append(out, s[i:i+runeWidth(s[i:])]...)
	}
	return string(out)
}

// Lines returns an iterator over the newline-terminated lines of s.
func Lines(s string) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for len(s) > 0 {
			line := s
			if i := IndexByte(s, '\n'); i >= 0 {
				line, s = s[:i+1], s[i+1:]
			} else {
				s = ""
			}
			if !yield(line) {
				return
			}
		}
	}
}

func splitSeq(s, sep string, keep int) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		if len(sep) == 0 {
			for len(s) > 0 {
				w := runeWidth(s)
				if !yield(s[:w]) {
					return
				}
				s = s[w:]
			}
			return
		}
		for {
			i := Index(s, sep)
			if i < 0 {
				break
			}
			if !yield(s[:i+keep]) {
				return
			}
			s = s[i+len(sep):]
		}
		yield(s)
	}
}

// SplitSeq returns an iterator over the substrings of s separated by sep.
func SplitSeq(s, sep string) func(yield func(string) bool) { return splitSeq(s, sep, 0) }

// SplitAfterSeq returns an iterator over substrings of s split after each sep.
func SplitAfterSeq(s, sep string) func(yield func(string) bool) { return splitSeq(s, sep, len(sep)) }

// FieldsFuncSeq returns an iterator over the fields FieldsFunc would return.
func FieldsFuncSeq(s string, f func(rune) bool) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		start := -1
		for i, r := range s {
			if f(r) {
				if start >= 0 {
					if !yield(s[start:i]) {
						return
					}
					start = -1
				}
			} else if start < 0 {
				start = i
			}
		}
		if start >= 0 {
			yield(s[start:])
		}
	}
}

// FieldsSeq returns an iterator over the fields Fields would return.
func FieldsSeq(s string) func(yield func(string) bool) {
	return FieldsFuncSeq(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r' || r == 0x85 || r == 0xA0 || r == 0x1680 || (r >= 0x2000 && r <= 0x200a) || r == 0x2028 || r == 0x2029 || r == 0x202f || r == 0x205f || r == 0x3000
	})
}

// A Builder is used to efficiently build a string. The zero value is ready
// to use.
type Builder struct {
	buf []byte
}

// String returns the accumulated string.
func (b *Builder) String() string { return string(b.buf) }

// Len returns the number of accumulated bytes.
func (b *Builder) Len() int { return len(b.buf) }

// Cap returns the capacity of the builder's underlying byte slice.
func (b *Builder) Cap() int { return cap(b.buf) }

// Reset resets the Builder to be empty.
func (b *Builder) Reset() { b.buf = nil }

// Grow grows b's capacity to guarantee space for another n bytes.
func (b *Builder) Grow(n int) {
	if n < 0 {
		panic("strings.Builder.Grow: negative count")
	}
	if cap(b.buf)-len(b.buf) < n {
		buf := make([]byte, len(b.buf), 2*cap(b.buf)+n)
		copy(buf, b.buf)
		b.buf = buf
	}
}

// Write appends the contents of p to b's buffer.
func (b *Builder) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	return len(p), nil
}

// WriteByte appends the byte c to b's buffer.
func (b *Builder) WriteByte(c byte) error {
	b.buf = append(b.buf, c)
	return nil
}

// WriteRune appends the UTF-8 encoding of r to b's buffer.
func (b *Builder) WriteRune(r rune) (int, error) {
	s := string(r)
	b.buf = append(b.buf, s...)
	return len(s), nil
}

// WriteString appends the contents of s to b's buffer.
func (b *Builder) WriteString(s string) (int, error) {
	b.buf = append(b.buf, s...)
	return len(s), nil
}

// Replacer replaces a list of strings with replacements.
type Replacer struct {
	oldnew []string
}

// NewReplacer returns a new Replacer from a list of old, new string pairs.
// Comparisons are done in argument order.
func NewReplacer(oldnew ...string) *Replacer {
	if len(oldnew)%2 == 1 {
		panic("strings.NewReplacer: odd argument count")
	}
	return &Replacer{append([]string(nil), oldnew...)}
}

// Replace returns a copy of s with all replacements performed.
func (r *Replacer) Replace(s string) string {
	var out []byte
	for i := 0; i <= len(s); {
		matched := false
		for k := 0; k < len(r.oldnew); k += 2 {
			old := r.oldnew[k]
			if !HasPrefix(s[i:], old) {
				continue
			}
			out = append(out, r.oldnew[k+1]...)
			if len(old) == 0 {
				// An empty old string matches before each byte and at the
				// end, as in Go; the byte itself is kept.
				if i < len(s) {
					out = append(out, s[i])
				}
				i++
			} else {
				i += len(old)
			}
			matched = true
			break
		}
		if !matched {
			if i < len(s) {
				out = append(out, s[i])
			}
			i++
		}
	}
	return string(out)
}
