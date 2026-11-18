package embeddedfixture

type Base struct{ Title string }
type Pointer struct {
	*Base
	Count int
}
type Value struct {
	Base
	Count int
}
