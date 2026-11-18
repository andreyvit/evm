// Package sort provides primitives for sorting slices and user-defined
// collections.
package sort

import "slices"

// An implementation of Interface can be sorted by the routines in this
// package. The methods refer to elements by integer index.
type Interface interface {
	Len() int
	Less(i, j int) bool
	Swap(i, j int)
}

// Sort sorts data in ascending order as determined by the Less method. The
// sort is not guaranteed to be stable.
func Sort(data Interface) {
	n := data.Len()
	for start := (n - 2) / 2; start >= 0; start-- {
		siftDown(data, start, n)
	}
	for end := n - 1; end > 0; end-- {
		data.Swap(0, end)
		siftDown(data, 0, end)
	}
}

func siftDown(data Interface, root, end int) {
	for {
		child := 2*root + 1
		if child >= end {
			return
		}
		if child+1 < end && data.Less(child, child+1) {
			child++
		}
		if !data.Less(root, child) {
			return
		}
		data.Swap(root, child)
		root = child
	}
}

// Stable sorts data in ascending order while keeping the original order of
// equal elements.
func Stable(data Interface) {
	stableRange(data, 0, data.Len())
}

func stableRange(data Interface, a, b int) {
	if b-a <= 12 {
		for i := a + 1; i < b; i++ {
			for j := i; j > a && data.Less(j, j-1); j-- {
				data.Swap(j, j-1)
			}
		}
		return
	}
	m := a + (b-a)/2
	stableRange(data, a, m)
	stableRange(data, m, b)
	mergeInPlace(data, a, m, b)
}

// mergeInPlace merges the sorted runs [a,m) and [m,b) using rotations: it
// splits the longer run in half, finds the matching point in the other by
// binary search, rotates the middle, and merges both sides.
func mergeInPlace(data Interface, a, m, b int) {
	if a >= m || m >= b || !data.Less(m, m-1) {
		return
	}
	if b-a == 2 {
		data.Swap(a, m)
		return
	}
	var i, j int
	if m-a >= b-m {
		i = a + (m-a)/2
		// first position in [m,b) not less than data[i]
		lo, hi := m, b
		for lo < hi {
			h := lo + (hi-lo)/2
			if data.Less(h, i) {
				lo = h + 1
			} else {
				hi = h
			}
		}
		j = lo
	} else {
		j = m + (b-m)/2
		// first position in [a,m) greater than data[j]
		lo, hi := a, m
		for lo < hi {
			h := lo + (hi-lo)/2
			if !data.Less(j, h) {
				lo = h + 1
			} else {
				hi = h
			}
		}
		i = lo
	}
	rotate(data, i, m, j)
	k := i + (j - m)
	mergeInPlace(data, a, i, k)
	mergeInPlace(data, k, j, b)
}

// rotate moves [m,b) in front of [a,m) by three reversals.
func rotate(data Interface, a, m, b int) {
	reverseRange(data, a, m)
	reverseRange(data, m, b)
	reverseRange(data, a, b)
}

func reverseRange(data Interface, a, b int) {
	for i, j := a, b-1; i < j; i, j = i+1, j-1 {
		data.Swap(i, j)
	}
}

// IsSorted reports whether data is sorted.
func IsSorted(data Interface) bool {
	n := data.Len()
	for i := n - 1; i > 0; i-- {
		if data.Less(i, i-1) {
			return false
		}
	}
	return true
}

type reverse struct {
	Interface
}

func (r reverse) Less(i, j int) bool {
	return r.Interface.Less(j, i)
}

// Reverse returns the reverse order for data.
func Reverse(data Interface) Interface {
	return &reverse{data}
}

// IntSlice attaches the methods of Interface to []int.
type IntSlice []int

func (x IntSlice) Len() int           { return len(x) }
func (x IntSlice) Less(i, j int) bool { return x[i] < x[j] }
func (x IntSlice) Swap(i, j int)      { x[i], x[j] = x[j], x[i] }

// Sort is a convenience method: x.Sort() calls Sort(x).
func (x IntSlice) Sort() { slices.Sort(x) }

// Search returns the result of applying SearchInts to the receiver and v.
func (x IntSlice) Search(v int) int { return SearchInts(x, v) }

// Float64Slice attaches the methods of Interface to []float64, with NaNs
// ordered before other values.
type Float64Slice []float64

func (x Float64Slice) Len() int           { return len(x) }
func (x Float64Slice) Less(i, j int) bool { return x[i] < x[j] || (x[i] != x[i] && x[j] == x[j]) }
func (x Float64Slice) Swap(i, j int)      { x[i], x[j] = x[j], x[i] }

// Sort is a convenience method: x.Sort() calls Sort(x).
func (x Float64Slice) Sort() { slices.Sort(x) }

// Search returns the result of applying SearchFloat64s to the receiver and v.
func (x Float64Slice) Search(v float64) int { return SearchFloat64s(x, v) }

// StringSlice attaches the methods of Interface to []string.
type StringSlice []string

func (x StringSlice) Len() int           { return len(x) }
func (x StringSlice) Less(i, j int) bool { return x[i] < x[j] }
func (x StringSlice) Swap(i, j int)      { x[i], x[j] = x[j], x[i] }

// Sort is a convenience method: x.Sort() calls Sort(x).
func (x StringSlice) Sort() { slices.Sort(x) }

// Search returns the result of applying SearchStrings to the receiver and v.
func (x StringSlice) Search(v string) int { return SearchStrings(x, v) }

// Ints sorts a slice of ints in increasing order.
func Ints(x []int) { slices.Sort(x) }

// Float64s sorts a slice of float64s in increasing order, NaNs first.
func Float64s(x []float64) { slices.Sort(x) }

// Strings sorts a slice of strings in increasing order.
func Strings(x []string) { slices.Sort(x) }

// IntsAreSorted reports whether the slice x is sorted in increasing order.
func IntsAreSorted(x []int) bool { return slices.IsSorted(x) }

// Float64sAreSorted reports whether the slice x is sorted in increasing order.
func Float64sAreSorted(x []float64) bool { return slices.IsSorted(x) }

// StringsAreSorted reports whether the slice x is sorted in increasing order.
func StringsAreSorted(x []string) bool { return slices.IsSorted(x) }

// Search uses binary search to find and return the smallest index i in
// [0, n) at which f(i) is true, assuming f is false then true.
func Search(n int, f func(int) bool) int {
	i, j := 0, n
	for i < j {
		h := i + (j-i)/2
		if !f(h) {
			i = h + 1
		} else {
			j = h
		}
	}
	return i
}

// Find uses binary search to find the smallest index i at which cmp(i) <= 0.
func Find(n int, cmp func(int) int) (i int, found bool) {
	i, j := 0, n
	for i < j {
		h := i + (j-i)/2
		if cmp(h) > 0 {
			i = h + 1
		} else {
			j = h
		}
	}
	return i, i < n && cmp(i) == 0
}

// SearchInts searches for x in a sorted slice of ints.
func SearchInts(a []int, x int) int {
	i, _ := slices.BinarySearch(a, x)
	return i
}

// SearchFloat64s searches for x in a sorted slice of float64s.
func SearchFloat64s(a []float64, x float64) int {
	i, _ := slices.BinarySearch(a, x)
	return i
}

// SearchStrings searches for x in a sorted slice of strings.
func SearchStrings(a []string, x string) int {
	i, _ := slices.BinarySearch(a, x)
	return i
}
