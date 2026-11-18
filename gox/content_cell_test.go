package gox

import (
	"github.com/andreyvit/evm/values"
	"math"
	"testing"
)

func TestContentCell(t *testing.T) {
	s := Schema().Package()
	var shape *values.Shape
	for _, d := range s.Types {
		if d.Name == "Content" {
			shape = d.Shape
		}
	}
	if shape == nil || shape.Cells != 1 || shape.Type.Repr() != values.Pair {
		t.Fatal("Content must occupy one pair")
	}
	table := values.NewTypeTable([]values.DynamicType{{}, {Shape: shape, Comparable: true}})
	for _, content := range []Content{{}, Text("<plain>"), TrustedHTML("<b>trusted</b>"), TrustedHTML("")} {
		cell := contentCell.pack(content)
		if got := contentCell.unpack(cell); got != content {
			t.Fatalf("%+v != %+v", got, content)
		}
		if n := testing.AllocsPerRun(100, func() {
			boxed, ok := table.Inline(1, contentCell.pack(content))
			if !ok {
				panic("boxed")
			}
			_, raw, err := table.Unpack(boxed)
			if err != nil || contentCell.unpack(raw) != content {
				panic("round trip")
			}
		}); n != 0 {
			t.Fatalf("%g allocations", n)
		}
	}
	if contentLength(math.MaxUint32) != math.MaxUint32 {
		t.Fatal("max length")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("oversize length accepted")
		}
	}()
	contentLength(math.MaxUint32 + 1)
}
