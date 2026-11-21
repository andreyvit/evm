package evmcompiler_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

func TestCompilePanicBecomesInternalError(t *testing.T) {
	box := evm.Package{Path: "box", Source: "package box\ntype Box[T any] struct{ V T }",
		StructValues: map[string]func(string, []*values.Shape) *values.CellStruct{
			"Box": func(string, []*values.Shape) *values.CellStruct { panic("unsupported box") },
		}}
	_, err := evmcompiler.Compile("main.go", `import "box"
func Run() int { var b box.Box[int]; return b.V }`, box)
	var internal *evmcompiler.InternalError
	if !errors.As(err, &internal) {
		t.Fatalf("got %v, want an internal error", err)
	}
	if got := err.Error(); got != "internal compiler error: unsupported box" {
		t.Fatal(got)
	}
	if !strings.Contains(string(internal.Stack), "TestCompilePanicBecomesInternalError") {
		t.Fatalf("stack does not reach the panic:\n%s", internal.Stack)
	}
}
