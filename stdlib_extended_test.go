package evm_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

type limitError struct{ n int }

func (e *limitError) Error() string { return "limit " + strconv.Itoa(e.n) }

// The built-in packages agree with Go on the additions made for parity.
func TestBuiltinPackageParity(t *testing.T) {
	module, err := evmcompiler.Compile("parity.go", `
import ("errors"; "fmt"; "math"; "strconv"; "strings")

type limitError struct{ n int }
func (e *limitError) Error() string { return "limit " + strconv.Itoa(e.n) }

func Run() string {
	var out []string
	add := func(v ...any) { out = append(out, fmt.Sprint(v...)) }

	frac, exp := math.Frexp(48)
	lg, sign := math.Lgamma(-0.5)
	add(math.MaxInt64, math.MinInt8, uint64(math.MaxUint64), math.MaxUint16, math.Gamma(5), math.Ldexp(0.75, 3), frac, exp, math.Ilogb(1000), math.Logb(0.1), lg, sign, math.J0(1), math.Yn(2, 3), math.Erfinv(0.5), math.Nextafter32(1, 2))

	add(strconv.QuoteRune('☺'), strconv.QuoteRuneToASCII('☺'), strconv.QuoteToGraphic("a\tb\u00a0"), strconv.QuoteRuneToGraphic('\u00a0'), strconv.CanBackquote("a\x60b"), strconv.CanBackquote("ok"), strconv.IsPrint('\u00ad'), strconv.IsGraphic('\u3000'), strconv.IntSize)
	add(string(strconv.AppendInt([]byte("n="), -42, 16)), string(strconv.AppendQuote(nil, "x\"")), string(strconv.AppendBool(nil, true)), string(strconv.AppendFloat(nil, 1.5, 'f', 2, 64)), string(strconv.AppendQuoteRune(nil, 'é')), string(strconv.AppendUint(nil, 255, 2)))

	upper := func(r rune) bool { return r >= 'A' && r <= 'Z' }
	add(strings.IndexFunc("héLLo", upper), strings.LastIndexFunc("héLLo", upper), strings.ContainsFunc("abc", upper), strings.FieldsFunc("a1b22c", func(r rune) bool { return r >= '0' && r <= '9' }), strings.Map(func(r rune) rune { if r == 'l' { return -1 }; return r + 1 }, "héllo"))
	add(strings.TrimFunc("XXhéYY", upper), "|", strings.TrimLeftFunc("ABc", upper), "|", strings.TrimRightFunc("cé☺AB", upper), "|", strings.ToValidUTF8("a\xff\xfeb\xffc", "?"))
	before, after, found := strings.CutLast("a/b/c", "/")
	_, _, missing := strings.CutLast("abc", "/")
	add(before, after, found, missing)
	var lines, parts, fields []string
	for l := range strings.Lines("one\ntwo\n\nthree") { lines = append(lines, strconv.Quote(l)) }
	for p := range strings.SplitSeq("a,b,,c", ",") { parts = append(parts, "["+p+"]") }
	for p := range strings.SplitAfterSeq("a,b", ",") { parts = append(parts, "<"+p+">") }
	for p := range strings.SplitSeq("hé", "") { parts = append(parts, "("+p+")") }
	for f := range strings.FieldsSeq("  go \t is\nfun ") { fields = append(fields, f) }
	add(lines, parts, fields)
	var b strings.Builder
	b.WriteString("hé")
	b.WriteByte('!')
	b.WriteRune('☺')
	b.Write([]byte("ok"))
	b.Grow(100)
	add(b.String(), b.Len(), b.Cap() >= 100)
	b.Reset()
	r := strings.NewReplacer("a", "1", "aa", "2", "<", "&lt;")
	add(b.Len(), r.Replace("aaa<b>"), strings.NewReplacer("", "-").Replace("hé"), strings.NewReplacer("x", "").Replace("axbx"))

	add(string(fmt.Append([]byte("v="), 1, "a", 2)), string(fmt.Appendf(nil, "%03d|%s", 7, "x")), string(fmt.Appendln(nil, "a", 1)))

	wrapped := fmt.Errorf("outer: %w", errors.Join(errors.New("plain"), &limitError{3}))
	limit, ok := errors.AsType[*limitError](wrapped)
	_, none := errors.AsType[*limitError](errors.New("plain"))
	add(ok, limit.n, none)
	return strings.Join(out, "\x1e")
}`)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module)
	if err != nil {
		t.Fatal(err)
	}
	var result [1]values.Value
	if err := evm.New(program).Run(context.Background(), "Run", nil, result[:]); err != nil {
		t.Fatal(err)
	}

	var out []string
	add := func(v ...any) { out = append(out, fmt.Sprint(v...)) }
	frac, exp := math.Frexp(48)
	lg, sign := math.Lgamma(-0.5)
	add(math.MaxInt64, math.MinInt8, uint64(math.MaxUint64), math.MaxUint16, math.Gamma(5), math.Ldexp(0.75, 3), frac, exp, math.Ilogb(1000), math.Logb(0.1), lg, sign, math.J0(1), math.Yn(2, 3), math.Erfinv(0.5), math.Nextafter32(1, 2))
	add(strconv.QuoteRune('☺'), strconv.QuoteRuneToASCII('☺'), strconv.QuoteToGraphic("a\tb\u00a0"), strconv.QuoteRuneToGraphic('\u00a0'), strconv.CanBackquote("a\x60b"), strconv.CanBackquote("ok"), strconv.IsPrint('\u00ad'), strconv.IsGraphic('\u3000'), strconv.IntSize)
	add(string(strconv.AppendInt([]byte("n="), -42, 16)), string(strconv.AppendQuote(nil, "x\"")), string(strconv.AppendBool(nil, true)), string(strconv.AppendFloat(nil, 1.5, 'f', 2, 64)), string(strconv.AppendQuoteRune(nil, 'é')), string(strconv.AppendUint(nil, 255, 2)))
	upper := func(r rune) bool { return r >= 'A' && r <= 'Z' }
	add(strings.IndexFunc("héLLo", upper), strings.LastIndexFunc("héLLo", upper), strings.ContainsFunc("abc", upper), strings.FieldsFunc("a1b22c", unicode.IsDigit), strings.Map(func(r rune) rune {
		if r == 'l' {
			return -1
		}
		return r + 1
	}, "héllo"))
	add(strings.TrimFunc("XXhéYY", upper), "|", strings.TrimLeftFunc("ABc", upper), "|", strings.TrimRightFunc("cé☺AB", upper), "|", strings.ToValidUTF8("a\xff\xfeb\xffc", "?"))
	before, after, found := strings.CutLast("a/b/c", "/")
	_, _, missing := strings.CutLast("abc", "/")
	add(before, after, found, missing)
	var lines, parts, fields []string
	for l := range strings.Lines("one\ntwo\n\nthree") {
		lines = append(lines, strconv.Quote(l))
	}
	for p := range strings.SplitSeq("a,b,,c", ",") {
		parts = append(parts, "["+p+"]")
	}
	for p := range strings.SplitAfterSeq("a,b", ",") {
		parts = append(parts, "<"+p+">")
	}
	for p := range strings.SplitSeq("hé", "") {
		parts = append(parts, "("+p+")")
	}
	for f := range strings.FieldsSeq("  go \t is\nfun ") {
		fields = append(fields, f)
	}
	add(lines, parts, fields)
	var b strings.Builder
	b.WriteString("hé")
	b.WriteByte('!')
	b.WriteRune('☺')
	b.Write([]byte("ok"))
	b.Grow(100)
	add(b.String(), b.Len(), b.Cap() >= 100)
	b.Reset()
	r := strings.NewReplacer("a", "1", "aa", "2", "<", "&lt;")
	add(b.Len(), r.Replace("aaa<b>"), strings.NewReplacer("", "-").Replace("hé"), strings.NewReplacer("x", "").Replace("axbx"))
	add(string(fmt.Append([]byte("v="), 1, "a", 2)), string(fmt.Appendf(nil, "%03d|%s", 7, "x")), string(fmt.Appendln(nil, "a", 1)))
	wrapped := fmt.Errorf("outer: %w", errors.Join(errors.New("plain"), &limitError{3}))
	limit, ok := errors.AsType[*limitError](wrapped)
	_, none := errors.AsType[*limitError](errors.New("plain"))
	add(ok, limit.n, none)

	got, want := strings.Split(result[0].AsString(), "\x1e"), out
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			actual := "(missing)"
			if i < len(got) {
				actual = got[i]
			}
			t.Errorf("line %d:\n got  %s\n want %s", i+1, actual, want[i])
		}
	}
}
