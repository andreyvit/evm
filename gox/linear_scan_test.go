package gox

import (
	"math/rand/v2"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// editSource applies edits in one pass. The reference applies them one by
// one, which defines the result for edits that touch or overlap, down to
// the panic when an earlier edit leaves the text too short for a later one.
func TestEditSourceMatchesSequentialEdits(t *testing.T) {
	sequential := func(source string, edits []sourceEdit) string {
		slices.SortStableFunc(edits, func(a, b sourceEdit) int {
			if a.start != b.start {
				return b.start - a.start
			}
			return b.end - a.end
		})
		for _, e := range edits {
			source = source[:e.start] + e.text + source[e.end:]
		}
		return source
	}
	run := func(f func(string, []sourceEdit) string, source string, edits []sourceEdit) (result string) {
		defer func() {
			if recover() != nil {
				result = "panic"
			}
		}()
		return f(source, slices.Clone(edits))
	}
	const source = "abcdefghijklmnopqrstuvwxyz0123456789"
	r := rand.New(rand.NewPCG(1, 2))
	overlapping := 0
	for range 5000 {
		edits := make([]sourceEdit, r.IntN(6))
		for i := range edits {
			start := r.IntN(len(source) + 1)
			end := start
			if r.IntN(2) == 0 {
				end = min(len(source), start+r.IntN(4))
			}
			edits[i] = sourceEdit{start, end, strings.Repeat(string(rune('A'+i)), r.IntN(3))}
		}
		want := run(sequential, source, edits)
		if got := run(editSource, source, edits); got != want {
			t.Fatalf("%v: got %q, want %q", edits, got, want)
		}
		for i, a := range edits {
			for _, b := range edits[:i] {
				if a.start < b.end && b.start < a.end {
					overlapping++
				}
			}
		}
	}
	if overlapping < 100 {
		t.Fatal("the edits rarely overlap; the fallback is not tested")
	}
}

func TestPreprocessorPositionResumes(t *testing.T) {
	const source = "one\n\ntwo three\nfour\n"
	p := &preprocessor{filename: "page.gox", source: source}
	r := rand.New(rand.NewPCG(3, 4))
	offsets := []int{0, 3, 4, 5, len(source), 0}
	for range 200 {
		offsets = append(offsets, r.IntN(len(source)+1))
	}
	for _, i := range offsets {
		prefix := source[:i]
		want := evmPos{"page.gox", strings.Count(prefix, "\n") + 1, i - strings.LastIndexByte(prefix, '\n')}
		if got := p.position(i); got != want {
			t.Fatalf("offset %d: got %+v, want %+v", i, got, want)
		}
	}
}

// Many tags on one level used to cost the formatter and the syntax check
// memory in proportion to the square of their number: 3000 of them allocated
// 1.5 to 3.5 GB in all, and now allocate under 100 MB.
func TestManyTagsFormatInLinearMemory(t *testing.T) {
	const header = "package plugin\n\nimport \"github.com/andreyvit/evm/gox\"\n\nfunc Page(b *gox.Builder, x string) {\n"
	for name, body := range map[string]string{
		"elements":       strings.Repeat("\t<div class=\"a\">\"x\"</div>\n", 3000),
		"styles":         strings.Repeat("\t<style>a{}</style>\n", 3000),
		"interpolations": "\t<style>" + strings.Repeat("{=x}\n", 3000) + "</style>\n",
	} {
		source := header + body + "}\n"
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		got, err := FormatSource("page.gox", source)
		runtime.ReadMemStats(&after)
		if err != nil || got != source {
			t.Fatalf("%s: %v\n%.300s", name, err, got)
		}
		if allocated := (after.TotalAlloc - before.TotalAlloc) >> 20; allocated > 400 {
			t.Errorf("%s: allocated %d MB", name, allocated)
		}
	}
}
