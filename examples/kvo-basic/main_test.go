package main

import (
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	var out strings.Builder
	run(&out)
	want := strings.TrimPrefix(`
Ada: 1000 points, 1 tags, joined 2024-01-15
Bob: 200 points, 1 tags, joined 2025-02-01
Cyd: 80 points, 0 tags, joined 2025-02-20
Ada's struct: 1350 earned, tags [vip welcomed]
Bob's old row: 300 earned; new row: 400 earned, tags [newsletter welcomed]
Ada: VIP for 411 days, 1100 points
Bob: member for 28 days, 300 points
Cyd: member for 9 days, 180 points
Ada redeems 250: true, balance 850
Bob redeems 250: true, balance 50
Cyd redeems 250: false, balance 180
Ada's struct: 500 redeemed
`, "\n")
	if out.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", out.String(), want)
	}
}
