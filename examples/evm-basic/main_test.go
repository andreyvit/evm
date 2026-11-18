package main

import (
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	var out strings.Builder
	run(&out)
	want := strings.TrimPrefix(`
#1 A-1: +470 points, 470 total, basic
#2 A-2: +2030 points, 2500 total, gold
#3 A-3: +200 points, 2700 total, gold
Ada has 2700 points after [order A-1 order A-2 order A-3]
`, "\n")
	if out.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", out.String(), want)
	}
}
