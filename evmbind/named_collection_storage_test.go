package evmbind_test

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

// A script-declared slice or map type shares storage with its underlying
// type, also when a host binds that underlying type natively.
func TestScriptNamedCollectionsShareNativeStorage(t *testing.T) {
	schema, err := evmbind.New("nat", fstest.MapFS{"nat.go": {Data: []byte(`package nat
func Ints() []int
func Sum(x []int) int
func Count(m map[string]int) int
`)}}, evmbind.Options{Functions: map[string]any{
		"Ints": func() []int { return []int{4, 5, 6} },
		"Sum": func(x []int) (n int) {
			for _, v := range x {
				n += v
			}
			return n
		},
		"Count": func(m map[string]int) int { m["seen"]++; return len(m) },
	}})
	if err != nil {
		t.Fatal(err)
	}
	lib := evm.Package{Path: "lib", Source: `package lib

type IntSlice []int

func (x IntSlice) Swap(i, j int) { x[i], x[j] = x[j], x[i] }
func (x IntSlice) At(i int) int   { return x[i] }

type Counts map[string]int

func (c Counts) Add(key string) { c[key]++ }
`}
	packages := []evm.Package{schema.Bind(nil), lib}
	for name, tc := range map[string]struct{ body, want string }{
		"script slice":  {`ints := []int{1, 2, 3}; lib.IntSlice(ints).Swap(0, 2); return fmt.Sprint(ints, nat.Sum(ints))`, "[3 2 1] 6"},
		"native slice":  {`ints := nat.Ints(); x := lib.IntSlice(ints); x.Swap(0, 2); return fmt.Sprint(ints, x.At(1), nat.Sum(x))`, "[6 5 4] 5 15"},
		"named literal": {`x := lib.IntSlice{1, 2, 3}; x = append(x, 4); x.Swap(0, 3); return fmt.Sprint(x, []int(x)[0], nat.Sum(x))`, "[4 2 3 1] 4 10"},
		"local type":    {`type mine []int; m := mine(nat.Ints()); m[0], m[2] = m[2], m[0]; return fmt.Sprint(m, lib.IntSlice(m).At(0), nat.Sum(m[1:]))`, "[6 5 4] 6 9"},
		"named map":     {`c := lib.Counts{"a": 1}; c.Add("a"); n := nat.Count(c); m := map[string]int(c); return fmt.Sprint(n, m["a"], c["seen"], nat.Count(m), c["seen"])`, "2 2 1 2 2"},
	} {
		module, err := evmcompiler.Compile("s.go", "import (\"lib\"; \"nat\"; \"fmt\")\nfunc Run() string { "+tc.body+" }", packages...)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		program, err := evm.Link(module, packages...)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		var out [1]values.Value
		if err := evm.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got := out[0].AsString(); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}
