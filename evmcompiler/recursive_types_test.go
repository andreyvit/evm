package evmcompiler_test

import (
	"context"
	"testing"

	"github.com/andreyvit/evm/values"
)

func TestGoTypesThatContainThemselves(t *testing.T) {
	tests := []struct {
		name, source string
		want         int64
	}{
		{"slice", `type T []T
func Run() int { var x T; x = append(x, T{}); x = append(x, x); return len(x) }`, 2},
		{"unused slice", `type T []T
func Run() int { return 1 }`, 1},
		{"slice in interface", `type T []T
func Run() int { var x T; var a any = append(x, T{}); y, _ := a.(T); return len(y) }`, 1},
		{"slice walked recursively", `type T []T
func depth(x T) int { d := 0; for _, c := range x { d = max(d, depth(c)) }; return d + 1 }
func Run() int { var x T; x = append(x, T{T{}}); return depth(x) }`, 3},
		{"copied slice", `type T []T
func Run() int { x := make(T, 3); y := make(T, 3); copy(y, x); x[0] = y; return len(x[0]) }`, 3},
		{"two slices of each other", `type A []B
type B []A
func Run() int { var a A; a = append(a, B{}); a[0] = append(a[0], a); return len(a) + len(a[0]) }`, 2},
		{"array of pointers", `type T [2]*T
func Run() int { var x T; x[0] = &x; if x[0] == nil { return 0 }; return 1 }`, 1},
		{"slice of maps", `type T []map[string]T
func Run() int { var x T; x = append(x, map[string]T{"a": nil}); return len(x) }`, 1},
		{"struct with slice", `type T struct{ X []T }
func Run() int { x := T{}; x.X = append(x.X, T{}); return len(x.X) }`, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out := make([]values.Value, 1)
			if err := compileGoVM(t, test.source).Run(context.Background(), "Run", nil, out); err != nil {
				t.Fatal(err)
			}
			if got := out[0].AsInt(); got != test.want {
				t.Fatalf("got %d, want %d", got, test.want)
			}
		})
	}
}
