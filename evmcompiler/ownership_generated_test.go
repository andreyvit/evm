package evmcompiler

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/values"
)

// borrowingPackage lends strings from two domains and ends their scopes, each
// domain alone, both at once, and through a call that declares nothing.
func borrowingPackage() evm.Package {
	text := evm.Signature{Results: []*values.Type{values.StringType}}
	lend := func(domain string) *evm.OwnershipEffects {
		return &evm.OwnershipEffects{NoRetain: true, NoInvalidate: true, Borrowed: []evm.BorrowSource{{Result: 0, Domain: domain}}}
	}
	end := func(domains ...string) *evm.OwnershipEffects {
		effects := &evm.OwnershipEffects{NoRetain: true}
		for _, domain := range domains {
			effects.Ends = append(effects.Ends, evm.ScopeEnd{Domain: domain})
		}
		return effects
	}
	nothing := func(*evm.VM) error { return nil }
	return evm.Package{Path: "borrow", Functions: []evm.ExternalFunction{
		{Name: "Read", Signature: text, Ownership: lend("test.buffer"), Call: nothing},
		{Name: "Other", Signature: text, Ownership: lend("test.other"), Call: nothing},
		{Name: "Next", Signature: text, Ownership: &evm.OwnershipEffects{NoRetain: true, Borrowed: []evm.BorrowSource{{Result: 0, Domain: "test.buffer"}}, Ends: []evm.ScopeEnd{{Domain: "test.buffer"}}}, Call: nothing},
		{Name: "End", Ownership: end("test.buffer"), Call: nothing},
		{Name: "EndOther", Ownership: end("test.other"), Call: nothing},
		{Name: "EndBoth", Ownership: end("test.other", "test.buffer"), Call: nothing},
		{Name: "UnknownEnd", Call: nothing},
		{Name: "Keep", Signature: evm.Signature{Params: []*values.Type{values.StringType}}, Call: nothing},
		{Name: "Peek", Signature: evm.Signature{Params: []*values.Type{values.StringType}}, Ownership: &evm.OwnershipEffects{NoRetain: true, NoInvalidate: true}, Call: nothing},
	}}
}

// borrowingFunction writes a function that moves borrowed strings between
// locals, an array, a map and an interface, across branches, loops with early
// exits, swaps and scope ends, in no sensible order.
type borrowingFunction struct {
	r      *rand.Rand
	b      strings.Builder
	loops  int
	budget int
}

func (g *borrowingFunction) text() string  { return fmt.Sprintf("s%d", g.r.IntN(5)) }
func (g *borrowingFunction) count() string { return fmt.Sprintf("n%d", g.r.IntN(3)) }

func (g *borrowingFunction) condition() string {
	switch g.r.IntN(4) {
	case 0:
		return g.text() + " == " + g.text()
	case 1:
		return "len(" + g.text() + ") > " + g.count()
	case 2:
		return g.count() + " < " + g.count() + " && " + g.text() + ` != ""`
	default:
		return g.count() + " < x"
	}
}

func (g *borrowingFunction) block(depth int) {
	for n := 1 + g.r.IntN(4); n > 0 && g.budget > 0; n-- {
		g.budget--
		g.statement(depth)
	}
}

func (g *borrowingFunction) statement(depth int) {
	w := func(format string, args ...any) { fmt.Fprintf(&g.b, format+"\n", args...) }
	choice := g.r.IntN(28)
	if depth >= 3 && choice >= 20 {
		choice = g.r.IntN(20)
	}
	switch choice {
	case 0, 1:
		w("%s = borrow.Read()", g.text())
	case 2:
		w("%s = borrow.Other()", g.text())
	case 3, 4:
		w("%s = %s", g.text(), g.text())
	case 5:
		a, b := g.text(), g.text()
		w("%s, %s = %s, %s", a, b, b, a)
	case 6:
		w("arr[%d] = %s", g.r.IntN(3), g.text())
	case 7:
		w("%s = arr[%d]", g.text(), g.r.IntN(3))
	case 8:
		w("m[%s] = %s", g.text(), g.text())
	case 9:
		w(`%s = m["k"]`, g.text())
	case 10:
		w("borrow.End()")
	case 11:
		w("borrow.EndOther()")
	case 12:
		w("borrow.EndBoth()")
	case 13:
		w("borrow.UnknownEnd()")
	case 14:
		w("borrow.Keep(%s)", g.text())
	case 15:
		w("borrow.Peek(%s)", g.text())
	case 16:
		w("total += len(%s)", g.text())
	case 17:
		w("box = %s", g.text())
	case 18:
		w("if t, ok := box.(string); ok { %s = t }", g.text())
	case 19:
		w("%s++", g.count())
	case 20, 21:
		w("if %s {", g.condition())
		g.block(depth + 1)
		if g.r.IntN(2) == 0 {
			w("} else {")
			g.block(depth + 1)
		}
		w("}")
	case 22, 23:
		i := fmt.Sprintf("i%d", depth)
		w("for %s := 0; %s < x; %s++ {", i, i, i)
		g.loops++
		g.block(depth + 1)
		g.loops--
		w("}")
	case 24:
		w("switch %s {", g.count())
		for k := 0; k < 3; k++ {
			w("case %d:", k)
			g.block(depth + 1)
		}
		w("}")
	case 25:
		if g.loops == 0 {
			w("total++")
			return
		}
		w("if %s { %s }", g.condition(), []string{"break", "continue"}[g.r.IntN(2)])
	case 26:
		// The loop branches back to its head or out, from one block.
		n := g.count()
		w("for {")
		g.loops++
		g.block(depth + 1)
		g.loops--
		w("%s++\nif %s > x { break }", n, n)
		w("}")
	case 27:
		w("%s = borrow.Next()", g.text()) // ends the scope it lends from
	}
}

func borrowingSource(seed uint64, functions int) string {
	g := &borrowingFunction{r: rand.New(rand.NewPCG(seed, 1))}
	g.b.WriteString("import \"borrow\"\n")
	for i := 0; i < functions; i++ {
		fmt.Fprintf(&g.b, "func F%d(x int) string {\n", i)
		g.b.WriteString("var s0, s1, s2, s3, s4 string\nvar n0, n1, n2, total int\nvar arr [3]string\nm := map[string]string{}\nvar box any\n")
		g.b.WriteString("s0 = borrow.Read()\n")
		g.budget = 6 + g.r.IntN(40)
		for g.budget > 0 {
			g.block(0)
		}
		g.b.WriteString("_, _, _, _ = n0, n1, n2, total\n_, _, _ = arr, m, box\n_, _, _, _, _ = s0, s1, s2, s3, s4\n")
		fmt.Fprintf(&g.b, "return %s + %s\n}\n", g.text(), g.text())
	}
	return g.b.String()
}

// The plans, liveness and record lists of each function compiled here are
// compared with the implementations they replaced; see ownership_reference_test.go.
func TestOwnershipOfGeneratedFunctionsMatchesReference(t *testing.T) {
	pkg := borrowingPackage()
	seeds := uint64(60)
	if testing.Short() {
		seeds = 15
	}
	owned := 0
	for seed := uint64(1); seed <= seeds; seed++ {
		source := borrowingSource(seed, 12)
		for _, options := range []Options{{}, {NoOptimization: true}} {
			m, err := options.Compile("borrow.go", source, pkg)
			if err != nil {
				t.Fatalf("seed %d: %v\n%s", seed, err, source)
			}
			for _, f := range m.Functions {
				owned += len(f.OwnershipSites)
			}
		}
	}
	if owned < 1000 {
		t.Fatalf("the generated functions own only %d values", owned)
	}
}
