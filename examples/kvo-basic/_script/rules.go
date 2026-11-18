// A script: ordinary Go that the host compiles and runs inside EVM.
package rules

import (
	"fmt"
	"m"
	"time"
)

// Describe reads a customer. Package m comes from the KVO schema: each model
// is a type and each property is a field. The same code runs on a Go struct
// and on an encoded row.
func Describe(c *m.Customer, now time.Time) string {
	status := "member"
	for _, tag := range c.tags {
		if tag == "vip" {
			status = "VIP"
		}
	}
	days := int(now.Sub(c.joined).Hours() / 24)
	return fmt.Sprintf("%s: %s for %d days, %d points", c.name, status, days, c.PointBalance())
}

// Redeem edits a customer. The host decides what an edit means: here it
// writes to the Go struct or builds a new row.
func Redeem(c *m.CustomerEdit, points int64) bool {
	if points <= 0 || points > c.points_earned-c.points_redeemed {
		return false
	}
	c.points_redeemed += points
	return true
}
