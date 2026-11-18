// Package iter provides the iterator types used with range.
package iter

// Seq is an iterator over a sequence of values. It calls yield for each
// value and stops early when yield returns false.
type Seq[V any] func(yield func(V) bool)

// Seq2 is an iterator over a sequence of pairs, such as key and value.
type Seq2[K, V any] func(yield func(K, V) bool)
