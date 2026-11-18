package evm_test

import (
	"context"
	"strings"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
)

func TestRunEntryNative(t *testing.T) {
	module, err := evmcompiler.Compile("bonus.go", `
import "strings"

var calls int

func Bonus(name string, points int64, vip bool, rate float64) (string, int64) {
	calls++
	if vip {
		points *= 2
	}
	return strings.ToUpper(name), points + int64(rate*10) + int64(calls)
}
`)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module)
	if err != nil {
		t.Fatal(err)
	}
	vm := evm.New(program)
	var out [2]any
	if err := vm.RunEntryNative(context.Background(), "Bonus", []any{"ada", int64(100), true, 1.5}, out[:], evm.Budget{}); err != nil {
		t.Fatal(err)
	}
	if out[0] != "ADA" || out[1] != int64(216) {
		t.Fatalf("got %#v", out)
	}
	// Globals persist across entries on one VM, as with Run.
	if err := vm.RunEntryNative(context.Background(), "Bonus", []any{"bob", int64(1), false, 0.0}, out[:], evm.Budget{}); err != nil {
		t.Fatal(err)
	}
	if out[0] != "BOB" || out[1] != int64(3) {
		t.Fatalf("got %#v", out)
	}

	// Scalar results convert by their static type; the script uses none of
	// them in an interface.
	plain, err := evmcompiler.Compile("plain.go", `
func Int() int             { return 42 }
func Text() string         { return "ok" }
func Flag() bool           { return true }
func Ratio() float64       { return 1.5 }
func Small() uint8         { return 200 }
func Pair() (int64, string) { return -7, "x" }
`)
	if err != nil {
		t.Fatal(err)
	}
	plainProgram, err := evm.Link(plain)
	if err != nil {
		t.Fatal(err)
	}
	for entry, want := range map[string][]any{
		"Int": {42}, "Text": {"ok"}, "Flag": {true}, "Ratio": {1.5}, "Small": {uint8(200)}, "Pair": {int64(-7), "x"},
	} {
		got := make([]any, len(want))
		if err := evm.New(plainProgram).RunEntryNative(context.Background(), entry, nil, got, evm.Budget{}); err != nil {
			t.Errorf("%s: %v", entry, err)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s result %d: got %#v, want %#v", entry, i, got[i], want[i])
			}
		}
	}

	for name, tc := range map[string]struct {
		entry string
		args  []any
		want  string
	}{
		"unknown entry":  {"Missing", nil, `unknown entry "Missing"`},
		"too few":        {"Bonus", []any{"ada"}, "got 1 arguments for 4 parameters"},
		"wrong type":     {"Bonus", []any{"ada", "100", true, 1.5}, "argument 1: Go value of type string does not match parameter type i64"},
		"no conversions": {"Bonus", []any{"ada", 100, true, 1.5}, "argument 1: Go value of type int does not match parameter type i64"},
	} {
		fresh := evm.New(program)
		err := fresh.RunEntryNative(context.Background(), tc.entry, tc.args, out[:], evm.Budget{})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want error containing %q, got %v", name, tc.want, err)
		}
	}
}
