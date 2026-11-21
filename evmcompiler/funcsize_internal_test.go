package evmcompiler

import (
	"fmt"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"golang.org/x/tools/go/ssa"
)

// The estimate is made from syntax and the cost is in the SSA form. For
// functions that are all of one construct, the estimate must not fall short
// of the blocks and phi operands the builder makes, nor exceed them by much.
func TestFuncSizeFollowsSSA(t *testing.T) {
	lines := func(n int, format string) string {
		var b strings.Builder
		for i := range n {
			b.WriteString(strings.ReplaceAll(format, "#", fmt.Sprint(i)))
		}
		return b.String()
	}
	const n = 60
	sum := "s := 0\n" + lines(n, "s += v#\n") + "return s }"
	for name, source := range map[string]string{
		"ifs":              "func F(x int) int { y := 0\n" + lines(n, "if x > # { y++ }\n") + "return y }",
		"conditions":       "func F(x, z int) int { y := 0\n" + lines(n, "if x > # && z < # || x == z { y++ }\n") + "return y }",
		"else ifs":         "func F(x int) int { y := 0\nif x < 0 { y++ }" + lines(n, " else if x == # { y++ }") + "\nreturn y }",
		"cases":            "func F(x int) int { y := 0\nswitch x {\n" + lines(n, "case #: y += #\n") + "}\nreturn y }",
		"conditions cases": "func F(x int) int { y := 0\nswitch {\n" + lines(n, "case x > #, x < -#: y += #\n") + "}\nreturn y }",
		"type cases":       "func F(x any) int { y := 0\nswitch x := x.(type) {\ncase int: y += x\ncase string: y += len(x)\ncase []int: y += len(x)\n}\nreturn y }",
		"loops":            "func F(x int) int { y := 0\n" + lines(n, "for i := 0; i < x; i++ { y += i }\n") + "return y }",
		"ranges":           "func F(xs []int) int { y := 0\n" + lines(n, "for _, v := range xs { y += v }\n") + "return y }",
		"breaks":           "func F(x int) int { y := 0\nfor {\n" + lines(n, "if x == # { break }\ny++\n") + "}\nreturn y }",
		"continues":        "func F(x int) int { y := 0\nfor i := 0; i < x; i++ {\n" + lines(n, "if x == # { continue }\ny++\n") + "}\nreturn y }",
		"labels":           "func F(x int) int { y := 0\nouter:\nfor {\nfor {\n" + lines(n, "if x == # { continue outer }\ny++\n") + "break outer\n}\n}\nreturn y }",
		"gotos":            "func F(x int) int { y := 0\n" + lines(n, "L#:\ny++\n") + lines(n, "if y == # { goto L# }\n") + "return y }",
		"locals":           "func F(x int) int {\n" + lines(n, "v# := x + #\nif x > # { v#++ }\n") + sum,
		"comma ok":         "func F(m map[string]int) int { s := 0\n" + lines(n, "if v, ok := m[\"k#\"]; ok { s += v }\n") + "return s }",
		"variables cases":  "func F(x int) int {\n" + lines(n, "v# := x\n") + "switch x {\n" + lines(n, "case #: v#++\n") + "}\n" + sum,
		"variables loop":   "func F(x int) int {\n" + lines(n, "v# := x\n") + "for i := 0; i < x; i++ {\n" + lines(n, "v#++\nif x == # { continue }\n") + "}\n" + sum,
		"variables nested": "func F(x int) int {\n" + lines(n, "v# := 0\n") + strings.Repeat("for i := 0; i < x; i++ {\n", 8) + lines(n, "v#++\n") + strings.Repeat("}\n", 8) + sum,
		"terms":            "func F(x int) bool { return " + strings.TrimSuffix(lines(n, "x > # && "), " && ") + " }",
	} {
		files := token.NewFileSet()
		file, err := parser.ParseFile(files, "main.go", "package p\n"+source, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(name, err)
		}
		c := nestingChecker{maxStmt: MaxStmtNesting}
		c.check(file)
		estimate := c.sizes.last

		blocks, operands := 0, 0
		plan := checkOwnership
		checkOwnership = func(f *lowerer) {
			if f.fn.Pkg != f.c.pkg || len(f.fn.Blocks) <= blocks {
				return
			}
			blocks, operands = len(f.fn.Blocks), 0
			for _, b := range f.fn.Blocks {
				for _, ins := range b.Instrs {
					if phi, ok := ins.(*ssa.Phi); ok {
						operands += len(phi.Edges)
					}
				}
			}
		}
		_, err = Options{NoOptimization: true}.Compile("main.go", source)
		checkOwnership = plan
		if err != nil {
			t.Fatal(name, err)
		}
		if estimate.blocks*10 < blocks*9 || estimate.blocks > 2*blocks+8 {
			t.Errorf("%s: %d blocks estimated, %d built", name, estimate.blocks, blocks)
		}
		if estimate.edges < operands || estimate.edges > 3*operands+4*n {
			t.Errorf("%s: %d phi operands estimated, %d built", name, estimate.edges, operands)
		}
	}
}
