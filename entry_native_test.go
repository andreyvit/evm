package evm_test

import (
	"context"
	"github.com/andreyvit/evm/values"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
)

func TestRunMixedArgumentsAndResults(t *testing.T) {
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
	fn, err := program.LookupFunction("Bonus", &evm.Signature{Params: []*values.Type{values.StringType, values.Int64, values.BoolType, values.Float64}, Results: []*values.Type{values.StringType, values.Int64}})
	if err != nil {
		t.Fatal(err)
	}
	vm := program.AcquireVM(context.Background())
	defer vm.Release()
	slots := [6]values.Value{values.String("ada"), values.Int(100), values.Bool(true), values.Float(1.5)}
	if err := vm.Run(fn, slots[:]); err != nil || slots[4].AsString() != "ADA" || slots[5].AsInt() != 216 {
		t.Fatal(slots, err)
	}
	if err := vm.Run(fn, slots[:4]); err == nil {
		t.Fatal("accepted missing result slots")
	}
}
