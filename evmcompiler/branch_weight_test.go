package evmcompiler_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
)

// repeated writes the n lines that format makes of 0..n-1.
func repeated(n int, format string) string {
	var b strings.Builder
	for i := range n {
		b.WriteString(strings.ReplaceAll(format, "#", fmt.Sprint(i)))
	}
	return b.String()
}

const (
	tooLarge    = "function is too large: its branches and local variables multiply past the compile budget"
	tooLargeSum = "functions are too large together: their branches and local variables multiply past the compile budget"
)

func TestBranchWeightLimit(t *testing.T) {
	ifs := func(n int) string {
		return "func f(x int) int { y := 0; " + repeated(n, "if x > # { y++ }; ") + "return y }"
	}
	// Each case assigns a variable of its own, so each variable takes a phi
	// operand for each case where the switch ends.
	cases := func(n int) string {
		return "func f(x int) int { " + repeated(n, "v# := x; ") + "switch x { " + repeated(n, "case #: v#++; ") + "}; s := 0; " + repeated(n, "s += v#; ") + "return s }"
	}
	// One variable assigned in every case takes one phi.
	oneResult := func(n int) string {
		return "func f(x int) string { s := \"\"; switch x { " + repeated(n, "case #: s = \"#\"; ") + "}; return s }"
	}
	breaks := func(n int) string {
		return "func f(x int) int { y := 0; for { " + repeated(n, "if x == # { break }; y++; ") + "}; return y }"
	}
	loops := func(n int) string {
		return "func f(x int) int { y := 0; " + repeated(n, "for i := 0; i < x; i++ { y += i }; ") + "return y }"
	}
	locals := func(n int) string {
		return "func f(x int) int { " + repeated(n, "v# := x + #; if x > # { v#++ }; ") + "s := 0; " + repeated(n, "s += v#; ") + "return s }"
	}
	for name, c := range map[string]struct {
		source  func(int) string
		fits    int
		exceeds int
	}{
		"ifs":            {ifs, 4000, 4200},
		"cases":          {cases, 450, 520},
		"one result":     {oneResult, 2000, 2400},
		"breaks":         {breaks, 2300, 2500},
		"loops":          {loops, 1200, 2000},
		"locals":         {locals, 2300, 2600},
		"straight lines": {func(n int) string { return "func f(x int) int { s := 0; " + repeated(n, "s += x * #; ") + "return s }" }, 50000, 0},
		"returns": {func(n int) string {
			return "func f(x int) int { " + repeated(n, "if x == # { return # }; ") + "return 0 }"
		}, 4000, 4200},
	} {
		if got := checkLimits(t, c.source(c.fits)); len(got) != 0 {
			t.Errorf("%s, %d: %v", name, c.fits, got)
		}
		if c.exceeds == 0 {
			continue
		}
		if got := checkLimits(t, c.source(c.exceeds)); len(got) != 1 || got[0] != "1:6: "+tooLarge {
			t.Errorf("%s, %d: %v", name, c.exceeds, got)
		}
	}

	// Functions add up: the one that takes the package past the limit is named.
	many := func(n int) string {
		return repeated(n, "func f#(x int) int { y := 0; "+repeated(1000, "if x > 0 { y++ }; ")+"return y }\n")
	}
	if got := checkLimits(t, many(14)); len(got) != 0 {
		t.Error(got)
	}
	if got := checkLimits(t, many(40)); len(got) != 1 || got[0] != "15:6: "+tooLargeSum {
		t.Error(got)
	}
	// A function literal is a function of its own, at its own position.
	literal := "func f(x int) int { g := func() int { y := 0; " + repeated(4200, "if x > # { y++ }; ") + "return y }; return g() }"
	if got := checkLimits(t, literal); len(got) != 1 || got[0] != "1:26: "+tooLarge {
		t.Error(got)
	}
	// Package variables are initialized in one function.
	variables := repeated(9000, "var v# = len(a) > # && len(b) > #\n")
	want := "1:10: package variables are too large: the branches of their initializers multiply past the compile budget"
	if got := checkLimits(t, variables); len(got) != 1 || got[0] != want {
		t.Error(got)
	}
	if got := compileError(ifs(4200)); got != "main.go:1:6: "+tooLarge {
		t.Error(got)
	}
}

// Each of these compiled for seconds, in gigabytes, when the passes that plan
// ownership and find record lists kept a set of live values per instruction
// and the SSA builder checked its own output; the call tree did not finish.
// A build that turns the builder's checks on is as slow as before.
func TestLargeFlatFunctionsCompile(t *testing.T) {
	if evmcompiler.SSAChecks() {
		t.Skip("the SSA builder's checks cost the square of a function")
	}
	tree := func(lo, hi int) string { return "" }
	tree = func(lo, hi int) string {
		if hi-lo == 1 {
			return fmt.Sprintf("x > %d", lo)
		}
		mid := (lo + hi) / 2
		return "(" + tree(lo, mid) + " && " + tree(mid, hi) + ")"
	}
	for name, source := range map[string]string{
		"4,000 ifs":                 "func F(x int) int { y := 0\n" + repeated(4000, "if x > # { y++ }\n") + "return y }",
		"2,000 conditions":          "func F(x, z int) int { y := 0\n" + repeated(2000, "if x > # && z < # || x == z { y++ }\n") + "return y }",
		"2,000 cases":               "func F(x int) int { y := 0\nswitch x {\n" + repeated(2000, "case #: y += #\n") + "}\nreturn y }",
		"2,000 else ifs":            "func F(x int) int { y := 0\n" + repeated(20, "if x < 0 { y++ }"+strings.Repeat(" else if x == 1 { y++ }", 99)+"\n") + "return y }",
		"4,000 terms":               "func F(x int) bool { return " + tree(0, 4000) + " }",
		"1,000 loops":               "func F(x int) int { y := 0\n" + repeated(1000, "for i := 0; i < x; i++ { y += i }\n") + "return y }",
		"2,000 locals":              "func F(x int) int {\n" + repeated(2000, "v# := x + #\nif x > # { v#++ }\n") + "s := 0\n" + repeated(2000, "s += v#\n") + "return s }",
		"50,000 repeated products":  "func F(x, y int) int { s := 0\n" + repeated(50000, "s ^= x * y\n") + "return s }",
		"50,000 folded sums":        "func F(x int) int { s := 0\n" + repeated(50000, "s ^= x + 0\n") + "return s }",
		"helpers 60 calls deep":     "func f0(a string) int { return len(a) }\n" + repeated(60, "func g#(a string) int { return f#(a) + f#(a) }\n") + "func F(a string) int { return f60(a) }",
		"3,000 helpers in a chain":  "func f0(a string) int { return len(a) }\n" + repeated(3000, "func g#(a string) int { return f#(a) }\n") + "func F(a string) int { return f3000(a) }",
		"1,000 helpers and a cycle": "func f0(a string) int { if len(a) > 3 { return f0(a[1:]) }; return len(a) }\n" + repeated(1000, "func g#(a string) int { return f#(a) }\n") + "func F(a string) int { return f1000(a) }",
	} {
		// The helpers call the one before them: g0 is f1.
		for i := 3000; i >= 0; i-- {
			if !strings.Contains(source, "func g") {
				break
			}
			source = strings.ReplaceAll(source, fmt.Sprintf("func g%d(", i), fmt.Sprintf("func f%d(", i+1))
		}
		evmcompiler.WithoutReference(func() {
			start := time.Now()
			if got := compileError(source); got != "" {
				t.Errorf("%s: %s", name, got)
			}
			if took := time.Since(start); took > 2*time.Second {
				t.Errorf("%s: compiled in %v", name, took)
			}
		})
	}
}

// Borrowed values and scope ends: each pair of them was asked about, and the
// copies of one value were compared in pairs.
func TestOwnershipOfLargeFunctions(t *testing.T) {
	if evmcompiler.SSAChecks() {
		t.Skip("the SSA builder's checks cost the square of a function")
	}
	pkg, _, _ := ownershipFixture()
	reads := func(n int) string { return repeated(n, "a# := borrow.Read()\n") }
	uses := func(n int) string { return "s := 0\n" + repeated(n, "s += len(a#)\n") + "return s }" }
	for name, c := range map[string]struct {
		source string
		copies int
		fails  string
	}{
		// One copy of each value, at the first call that may end its scope.
		"1,000 values, 1,000 scope ends": {source: "func F() int {\n" + reads(1000) + repeated(1000, "borrow.UnknownEnd()\n") + uses(1000), copies: 1000},
		// One copy in each branch: none of them comes before another.
		"one value, 1,000 branches":             {source: "func F(x int) int {\na := borrow.Read()\n" + repeated(1000, "if x == # { borrow.End() }\n") + "return len(a) }", copies: 1000},
		"1,000 values, 1,000 branches, one end": {source: "func F(x int) int {\ny := 0\n" + repeated(1000, "a# := borrow.Read()\nif x == # { y++ }\n") + "borrow.End()\n" + uses(1000), copies: 1000},
		"1,000 stores into an array":            {source: "func F() int {\nvar a [1000]string\n" + repeated(1000, "a[#] = borrow.Read()\n") + "s := 0\n" + repeated(1000, "s += len(a[#])\n") + "return s }", copies: 0},
		// Every value in every branch is a copy: the function is refused.
		"300 values, 300 branches": {source: "func F(x int) int {\n" + reads(300) + repeated(300, "if x == # { borrow.End() }\n") + uses(300), fails: "borrow.go:2:6: function is too large: it copies borrowed values in more places than the compile budget allows"},
	} {
		var m *evm.Module
		var err error
		evmcompiler.WithoutReference(func() {
			start := time.Now()
			m, err = evmcompiler.Compile("borrow.go", "import \"borrow\"\n"+c.source, pkg)
			if took := time.Since(start); took > 2*time.Second {
				t.Errorf("%s: compiled in %v", name, took)
			}
		})
		if c.fails != "" {
			if err == nil || err.Error() != c.fails {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		copies := 0
		for _, f := range m.Functions {
			copies += len(f.OwnershipSites)
		}
		if copies != c.copies {
			t.Errorf("%s: %d copies, want %d", name, copies, c.copies)
		}
	}
}
