// Package appfixture is an ordinary Go package whose own source is bound whole.
package appfixture

import (
	"embed"
	"fmt"
	"strings"
	"sync"
)

//go:embed app.go
var Source embed.FS

const (
	MaxStock       = 99
	Greeting       = "hello"
	Kitchen  Shelf = iota + 1
	Garden
	internalLimit = MaxStock * 2
)

// Shelf is a named scalar with a method.
type Shelf int

func (s Shelf) Label() string {
	if s == Kitchen {
		return "kitchen"
	}
	return "garden"
}

type Money int64

func (m Money) String() string { return fmt.Sprintf("$%d.%02d", m/100, m%100) }

type Product struct {
	Name  string
	Price Money
	Shelf Shelf
	Tags  []string

	mu    sync.Mutex
	views int
}

func (p *Product) Viewed() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.views++
	return p.views
}

func (p *Product) Slug() string { return slug(p.Name) }

// Lock is exported but takes a type scripts cannot use.
func (p *Product) Lock(wg *sync.WaitGroup) { wg.Wait() }

type Store struct {
	Name     string
	Products []*Product
	registry
}

type registry struct{ seen map[string]bool }

func (s *Store) Find(name string) *Product {
	for _, p := range s.Products {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// Standalone is reachable from no function signature or field.
type Standalone struct{ Note string }

var Default = &Store{Name: "Default"}

var counter sync.Mutex

func CurrentStore() *Store { return Default }

// Discount takes named parameters that component calls rely on.
func Discount(amount Money, percent int) Money { return amount - amount*Money(percent)/100 }

func Wait(wg *sync.WaitGroup) { wg.Wait() }

func slug(s string) string {
	counter.Lock()
	defer counter.Unlock()
	return strings.ToLower(strings.ReplaceAll(s, " ", "-"))
}
