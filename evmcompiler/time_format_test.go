package evmcompiler_test

import (
	"context"
	"testing"
	"time"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
)

func TestNativeTimestampFormatting(t *testing.T) {
	module, err := evmcompiler.Compile("time.go", `import "time"
func Format(t time.Time)string{return t.Format(time.RFC3339)+"|"+t.Format(time.RFC3339Nano)+"|"+t.Format("2006-01-02")}`)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module)
	if err != nil {
		t.Fatal(err)
	}
	for _, stamp := range []time.Time{{}, time.Date(2026, 11, 13, 17, 0, 0, 123456789, time.UTC), time.Date(2026, 11, 14, 1, 0, 0, 123, time.FixedZone("shop", 14400)), time.Now()} {
		var output [1]values.Value
		if err := evmtest.New(program).Run(context.Background(), "Format", []values.Value{values.NativeTime(stamp)}, output[:]); err != nil {
			t.Fatal(err)
		}
		want := stamp.Format(time.RFC3339) + "|" + stamp.Format(time.RFC3339Nano) + "|" + stamp.Format("2006-01-02")
		if got := output[0].AsString(); got != want {
			t.Fatalf("%q != %q", got, want)
		}
	}
}
