// A plugin: ordinary Go that the host compiles and runs inside EVM.
package plugin

import (
	"fmt"
	"shop"
	"slices"
	"strings"
	"time"
)

// OnOrder awards points for an order and returns a line for the log.
func OnOrder(c *shop.Customer, o *shop.Order, now time.Time) string {
	points := o.Cents / 100 * shop.PointsPerDollar
	if slices.ContainsFunc(o.Items, isGift) {
		points *= 2
	}
	if now.Sub(c.Joined) >= 365*24*time.Hour {
		points += 50 // loyalty bonus after a year
	}
	c.AddPoints(points, "order "+o.ID)

	for _, tier := range []string{"gold", "silver"} {
		if c.Points >= shop.TierThreshold(tier) {
			c.Tier = tier
			break
		}
	}
	return fmt.Sprintf("%s: +%d points, %d total, %s", o.ID, points, c.Points, c.Tier)
}

func isGift(item shop.Item) bool { return strings.HasPrefix(item.SKU, "GIFT-") }
