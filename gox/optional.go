package gox

// Optional distinguishes an omitted component argument from an explicit zero.
// XML calls construct Value and Present through structural optional recognition.
type Optional[T any] struct {
	Value   T
	Present bool
}
