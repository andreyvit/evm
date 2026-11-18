// Package slices defines functions useful with slices of any type.
package slices

import (
	"cmp"
	"iter"
)

// All returns an iterator over index-value pairs in the slice.
func All[Slice ~[]E, E any](s Slice) iter.Seq2[int, E] {
	return func(yield func(int, E) bool) {
		for i, v := range s {
			if !yield(i, v) {
				return
			}
		}
	}
}

// Backward returns an iterator over index-value pairs, last to first.
func Backward[Slice ~[]E, E any](s Slice) iter.Seq2[int, E] {
	return func(yield func(int, E) bool) {
		for i := len(s) - 1; i >= 0; i-- {
			if !yield(i, s[i]) {
				return
			}
		}
	}
}

// Values returns an iterator that yields the slice elements in order.
func Values[Slice ~[]E, E any](s Slice) iter.Seq[E] {
	return func(yield func(E) bool) {
		for _, v := range s {
			if !yield(v) {
				return
			}
		}
	}
}

// AppendSeq appends the values from seq to the slice and returns it.
func AppendSeq[Slice ~[]E, E any](s Slice, seq iter.Seq[E]) Slice {
	for v := range seq {
		s = append(s, v)
	}
	return s
}

// Collect collects values from seq into a new slice.
func Collect[E any](seq iter.Seq[E]) []E {
	var s []E
	for v := range seq {
		s = append(s, v)
	}
	return s
}

// Sorted collects values from seq into a new sorted slice.
func Sorted[E cmp.Ordered](seq iter.Seq[E]) []E {
	s := Collect(seq)
	Sort(s)
	return s
}

// SortedFunc collects values from seq into a new slice sorted by cmp.
func SortedFunc[E any](seq iter.Seq[E], cmp func(E, E) int) []E {
	s := Collect(seq)
	SortFunc(s, cmp)
	return s
}

// SortedStableFunc is SortedFunc keeping the order of equal elements.
func SortedStableFunc[E any](seq iter.Seq[E], cmp func(E, E) int) []E {
	s := Collect(seq)
	SortStableFunc(s, cmp)
	return s
}

// Chunk returns an iterator over consecutive sub-slices of up to n elements.
func Chunk[Slice ~[]E, E any](s Slice, n int) iter.Seq[Slice] {
	if n < 1 {
		panic("cannot be less than 1")
	}
	return func(yield func(Slice) bool) {
		for i := 0; i < len(s); i += n {
			end := min(n, len(s[i:]))
			if !yield(s[i : i+end : i+end]) {
				return
			}
		}
	}
}

// Equal reports whether two slices have the same length and elements.
func Equal[S ~[]E, E comparable](s1, s2 S) bool {
	if len(s1) != len(s2) {
		return false
	}
	for i := range s1 {
		if s1[i] != s2[i] {
			return false
		}
	}
	return true
}

// EqualFunc reports whether two slices are equal using eq on each pair.
func EqualFunc[S1 ~[]E1, S2 ~[]E2, E1, E2 any](s1 S1, s2 S2, eq func(E1, E2) bool) bool {
	if len(s1) != len(s2) {
		return false
	}
	for i, v1 := range s1 {
		if !eq(v1, s2[i]) {
			return false
		}
	}
	return true
}

// Compare compares the elements of s1 and s2 in order, using cmp.Compare.
func Compare[S ~[]E, E cmp.Ordered](s1, s2 S) int {
	for i, v1 := range s1 {
		if i >= len(s2) {
			return +1
		}
		if c := cmp.Compare(v1, s2[i]); c != 0 {
			return c
		}
	}
	if len(s1) < len(s2) {
		return -1
	}
	return 0
}

// CompareFunc is like Compare but uses a custom comparison function.
func CompareFunc[S1 ~[]E1, S2 ~[]E2, E1, E2 any](s1 S1, s2 S2, cmp func(E1, E2) int) int {
	for i, v1 := range s1 {
		if i >= len(s2) {
			return +1
		}
		if c := cmp(v1, s2[i]); c != 0 {
			return c
		}
	}
	if len(s1) < len(s2) {
		return -1
	}
	return 0
}

// Index returns the index of the first occurrence of v in s, or -1.
func Index[S ~[]E, E comparable](s S, v E) int {
	for i := range s {
		if v == s[i] {
			return i
		}
	}
	return -1
}

// IndexFunc returns the first index i satisfying f(s[i]), or -1.
func IndexFunc[S ~[]E, E any](s S, f func(E) bool) int {
	for i := range s {
		if f(s[i]) {
			return i
		}
	}
	return -1
}

// Contains reports whether v is present in s.
func Contains[S ~[]E, E comparable](s S, v E) bool {
	return Index(s, v) >= 0
}

// ContainsFunc reports whether at least one element of s satisfies f.
func ContainsFunc[S ~[]E, E any](s S, f func(E) bool) bool {
	return IndexFunc(s, f) >= 0
}

// Insert inserts the values v... into s at index i and returns the result.
func Insert[S ~[]E, E any](s S, i int, v ...E) S {
	_ = s[i:] // bounds check
	if len(v) == 0 {
		return s
	}
	out := make(S, 0, len(s)+len(v))
	out = append(out, s[:i]...)
	out = append(out, v...)
	return append(out, s[i:]...)
}

// Delete removes the elements s[i:j] from s and returns the result. The
// removed tail is zeroed.
func Delete[S ~[]E, E any](s S, i, j int) S {
	_ = s[i:j:len(s)] // bounds check
	if i == j {
		return s
	}
	oldlen := len(s)
	s = append(s[:i], s[j:]...)
	clear(s[len(s):oldlen])
	return s
}

// DeleteFunc removes the elements for which del returns true.
func DeleteFunc[S ~[]E, E any](s S, del func(E) bool) S {
	i := IndexFunc(s, del)
	if i == -1 {
		return s
	}
	for j := i + 1; j < len(s); j++ {
		if v := s[j]; !del(v) {
			s[i] = v
			i++
		}
	}
	clear(s[i:])
	return s[:i]
}

// Replace replaces the elements s[i:j] by the given v and returns the result.
func Replace[S ~[]E, E any](s S, i, j int, v ...E) S {
	_ = s[i:j] // bounds check
	out := make(S, 0, len(s)-(j-i)+len(v))
	out = append(out, s[:i]...)
	out = append(out, v...)
	return append(out, s[j:]...)
}

// Clone returns a shallow copy of the slice.
func Clone[S ~[]E, E any](s S) S {
	if s == nil {
		return nil
	}
	return append(S{}, s...)
}

// Compact replaces consecutive runs of equal elements with a single copy.
func Compact[S ~[]E, E comparable](s S) S {
	if len(s) < 2 {
		return s
	}
	i := 1
	for k := 1; k < len(s); k++ {
		if s[k] != s[k-1] {
			if i != k {
				s[i] = s[k]
			}
			i++
		}
	}
	clear(s[i:])
	return s[:i]
}

// CompactFunc is like Compact but uses an equality function.
func CompactFunc[S ~[]E, E any](s S, eq func(E, E) bool) S {
	if len(s) < 2 {
		return s
	}
	i := 1
	for k := 1; k < len(s); k++ {
		if !eq(s[k], s[i-1]) {
			if i != k {
				s[i] = s[k]
			}
			i++
		}
	}
	clear(s[i:])
	return s[:i]
}

// Grow increases the slice's capacity to hold n more elements.
func Grow[S ~[]E, E any](s S, n int) S {
	if n < 0 {
		panic("cannot be negative")
	}
	if n -= cap(s) - len(s); n > 0 {
		s = append(s[:cap(s)], make([]E, n)...)[:len(s)]
	}
	return s
}

// Clip removes unused capacity from the slice.
func Clip[S ~[]E, E any](s S) S {
	return s[:len(s):len(s)]
}

// Concat returns a new slice concatenating the passed in slices.
func Concat[S ~[]E, E any](slices ...S) S {
	size := 0
	for _, s := range slices {
		size += len(s)
	}
	out := make(S, 0, size)
	for _, s := range slices {
		out = append(out, s...)
	}
	return out
}

// Repeat returns a new slice that repeats the provided slice count times.
func Repeat[S ~[]E, E any](x S, count int) S {
	if count < 0 {
		panic("cannot be negative")
	}
	out := make(S, 0, len(x)*count)
	for i := 0; i < count; i++ {
		out = append(out, x...)
	}
	return out
}

// Reverse reverses the elements of the slice in place.
func Reverse[S ~[]E, E any](s S) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

// Sort sorts a slice of any ordered type in ascending order.
func Sort[S ~[]E, E cmp.Ordered](x S) {
	mergeSort(x, cmp.Compare[E])
}

// SortFunc sorts the slice in ascending order as determined by cmp.
func SortFunc[S ~[]E, E any](x S, cmp func(a, b E) int) {
	mergeSort(x, cmp)
}

// SortStableFunc sorts the slice, keeping the order of equal elements.
func SortStableFunc[S ~[]E, E any](x S, cmp func(a, b E) int) {
	mergeSort(x, cmp)
}

// mergeSort is a stable top-down merge sort with one scratch buffer.
func mergeSort[S ~[]E, E any](x S, cmp func(a, b E) int) {
	if len(x) < 2 {
		return
	}
	buf := make([]E, len(x))
	mergeSortRange(x, buf, 0, len(x), cmp)
}

func mergeSortRange[S ~[]E, E any](x S, buf []E, lo, hi int, cmp func(a, b E) int) {
	if hi-lo <= 12 {
		for i := lo + 1; i < hi; i++ {
			v := x[i]
			j := i
			for ; j > lo && cmp(v, x[j-1]) < 0; j-- {
				x[j] = x[j-1]
			}
			x[j] = v
		}
		return
	}
	mid := lo + (hi-lo)/2
	mergeSortRange(x, buf, lo, mid, cmp)
	mergeSortRange(x, buf, mid, hi, cmp)
	if cmp(x[mid], x[mid-1]) >= 0 {
		return // already ordered
	}
	copy(buf[lo:mid], x[lo:mid])
	i, j, k := lo, mid, lo
	for i < mid && j < hi {
		if cmp(x[j], buf[i]) < 0 {
			x[k] = x[j]
			j++
		} else {
			x[k] = buf[i]
			i++
		}
		k++
	}
	for i < mid {
		x[k] = buf[i]
		i++
		k++
	}
}

// IsSorted reports whether x is sorted in ascending order.
func IsSorted[S ~[]E, E cmp.Ordered](x S) bool {
	for i := len(x) - 1; i > 0; i-- {
		if cmp.Less(x[i], x[i-1]) {
			return false
		}
	}
	return true
}

// IsSortedFunc reports whether x is sorted in ascending order by cmp.
func IsSortedFunc[S ~[]E, E any](x S, cmp func(a, b E) int) bool {
	for i := len(x) - 1; i > 0; i-- {
		if cmp(x[i], x[i-1]) < 0 {
			return false
		}
	}
	return true
}

// Min returns the minimal value in x. It panics if x is empty.
func Min[S ~[]E, E cmp.Ordered](x S) E {
	if len(x) < 1 {
		panic("slices.Min: empty list")
	}
	m := x[0]
	for i := 1; i < len(x); i++ {
		if cmp.Less(x[i], m) {
			m = x[i]
		}
	}
	return m
}

// MinFunc returns the minimal value in x, using cmp to compare elements.
func MinFunc[S ~[]E, E any](x S, cmp func(a, b E) int) E {
	if len(x) < 1 {
		panic("slices.MinFunc: empty list")
	}
	m := x[0]
	for i := 1; i < len(x); i++ {
		if cmp(x[i], m) < 0 {
			m = x[i]
		}
	}
	return m
}

// Max returns the maximal value in x. It panics if x is empty.
func Max[S ~[]E, E cmp.Ordered](x S) E {
	if len(x) < 1 {
		panic("slices.Max: empty list")
	}
	m := x[0]
	for i := 1; i < len(x); i++ {
		if cmp.Less(m, x[i]) {
			m = x[i]
		}
	}
	return m
}

// MaxFunc returns the maximal value in x, using cmp to compare elements.
func MaxFunc[S ~[]E, E any](x S, cmp func(a, b E) int) E {
	if len(x) < 1 {
		panic("slices.MaxFunc: empty list")
	}
	m := x[0]
	for i := 1; i < len(x); i++ {
		if cmp(x[i], m) > 0 {
			m = x[i]
		}
	}
	return m
}

// BinarySearch searches for target in a sorted slice and returns the position
// where target is found, or where it would appear in the sort order.
func BinarySearch[S ~[]E, E cmp.Ordered](x S, target E) (int, bool) {
	n := len(x)
	i, j := 0, n
	for i < j {
		h := i + (j-i)/2
		if cmp.Less(x[h], target) {
			i = h + 1
		} else {
			j = h
		}
	}
	return i, i < n && cmp.Compare(x[i], target) == 0
}

// BinarySearchFunc works like BinarySearch, with a custom comparison function.
func BinarySearchFunc[S ~[]E, E, T any](x S, target T, cmp func(E, T) int) (int, bool) {
	n := len(x)
	i, j := 0, n
	for i < j {
		h := i + (j-i)/2
		if cmp(x[h], target) < 0 {
			i = h + 1
		} else {
			j = h
		}
	}
	return i, i < n && cmp(x[i], target) == 0
}
