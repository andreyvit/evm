package cachecore

type Store struct {
	cache struct{}
	owner struct{}
}
type Ticket struct{ entry struct{} }
