package importfixture

type Permission uint16

const (
	Read Permission = 1 << iota
	Write
)

type Box[T any] struct{ Value T }

type Child struct {
	Permission Permission
	Box[Permission]
}
