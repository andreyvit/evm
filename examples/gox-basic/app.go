package main

import (
	"fmt"
	"strings"
	"time"
)

// This file is the application: plain Go with no knowledge of GoX or EVM.

// FreeShippingThreshold is the smallest subtotal that ships free.
const FreeShippingThreshold Money = 25000

// Money is an amount in cents.
type Money int64

type Product struct {
	SKU   string
	Name  string
	Price Money
	Stock int
	Tags  []string

	views int // unexported: hidden from templates
}

func (p *Product) InStock() bool { return p.Stock > 0 }
func (p *Product) URL() string   { return "/products/" + strings.ToLower(p.SKU) }

// Viewed counts a view and returns the new total.
func (p *Product) Viewed() int { p.views++; return p.views }

type User struct {
	Name   string
	Email  string
	Admin  bool
	Joined time.Time
}

func (u *User) Initials() string {
	var b strings.Builder
	for _, w := range strings.Fields(u.Name) {
		b.WriteString(w[:1])
	}
	return b.String()
}

type CartLine struct {
	Product *Product
	Qty     int
}

func (l CartLine) Total() Money { return l.Product.Price * Money(l.Qty) }

type Cart struct {
	Lines  []CartLine
	Coupon string
}

func (c *Cart) Subtotal() (sum Money) {
	for _, l := range c.Lines {
		sum += l.Total()
	}
	return sum
}

func (c *Cart) Count() (n int) {
	for _, l := range c.Lines {
		n += l.Qty
	}
	return n
}

type Store struct {
	Name     string
	Currency string
	Products []*Product
}

func (s *Store) Search(q string) []*Product {
	var out []*Product
	for _, p := range s.Products {
		if q == "" || strings.Contains(strings.ToLower(p.Name), strings.ToLower(q)) {
			out = append(out, p)
		}
	}
	return out
}

// TheStore is the store the host renders pages for.
var TheStore = &Store{
	Name:     "Prairie Goods",
	Currency: "$",
	Products: []*Product{
		{SKU: "MUG-1", Name: "Enamel Mug", Price: 1800, Stock: 12, Tags: []string{"kitchen", "camp"}},
		{SKU: "BLK-2", Name: "Wool Blanket <XL>", Price: 12900, Stock: 0, Tags: []string{"home"}},
		{SKU: "LMP-3", Name: "Brass Lamp", Price: 6450, Stock: 3},
	},
}

func Pluralize(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func Discount(m Money, percent int) Money { return m - m*Money(percent)/100 }

// FormatMoney writes an amount in the store's currency.
func FormatMoney(m Money, store *Store) string {
	return fmt.Sprintf("%s%d.%02d", store.Currency, m/100, m%100)
}

// FormatDate writes a date the way the store shows dates to people.
func FormatDate(t time.Time) string { return t.Format("Jan 2, 2006") }
