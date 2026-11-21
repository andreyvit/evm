package evmcompiler_test

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
)

// nesting builds prefix, n opens, middle, n closes and suffix on one line.
type nesting struct {
	name                                string
	prefix, open, middle, close, suffix string
	limit                               int
	message                             string
	// extra opens fit before the first counted level; at is the offset of the
	// reported node inside open.
	extra, at int
}

func (c nesting) source(n int) string {
	return c.prefix + strings.Repeat(c.open, n) + c.middle + strings.Repeat(c.close, n) + c.suffix
}

// column is where the first level past the limit starts.
func (c nesting) column() int {
	return len(c.prefix) + (c.limit+c.extra)*len(c.open) + c.at + 1
}

var nestings = []nesting{
	{name: "slice", prefix: "var x ", open: "[]", middle: "int", limit: evmcompiler.MaxTypeNesting, message: "type nests more than 32 levels"},
	{name: "array", prefix: "var x ", open: "[1]", middle: "int", limit: evmcompiler.MaxTypeNesting, message: "type nests more than 32 levels"},
	{name: "map", prefix: "var x ", open: "map[string]", middle: "int", limit: evmcompiler.MaxTypeNesting, message: "type nests more than 32 levels"},
	{name: "chan", prefix: "var x ", open: "chan ", middle: "int", limit: evmcompiler.MaxTypeNesting, message: "type nests more than 32 levels"},
	{name: "func type", prefix: "var x ", open: "func() ", middle: "int", limit: evmcompiler.MaxTypeNesting, message: "type nests more than 32 levels"},
	{name: "struct", prefix: "type T ", open: "struct{ X ", middle: "int", close: " }", limit: evmcompiler.MaxTypeNesting, message: "type nests more than 32 levels"},
	{name: "interface", prefix: "type T ", open: "interface{ M() ", middle: "int", close: " }", limit: evmcompiler.MaxTypeNesting / 2, message: "type nests more than 32 levels"},
	{name: "parameter", prefix: "func f(x ", open: "[]", middle: "int", suffix: ") {}", limit: evmcompiler.MaxTypeNesting, message: "type nests more than 32 levels"},
	{name: "pointer", prefix: "var x ", open: "*", middle: "int", limit: evmcompiler.MaxTypeNesting, message: "pointers nest more than 32 levels"},
	{name: "type argument", prefix: "var x ", open: "L[", middle: "int", close: "]", limit: evmcompiler.MaxTypeNesting, extra: 1, message: "index nests more than 32 levels"},

	{name: "parentheses", prefix: "var x = ", open: "(", middle: "1", close: ")", limit: evmcompiler.MaxExprNesting, message: "expression nests more than 500 levels"},
	{name: "calls", prefix: "var x = ", open: "f(", middle: "1", close: ")", limit: evmcompiler.MaxExprNesting, message: "expression nests more than 500 levels"},
	{name: "negations", prefix: "var x = ", open: "!", middle: "true", limit: evmcompiler.MaxExprNesting, message: "expression nests more than 500 levels"},

	{name: "if", prefix: "func f(a int) { ", open: "if a > 0 {", middle: "a++", close: "}", suffix: " }", limit: evmcompiler.MaxStmtNesting, message: "statements nest more than 100 levels"},
	{name: "block", prefix: "func f(a int) { ", open: "{", middle: "a++", close: "}", suffix: " }", limit: evmcompiler.MaxStmtNesting, message: "statements nest more than 100 levels"},
	{name: "for", prefix: "func f(a int) { ", open: "for a < 0 {", middle: "a++", close: "}", suffix: " }", limit: evmcompiler.MaxStmtNesting, message: "statements nest more than 100 levels"},
	{name: "range", prefix: "func f(a []int) { ", open: "for range a {", middle: "a = nil", close: "}", suffix: " }", limit: evmcompiler.MaxStmtNesting, message: "statements nest more than 100 levels"},
	{name: "switch", prefix: "func f(a int) { ", open: "switch { case a > 0: ", middle: "a++", close: "}", suffix: " }", limit: evmcompiler.MaxStmtNesting, message: "statements nest more than 100 levels"},
	{name: "type switch", prefix: "func f(a any) { ", open: "switch a.(type) { default: ", middle: "a = nil", close: "}", suffix: " }", limit: evmcompiler.MaxStmtNesting, message: "statements nest more than 100 levels"},
	{name: "else if", prefix: "func f(a int) { if a == 0 {}", open: " else if a == 0 {}", suffix: " }", limit: evmcompiler.MaxStmtNesting, extra: -1, at: len(" else "), message: "statements nest more than 100 levels"},
	{name: "else", prefix: "func f(a int) { ", open: "if a == 0 {} else { ", middle: "a++", close: "}", suffix: " }", limit: evmcompiler.MaxStmtNesting, message: "statements nest more than 100 levels"},

	{name: "function literal", prefix: "func f(a int) { ", open: "func() {", middle: "a++", close: "}()", suffix: " }", limit: evmcompiler.MaxFuncNesting, message: "function literals nest more than 32 levels"},
}

func checkLimits(t *testing.T, source string) []string {
	t.Helper()
	files := token.NewFileSet()
	file, err := parser.ParseFile(files, "main.go", "package p\n"+source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evmcompiler.CheckLimits([]*ast.File{file}) {
		p := files.Position(e.Pos)
		out = append(out, fmt.Sprintf("%d:%d: %s", p.Line-1, p.Column, e.Message))
	}
	return out
}

func TestNestingLimits(t *testing.T) {
	for _, c := range nestings {
		t.Run(c.name, func(t *testing.T) {
			if got := checkLimits(t, c.source(c.limit+c.extra)); len(got) != 0 {
				t.Fatalf("at the limit: %v", got)
			}
			got := checkLimits(t, c.source(c.limit+c.extra+1))
			want := fmt.Sprintf("1:%d: %s", c.column(), c.message)
			if len(got) != 1 || got[0] != want {
				t.Fatalf("past the limit: got %v, want %s", got, want)
			}
		})
	}
}

func TestNestingLimitsCountRealDepth(t *testing.T) {
	terms := func(n int) string { return "var x = 1" + strings.Repeat("+1", n) }
	if got := checkLimits(t, terms(evmcompiler.MaxExprNesting)); len(got) != 0 {
		t.Fatal(got)
	}
	// A chain leans left: its first operator is the deepest, at the chain's start.
	if got := checkLimits(t, terms(evmcompiler.MaxExprNesting+1)); len(got) != 1 || got[0] != "1:9: expression nests more than 500 levels" {
		t.Fatal(got)
	}

	labels := func(n int) string {
		var b strings.Builder
		b.WriteString("func f() {")
		for i := range n {
			fmt.Fprintf(&b, " L%03d:", i)
		}
		return b.String() + " }"
	}
	if got := checkLimits(t, labels(evmcompiler.MaxStmtNesting)); len(got) != 0 {
		t.Fatal(got)
	}
	want := fmt.Sprintf("1:%d: statements nest more than 100 levels", len("func f() {")+evmcompiler.MaxStmtNesting*len(" L000:")+2)
	if got := checkLimits(t, labels(evmcompiler.MaxStmtNesting+1)); len(got) != 1 || got[0] != want {
		t.Fatalf("got %v, want %s", got, want)
	}

	// A function literal starts a new expression and a new type, but not a
	// new scope: its statements nest inside the ones around it.
	deep := strings.Repeat("(", 400)
	closed := strings.Repeat(")", 400)
	if got := checkLimits(t, "var x = "+deep+"func() int { return "+deep+"1"+closed+" }()"+closed); len(got) != 0 {
		t.Fatal(got)
	}
	if got := checkLimits(t, "var x = *g(func() { var y "+strings.Repeat("*", 32)+"int })"); len(got) != 0 {
		t.Fatal(got)
	}
	ifs := strings.Repeat("if a > 0 {", 60)
	ends := strings.Repeat("}", 60)
	if got := checkLimits(t, "func f(a int) { "+ifs+"func() { "+ifs+ends+" }()"+ends+" }"); len(got) != 1 || !strings.HasSuffix(got[0], ": statements nest more than 100 levels") {
		t.Fatal(got)
	}

	// Independent mistakes are each reported, up to a few.
	if got := checkLimits(t, strings.Repeat("var _ "+strings.Repeat("[]", 33)+"int\n", 50)); len(got) != 10 || got[9] != "10:71: type nests more than 32 levels" {
		t.Fatal(got)
	}
}

func compileError(source string, packages ...evm.Package) string {
	_, err := evmcompiler.Compile("main.go", source, packages...)
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestCompileAtNestingLimits(t *testing.T) {
	for name, source := range map[string]string{
		"type":             "func Run() int { var x " + strings.Repeat("[]", 32) + "int; return len(x) }",
		"expression":       "func Run() int { a := 1; return a" + strings.Repeat("+a", 500) + " }",
		"statements":       "func Run() int { a := 1; " + strings.Repeat("if a > 0 {", 100) + "a++" + strings.Repeat("}", 100) + "; return a }",
		"function literal": "func Run() int { a := 1; " + strings.Repeat("func() {", 32) + "a++" + strings.Repeat("}()", 32) + "; return a }",
	} {
		if got := compileError(source); got != "" {
			t.Errorf("%s: %s", name, got)
		}
	}
	for want, source := range map[string]string{
		"main.go:1:88: type nests more than 32 levels":              "func Run() int { var x " + strings.Repeat("[]", 33) + "int; return len(x) }",
		"main.go:2:33: expression nests more than 500 levels":       "package plugin\nfunc Run() int { a := 1; return a" + strings.Repeat("+a", 501) + " }",
		"main.go:1:1026: statements nest more than 100 levels":      "func Run() int { a := 1; " + strings.Repeat("if a > 0 {", 101) + "a++" + strings.Repeat("}", 101) + "; return a }",
		"main.go:1:282: function literals nest more than 32 levels": "func Run() int { a := 1; " + strings.Repeat("func() {", 33) + "a++" + strings.Repeat("}()", 33) + "; return a }",
	} {
		if got := compileError(source); got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
	_, err := evmcompiler.CompileFiles(map[string]string{
		"main.go":  "package plugin\nfunc Run() int { return 1 }",
		"other.go": "package plugin\nvar x " + strings.Repeat("[]", 33) + "int",
	})
	if err == nil || err.Error() != "other.go:2:71: type nests more than 32 levels" {
		t.Fatal(err)
	}
}

// Each of these took the compiler from half a minute to more than 100
// seconds, or more than a gigabyte, before the limits.
func TestCompileRejectsCostlySources(t *testing.T) {
	doubling := `const a = "xxxxxxxxxxxxxxxx"
const b = a+a+a+a+a+a+a+a+a+a+a+a+a+a+a+a
const c = b+b+b+b+b+b+b+b+b+b+b+b+b+b+b+b
const d = c+c+c+c+c+c+c+c+c+c+c+c+c+c+c+c
const e = d+d+d+d+d+d+d+d+d+d+d+d+d+d+d+d
const f = e+e+e+e+e+e+e+e+e+e+e+e+e+e+e+e
`
	for name, c := range map[string]struct{ source, want string }{
		"slice 1000 deep":       {"func Run() int { var x " + strings.Repeat("[]", 1000) + "int; return len(x) }", "main.go:1:88: type nests more than 32 levels"},
		"slice 4000 deep":       {"func Run() int { var x " + strings.Repeat("[]", 4000) + "int; return len(x) }", "main.go:1:88: type nests more than 32 levels"},
		"90,000 terms":          {"func Run() int { a := 1; return a" + strings.Repeat("+a", 90000) + " }", "main.go:1:33: expression nests more than 500 levels"},
		"90,000 conditions":     {"func Run() int { a := true; if a" + strings.Repeat(" && a", 90000) + " { return 1 }; return 0 }", "main.go:1:32: expression nests more than 500 levels"},
		"2000 nested closures":  {"func Run() int { a := 1; " + strings.Repeat("func() {", 2000) + "a++" + strings.Repeat("}()", 2000) + "; return a }", "main.go:1:282: function literals nest more than 32 levels"},
		"4000 nested ifs":       {"func Run() int { a := 1; " + strings.Repeat("if a > 0 {", 4000) + "a++" + strings.Repeat("}", 4000) + "; return a }", "main.go:1:1026: statements nest more than 100 levels"},
		"500 nested loops":      {"func Run() int { a := 1; " + strings.Repeat("for a < 0 {", 500) + "a++" + strings.Repeat("}", 500) + "; return a }", "main.go:1:1126: statements nest more than 100 levels"},
		"string doubling":       {doubling + "func Run() int { return len(f) }", "main.go:6:11: constant string may exceed 1048576 bytes"},
		"string doubling, used": {doubling + "func Run() int { s := f; return len(s) }", "main.go:6:11: constant string may exceed 1048576 bytes"},
	} {
		if got := compileError(c.source); got != c.want {
			t.Errorf("%s: got %.200s, want %s", name, got, c.want)
		}
	}
}

func TestConstStringLimit(t *testing.T) {
	// 16 * 65536 bytes is the limit exactly.
	base := "const a = \"" + strings.Repeat("x", 65536) + "\"\nconst b = a+a+a+a+a+a+a+a+a+a+a+a+a+a+a+a\n"
	const exceeded = ": constant string may exceed 1048576 bytes"
	for name, c := range map[string]struct{ source, want string }{
		"at the limit":       {base + "func Run() int { return len(b) }", ""},
		"one byte more":      {base + "const c = b + \"x\"\nfunc Run() int { return len(c) }", "main.go:3:11" + exceeded},
		"variable":           {base + "var s = b + b\nfunc Run() int { return len(s) }", "main.go:3:9" + exceeded},
		"local":              {base + "func Run() int {\n\ts := \"x\" + (b + \"y\")\n\treturn len(s)\n}", "main.go:4:14" + exceeded},
		"argument":           {base + "func Run() int {\n\treturn len(b + b)\n}", "main.go:4:13" + exceeded},
		"conversion":         {base + "type S string\nconst c = S(b) + S(a)\nfunc Run() int { return len(c) }", "main.go:4:11" + exceeded},
		"rune conversion":    {"const a = string('x') + string('x')\n" + doublings("a", 20) + "func Run() int { return len(a20) }", "main.go:19:13" + exceeded},
		"repeated value":     {base + "const (\n\tc = b + \"x\"\n\td\n)\nfunc Run() int { return len(d) }", "main.go:4:6" + exceeded},
		"forward reference":  {"const c = b + b\n" + base + "func Run() int { return len(c) }", "main.go:1:11" + exceeded},
		"local constant":     {base + "func Run() int {\n\tconst c = b + \"x\"\n\treturn len(c)\n}", "main.go:4:12" + exceeded},
		"shadowed name":      {base + "func Run() int {\n\tb := \"x\"\n\treturn len(b + b)\n}", "main.go:5:13" + exceeded},
		"reported once":      {base + "const c = b + \"x\"\nconst d = c + c\nvar s = c + \"y\"\nfunc Run() int { return len(d) + len(s) }", "main.go:3:11" + exceeded},
		"numbers are no str": {base + "const n = len(b) + len(b)\nfunc Run() int { return n }", ""},
		"short strings":      {"func Run() string {\n\ts := \"a\"\n\treturn s" + strings.Repeat(" + s", 400) + "\n}", ""},
	} {
		if got := compileError(c.source); got != c.want {
			t.Errorf("%s: got %.200s, want %s", name, got, c.want)
		}
	}

	// The type checker reports a cycle; the estimate must end.
	if got := compileError("const a = b + \"x\"\nconst b = a + \"y\"\nfunc Run() int { return len(a) }"); got == "" || strings.Contains(got, "constant string") {
		t.Error(got)
	}
	// Chains of names as long as the file resolve without recursion.
	var chain strings.Builder
	chain.WriteString("const c0 = \"x\"\n")
	for i := 1; i <= 20000; i++ {
		fmt.Fprintf(&chain, "const c%d = c%d + \"\"\n", i, i-1)
	}
	files := token.NewFileSet()
	file, err := parser.ParseFile(files, "main.go", "package p\nvar s = c20000 + c20000\n"+chain.String(), parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if got := evmcompiler.CheckLimits([]*ast.File{file}); len(got) != 0 {
		t.Error(got)
	}
}

// doublings declares name1 to nameN, each twice the one before.
func doublings(name string, n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		prev := name
		if i > 1 {
			prev = fmt.Sprintf("%s%d", name, i-1)
		}
		fmt.Fprintf(&b, "const %s%d = %s + %s\n", name, i, prev, prev)
	}
	return b.String()
}

func TestConstStringLimitCountsImportedConstants(t *testing.T) {
	host := evm.Package{Path: "example.test/host", Constants: []evm.ExternalConstant{
		{Name: "Big", Untyped: constant.MakeString(strings.Repeat("x", 600000))},
		{Name: "Small", Untyped: constant.MakeString("ab")},
	}}
	const prefix = "import \"example.test/host\"\n"
	const exceeded = ": constant string may exceed 1048576 bytes"
	for name, c := range map[string]struct{ source, want string }{
		"one":            {"const c = host.Big + \"x\"\nfunc Run() int { return len(c) }", ""},
		"two":            {"const c = host.Big + host.Big\nfunc Run() int { return len(c) }", "main.go:2:11" + exceeded},
		"through a name": {"const c = host.Big\nvar s = c + c\nfunc Run() int { return len(s) }", "main.go:3:9" + exceeded},
		// Small is first taken to be as long as Big, then looked up by name.
		"other names": {"func Run() int { return len(host.Small + host.Small + host.Small) }", ""},
	} {
		if got := compileError(prefix+c.source, host); got != c.want {
			t.Errorf("%s: got %.200s, want %s", name, got, c.want)
		}
	}
	if got := compileError("import . \"example.test/host\"\nconst c = Big + Big\nfunc Run() int { return len(c) }", host); got != "main.go:2:11"+exceeded {
		t.Error(got)
	}

	// Constants that a host declares in Go source are estimated from it.
	source := evm.Package{Path: "example.test/source", Source: "package source\n\nconst half = \"" + strings.Repeat("x", 300000) + "\"\n\nconst (\n\tBig   = half + \"" + strings.Repeat("y", 300000) + "\"\n\tSmall = \"ab\"\n\tCount = iota + 1\n)\n"}
	for name, c := range map[string]struct{ source, want string }{
		"one":         {"const c = source.Big + \"x\"\nfunc Run() int { return len(c) }", ""},
		"two":         {"const c = source.Big + source.Big\nfunc Run() int { return len(c) }", "main.go:2:11" + exceeded},
		"other names": {"func Run() int { return len(source.Small+source.Small+source.Small+source.Small) + source.Count + source.Count + source.Count + source.Count }", ""},
	} {
		if got := compileError("import \"example.test/source\"\n"+c.source, source); got != c.want {
			t.Errorf("source package, %s: got %.200s, want %s", name, got, c.want)
		}
	}

	// A host constant past the limit cannot enter a constant expression.
	huge := evm.Package{Path: "example.test/host", Constants: []evm.ExternalConstant{{Name: "Huge", Untyped: constant.MakeString(strings.Repeat("x", evmcompiler.MaxConstStringSize+1))}}}
	for name, c := range map[string]struct{ source, want string }{
		"value":    {"func Run() int { s := host.Huge; return len(s) }", ""},
		"sum":      {"func Run() int { s := host.Huge + \"\"; return len(s) }", "main.go:2:23" + exceeded},
		"constant": {"const a = host.Huge\nconst b = a + a\nfunc Run() int { return len(b) }", "main.go:2:11" + exceeded},
	} {
		if got := compileError(prefix+c.source, huge); got != c.want {
			t.Errorf("huge constant, %s: got %.200s, want %s", name, got, c.want)
		}
	}
	// The built-in time package is not among the host's packages.
	layouts := "import \"time\"\nconst a = time.RFC3339 + time.RFC3339\n" + doublings("a", 20) + "func Run() int { return len(a20) }"
	if got := compileError(layouts); !strings.HasSuffix(got, exceeded) {
		t.Error(got)
	}
}

// The type checker already bounds numeric constants: integers to 512 bits,
// shift counts to about a thousand, literals to a few thousand characters, and
// floats to a fixed precision. These sources stay cheap without a limit here.
func TestNumericConstantsStayBounded(t *testing.T) {
	squares := func(first string, n int) string {
		var b strings.Builder
		b.WriteString("const c0 = " + first + "\n")
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&b, "const c%d = c%d * c%d\n", i, i-1, i-1)
		}
		return b.String() + fmt.Sprintf("func Run() bool { return c%d > 0 }", n)
	}
	for name, c := range map[string]struct {
		source   string
		rejected bool
	}{
		"huge shift":        {"const a = 1 << 1000000000\nfunc Run() bool { return a > 0 }", true},
		"shifted shifts":    {"const a = 1 << 500\nconst b = a << 500\nfunc Run() bool { return b > 0 }", true},
		"integer squares":   {squares("3", 2000), true},
		"float squares":     {squares("1.5", 2000), true},
		"rational squares":  {squares("(1.0/3)", 2000), false},
		"huge exponent":     {"const a = 1e1000000000\nfunc Run() bool { return a > 0 }", true},
		"long literal":      {"const a = " + strings.Repeat("9", 100000) + "\nfunc Run() bool { return a > 0 }", true},
		"huge array length": {"const n = 1 << 40\nvar x [n]byte\nfunc Run() bool { return len(x) > 0 }", true},
	} {
		got := compileError(c.source)
		if (got != "") != c.rejected || strings.Contains(got, "internal compiler error") {
			t.Errorf("%s: %.200s", name, got)
		}
	}
}

func TestLoweredStmtNesting(t *testing.T) {
	ifs := func(n int) string {
		return "func Run() int { a := 1; " + strings.Repeat("if a > 0 {", n) + "a++" + strings.Repeat("}", n) + "; return a }"
	}
	lowered := evmcompiler.Options{LoweredStmtNesting: 4}
	if _, err := lowered.Compile("main.go", ifs(104)); err != nil {
		t.Fatal(err)
	}
	if _, err := lowered.Compile("main.go", ifs(105)); err == nil || err.Error() != "main.go:1:1066: statements nest more than 100 levels" {
		t.Fatal(err)
	}

	files := token.NewFileSet()
	file, err := parser.ParseFile(files, "main.go", "package plugin\n"+ifs(104), parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if _, info, err := lowered.AnalyzeFiles(files, []*ast.File{file}); info == nil || err != nil {
		t.Fatal(err)
	}
	_, info, err := evmcompiler.AnalyzeFiles(files, []*ast.File{file})
	if info != nil || err == nil || err.Error() != "main.go:2:1026: statements nest more than 100 levels" {
		t.Fatal(err)
	}
}
