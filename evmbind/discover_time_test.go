package evmbind_test

import (
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmcompiler"
	"testing"
	"time"
)

func TestDiscoverUsesBuiltinTime(t *testing.T) {
	_, packages, err := evmbind.Discover("api", evmbind.Discovery{Options: evmbind.Options{Functions: map[string]any{
		"Stamp": func() time.Time { return time.Time{} },
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evmcompiler.Compile("test.go", `import "api";func Run() bool { return api.Stamp().IsZero() }`, packages...); err != nil {
		t.Fatal(err)
	}
}
