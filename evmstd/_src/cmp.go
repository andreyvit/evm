// Package cmp provides types and functions for comparing ordered values.
package cmp

// Ordered is a constraint that permits any ordered type.
type Ordered interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr |
		~float32 | ~float64 |
		~string
}

// Less reports whether x is less than y. A NaN is less than any non-NaN.
func Less[T Ordered](x, y T) bool {
	return (x != x && y == y) || x < y
}

// Compare returns -1 if x is less than y, 0 if they are equal, and +1 if x is
// greater. A NaN is less than any non-NaN and equal to a NaN.
func Compare[T Ordered](x, y T) int {
	xNaN, yNaN := x != x, y != y
	if xNaN {
		if yNaN {
			return 0
		}
		return -1
	}
	if yNaN {
		return +1
	}
	if x < y {
		return -1
	}
	if x > y {
		return +1
	}
	return 0
}

// Or returns the first of its arguments that is not the zero value.
func Or[T comparable](vals ...T) T {
	var zero T
	for _, v := range vals {
		if v != zero {
			return v
		}
	}
	return zero
}
