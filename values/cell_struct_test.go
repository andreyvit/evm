package values

import "testing"

func TestCompactOptional(t *testing.T) {
	for _, elem := range []*Shape{{Type: Int64, Kind: IntKind, Cells: 1}, {Type: Float64, Kind: FloatKind, Cells: 1}, {Type: StringType, Kind: StringKind, Cells: 1}} {
		cell := CompactOptional("test.Optional", elem)
		vals := []Value{{}, {R: 1}, {R: ^uint64(0)}, {R: uint64(6) << 60}, {R: uint64(1) << 63}}
		if elem.Kind == StringKind {
			vals = []Value{String(""), String("hello"), String("<b>hello</b>")}
		}
		for _, v := range vals {
			for _, present := range []bool{false, true} {
				fields := [2]Value{v, Bool(present)}
				got := cell.Pack(fields)
				if cell.Get(got, 0) != v || cell.Get(got, 1).R != fields[1].R {
					t.Fatalf("%s %+v %v: %+v", elem.Type.Name(), v, present, got)
				}
				if n := testing.AllocsPerRun(100, func() { cell.Pack(fields) }); n != 0 {
					t.Fatalf("%g allocations", n)
				}
			}
		}
	}
}
