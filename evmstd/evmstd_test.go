package evmstd_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"math/bits"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmstd"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
)

func run(t *testing.T, script string) string {
	t.Helper()
	packages := evmstd.Packages()
	module, err := evmcompiler.Compile("script.go", script, packages...)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	program, err := evm.Link(module, packages...)
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	var out [1]values.Value
	if err := evmtest.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatalf("run: %v", err)
	}
	return out[0].AsString()
}

// Each script uses a package the way ordinary Go code does; the expected
// string is computed by the same calls made natively.
func TestNativePackages(t *testing.T) {
	re := regexp.MustCompile(`(\w+)@(\w+)\.com`)
	u, _ := url.Parse("https://ada:pw@example.com:8080/a%20b/c?x=1&y=2#frag")
	q := u.Query()
	q.Add("z", "a b")
	day := time.Date(2025, time.March, 1, 13, 4, 5, 0, time.UTC)
	later := day.Add(36*time.Hour+30*time.Minute).AddDate(0, 1, 0)
	parsed, _ := time.Parse(time.DateOnly, "2024-02-29")
	var buf bytes.Buffer
	buf.WriteString("héllo ")
	buf.WriteByte('w')
	buf.Write([]byte("orld"))
	for name, tc := range map[string]struct{ script, want string }{
		"path": {`import ("path"; "fmt")
func Run() string {
	dir, file := path.Split("a/b/c.txt")
	ok, err := path.Match("*.txt", file)
	_, bad := path.Match("[", "x")
	return fmt.Sprint(path.Join("a", "../b", "c"), path.Base("/x/y.go"), path.Ext("y.tar.gz"), path.Clean("a//b/./c/.."), path.IsAbs("/a"), dir, file, ok, err, bad == path.ErrBadPattern, path.Dir("/a/b"))
}`, func() string {
			dir, file := path.Split("a/b/c.txt")
			ok, err := path.Match("*.txt", file)
			_, bad := path.Match("[", "x")
			return fmt.Sprint(path.Join("a", "../b", "c"), path.Base("/x/y.go"), path.Ext("y.tar.gz"), path.Clean("a//b/./c/.."), path.IsAbs("/a"), dir, file, ok, err, bad == path.ErrBadPattern, path.Dir("/a/b"))
		}()},
		"filepath": {`import ("path/filepath"; "fmt")
func Run() string {
	rel, err := filepath.Rel("/a/b", "/a/c/d")
	return fmt.Sprint(filepath.Join("a", "b"), filepath.Base("a/b.go"), filepath.Ext("b.go"), rel, err, filepath.IsLocal("../x"), filepath.SplitList("a:b"), filepath.ToSlash("a/b"))
}`, func() string {
			rel, err := filepath.Rel("/a/b", "/a/c/d")
			return fmt.Sprint(filepath.Join("a", "b"), filepath.Base("a/b.go"), filepath.Ext("b.go"), rel, err, filepath.IsLocal("../x"), filepath.SplitList("a:b"), filepath.ToSlash("a/b"))
		}()},
		"html": {`import "html"
func Run() string { return html.EscapeString("<a href='x'>&</a>") + html.UnescapeString("&lt;&eacute;&#39;") }`,
			html.EscapeString("<a href='x'>&</a>") + html.UnescapeString("&lt;&eacute;&#39;")},
		"regexp": {"import (\"regexp\"; \"fmt\"; \"strings\")\nfunc Run() string {\n\tre := regexp.MustCompile(`(\\w+)@(\\w+)\\.com`)\n\t_, err := regexp.Compile(\"(\")\n\tok, _ := regexp.MatchString(\"^a+$\", \"aaa\")\n\treturn fmt.Sprint(re.FindStringSubmatch(\"ada@example.com\"), re.NumSubexp(), re.ReplaceAllString(\"x a@b.com y\", \"$2:$1\"), re.FindAllStringIndex(\"a@b.com c@d.com\", -1), re.ReplaceAllStringFunc(\"a@b.com\", strings.ToUpper), err != nil, ok, regexp.QuoteMeta(\"a.b\"), re.String(), re.MatchString(\"nope\"), re.Split(\"1 a@b.com 2\", -1))\n}",
			func() string {
				_, err := regexp.Compile("(")
				ok, _ := regexp.MatchString("^a+$", "aaa")
				return fmt.Sprint(re.FindStringSubmatch("ada@example.com"), re.NumSubexp(), re.ReplaceAllString("x a@b.com y", "$2:$1"), re.FindAllStringIndex("a@b.com c@d.com", -1), re.ReplaceAllStringFunc("a@b.com", strings.ToUpper), err != nil, ok, regexp.QuoteMeta("a.b"), re.String(), re.MatchString("nope"), re.Split("1 a@b.com 2", -1))
			}()},
		"url": {`import ("net/url"; "fmt")
func Run() string {
	u, err := url.Parse("https://ada:pw@example.com:8080/a%20b/c?x=1&y=2#frag")
	if err != nil { return err.Error() }
	q := u.Query()
	q.Add("z", "a b")
	pw, has := u.User.Password()
	ref, _ := u.Parse("../d?k=v")
	_, bad := url.Parse("http://[::1")
	v := url.Values{}
	v.Set("b", "2"); v.Set("a", "1")
	joined, _ := url.JoinPath("https://x.io/a", "b", "../c")
	return fmt.Sprint(u.Scheme, u.Hostname(), u.Port(), u.Path, u.EscapedPath(), q.Get("y"), q.Encode(), u.User.Username(), pw, has, ref.String(), bad != nil, v.Encode(), url.QueryEscape("a b&c"), url.PathEscape("a b/c"), joined, u.Redacted(), u.IsAbs(), q.Has("x"))
}`, func() string {
			pw, has := u.User.Password()
			ref, _ := u.Parse("../d?k=v")
			_, bad := url.Parse("http://[::1")
			v := url.Values{}
			v.Set("b", "2")
			v.Set("a", "1")
			joined, _ := url.JoinPath("https://x.io/a", "b", "../c")
			return fmt.Sprint(u.Scheme, u.Hostname(), u.Port(), u.Path, u.EscapedPath(), q.Get("y"), q.Encode(), u.User.Username(), pw, has, ref.String(), bad != nil, v.Encode(), url.QueryEscape("a b&c"), url.PathEscape("a b/c"), joined, u.Redacted(), u.IsAbs(), q.Has("x"))
		}()},
		"time": {`import ("time"; "fmt")
func Run() string {
	day := time.Date(2025, time.March, 1, 13, 4, 5, 0, time.UTC)
	later := day.Add(36*time.Hour + 30*time.Minute).AddDate(0, 1, 0)
	parsed, err := time.Parse(time.DateOnly, "2024-02-29")
	_, bad := time.Parse(time.DateOnly, "nope")
	d, _ := time.ParseDuration("1h15m30.5s")
	zone := time.FixedZone("EST", -5*3600)
	y, w := day.ISOWeek()
	return fmt.Sprint(day.Year(), day.Month(), day.Day(), day.Weekday(), day.YearDay(), day.Hour(), later.Format(time.RFC1123), later.Sub(day), later.After(day), parsed.Weekday(), err, bad != nil, d, d.Minutes(), d.Round(time.Minute), d.Truncate(time.Hour).String(), day.In(zone).Format(time.Kitchen), day.Truncate(24*time.Hour).Unix(), time.Duration(1500)*time.Millisecond, y, w, day.Location().String(), time.UTC.String(), time.Unix(86400, 0).UTC().Format(time.DateOnly), time.Now().After(day), time.Since(day) > time.Hour, day.Compare(later), time.March.String(), time.Saturday)
}`, func() string {
			_, bad := time.Parse(time.DateOnly, "nope")
			d, _ := time.ParseDuration("1h15m30.5s")
			zone := time.FixedZone("EST", -5*3600)
			y, w := day.ISOWeek()
			var err error
			return fmt.Sprint(day.Year(), day.Month(), day.Day(), day.Weekday(), day.YearDay(), day.Hour(), later.Format(time.RFC1123), later.Sub(day), later.After(day), parsed.Weekday(), err, bad != nil, d, d.Minutes(), d.Round(time.Minute), d.Truncate(time.Hour).String(), day.In(zone).Format(time.Kitchen), day.Truncate(24*time.Hour).Unix(), time.Duration(1500)*time.Millisecond, y, w, day.Location().String(), time.UTC.String(), time.Unix(86400, 0).UTC().Format(time.DateOnly), time.Now().After(day), time.Since(day) > time.Hour, day.Compare(later), time.March.String(), time.Saturday)
		}()},
		"unicode": {`import ("unicode"; "unicode/utf8"; "unicode/utf16"; "fmt")
func Run() string {
	b := make([]byte, 4)
	n := utf8.EncodeRune(b, 'é')
	r, size := utf8.DecodeRuneInString("éa")
	return fmt.Sprint(unicode.IsUpper('A'), unicode.IsLetter('1'), unicode.ToUpper('ß'), unicode.Is(unicode.Cyrillic, 'ж'), unicode.In('x', unicode.Latin, unicode.Greek), string(unicode.ToTitle('a')), unicode.IsSpace('\t'), unicode.MaxRune, utf8.RuneCountInString("héllo"), utf8.ValidString("a\xffb"), n, b[:n], r, size, utf8.RuneLen('€'), utf8.RuneError, utf16.Encode([]rune("a😀")), string(utf16.Decode([]uint16{0x61, 0xd83d, 0xde00})), utf8.UTFMax, unicode.IsPunct('!'), unicode.SimpleFold('k'))
}`, func() string {
			b := make([]byte, 4)
			n := utf8.EncodeRune(b, 'é')
			r, size := utf8.DecodeRuneInString("éa")
			return fmt.Sprint(unicode.IsUpper('A'), unicode.IsLetter('1'), unicode.ToUpper('ß'), unicode.Is(unicode.Cyrillic, 'ж'), unicode.In('x', unicode.Latin, unicode.Greek), string(unicode.ToTitle('a')), unicode.IsSpace('\t'), unicode.MaxRune, utf8.RuneCountInString("héllo"), utf8.ValidString("a\xffb"), n, b[:n], r, size, utf8.RuneLen('€'), utf8.RuneError, utf16.Encode([]rune("a😀")), string(utf16.Decode([]uint16{0x61, 0xd83d, 0xde00})), utf8.UTFMax, unicode.IsPunct('!'), unicode.SimpleFold('k'))
		}()},
		"encoding": {`import ("encoding/base64"; "encoding/hex"; "fmt")
func Run() string {
	data := []byte("hi there?>")
	enc := base64.StdEncoding.EncodeToString(data)
	dec, err := base64.URLEncoding.DecodeString(base64.URLEncoding.EncodeToString(data))
	_, bad := base64.StdEncoding.DecodeString("!!")
	raw := base64.RawURLEncoding.EncodeToString(data)
	h := hex.EncodeToString(data)
	back, herr := hex.DecodeString(h)
	_, hbad := hex.DecodeString("zz")
	custom := base64.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_").WithPadding(base64.NoPadding)
	return fmt.Sprint(enc, string(dec), err, bad != nil, raw, base64.StdEncoding.EncodedLen(10), h, string(back), herr, hbad != nil, custom.EncodeToString(data), hex.EncodedLen(3))
}`, func() string {
			data := []byte("hi there?>")
			enc := base64.StdEncoding.EncodeToString(data)
			dec, err := base64.URLEncoding.DecodeString(base64.URLEncoding.EncodeToString(data))
			_, bad := base64.StdEncoding.DecodeString("!!")
			raw := base64.RawURLEncoding.EncodeToString(data)
			h := hex.EncodeToString(data)
			back, herr := hex.DecodeString(h)
			_, hbad := hex.DecodeString("zz")
			custom := base64.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_").WithPadding(base64.NoPadding)
			return fmt.Sprint(enc, string(dec), err, bad != nil, raw, base64.StdEncoding.EncodedLen(10), h, string(back), herr, hbad != nil, custom.EncodeToString(data), hex.EncodedLen(3))
		}()},
		"bits": {`import ("math/bits"; "fmt")
func Run() string {
	hi, lo := bits.Mul64(1<<63, 4)
	return fmt.Sprint(bits.OnesCount(255), bits.LeadingZeros32(1), bits.TrailingZeros(8), bits.Reverse8(1), bits.Len(1023), bits.RotateLeft8(1, 3), hi, lo, bits.UintSize)
}`, func() string {
			hi, lo := bits.Mul64(1<<63, 4)
			return fmt.Sprint(bits.OnesCount(255), bits.LeadingZeros32(1), bits.TrailingZeros(8), bits.Reverse8(1), bits.Len(1023), bits.RotateLeft8(1, 3), hi, lo, bits.UintSize)
		}()},
		"bytes": {`import ("bytes"; "fmt")
func Run() string {
	var buf bytes.Buffer
	buf.WriteString("héllo ")
	buf.WriteByte('w')
	buf.Write([]byte("orld"))
	b := []byte("a,b,,c")
	parts := bytes.Split(b, []byte(","))
	upper := bytes.Map(func(r rune) rune { if r == 'a' { return 'A' }; return r }, b)
	return fmt.Sprint(buf.String(), buf.Len(), bytes.Contains(b, []byte(",,")), len(parts), string(bytes.ToUpper(b)), bytes.Index(b, []byte("c")), string(bytes.TrimSpace([]byte("  x "))), bytes.Equal(b, b), string(bytes.Join(parts, []byte("-"))), string(upper), string(bytes.Repeat([]byte("ab"), 3)), bytes.Count(b, []byte(",")), string(bytes.Fields([]byte(" a  b "))[1]), bytes.NewBufferString("xyz").String())
}`, func() string {
			b := []byte("a,b,,c")
			parts := bytes.Split(b, []byte(","))
			upper := bytes.Map(func(r rune) rune {
				if r == 'a' {
					return 'A'
				}
				return r
			}, b)
			return fmt.Sprint(buf.String(), buf.Len(), bytes.Contains(b, []byte(",,")), len(parts), string(bytes.ToUpper(b)), bytes.Index(b, []byte("c")), string(bytes.TrimSpace([]byte("  x "))), bytes.Equal(b, b), string(bytes.Join(parts, []byte("-"))), string(upper), string(bytes.Repeat([]byte("ab"), 3)), bytes.Count(b, []byte(",")), string(bytes.Fields([]byte(" a  b "))[1]), bytes.NewBufferString("xyz").String())
		}()},
	} {
		t.Run(name, func(t *testing.T) {
			if got := run(t, tc.script); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
	_ = later
	_ = parsed
}

func TestSourcePackages(t *testing.T) {
	got := run(t, `import ("cmp"; "maps"; "slices"; "sort"; "fmt"; "strings")

type person struct { name string; age int }
type byAge []person
func (p byAge) Len() int { return len(p) }
func (p byAge) Less(i, j int) bool { return p[i].age < p[j].age }
func (p byAge) Swap(i, j int) { p[i], p[j] = p[j], p[i] }

func Run() string {
	var out []string
	add := func(v ...any) { out = append(out, fmt.Sprint(v...)) }

	s := []int{5, 2, 8, 2, 9, 1}
	slices.Sort(s)
	add(s, slices.Contains(s, 8), slices.Index(s, 9), slices.Max(s), slices.Min(s), slices.IsSorted(s))
	i, found := slices.BinarySearch(s, 8)
	add(i, found, slices.Compact(slices.Clone(s)), len(slices.Repeat([]int{1}, 3)))
	words := []string{"pear", "Apple", "fig", "apple"}
	slices.SortFunc(words, func(a, b string) int { return cmp.Compare(strings.ToLower(a), strings.ToLower(b)) })
	add(words, slices.IndexFunc(words, func(w string) bool { return len(w) == 3 }))
	add(slices.Insert([]int{1, 4}, 1, 2, 3), slices.Delete([]int{1, 2, 3, 4}, 1, 3), slices.Concat([]int{1}, []int{2, 3}), slices.Equal(s, s))
	add(slices.DeleteFunc([]int{1, 2, 3, 4}, func(n int) bool { return n%2 == 0 }), slices.Replace([]int{1, 2, 3}, 0, 2, 9), slices.Compare([]int{1, 2}, []int{1, 3}))
	var chunks []string
	for c := range slices.Chunk([]int{1, 2, 3, 4, 5}, 2) { chunks = append(chunks, fmt.Sprint(c)) }
	add(chunks, slices.Collect(slices.Values([]string{"a", "b"})))
	for i, v := range slices.Backward([]string{"x", "y"}) { add(i, v) }

	m := map[string]int{"b": 2, "a": 1, "c": 3}
	keys := slices.Sorted(maps.Keys(m))
	add(keys, slices.Sorted(maps.Values(m)), maps.Equal(m, maps.Clone(m)), len(maps.Collect(maps.All(m))))
	maps.DeleteFunc(m, func(k string, v int) bool { return v > 1 })
	add(m, cmp.Compare(2, 1), cmp.Compare("a", "b"), cmp.Less(1.5, 2.5), cmp.Or(0, 0, 7), cmp.Or("", "x"))

	ints := []int{3, 1, 2}
	sort.Ints(ints)
	strs := []string{"b", "c", "a"}
	sort.Strings(strs)
	people := byAge{{"Ann", 30}, {"Bob", 25}, {"Cid", 30}, {"Dee", 20}, {"Eve", 25}}
	sort.Stable(people)
	add(ints, strs, sort.SearchInts(ints, 2), sort.IntsAreSorted(ints), people)
	sort.Sort(sort.Reverse(sort.IntSlice(ints)))
	big := make([]int, 200)
	for i := range big { big[i] = (i * 7919) % 211 }
	sort.Sort(sort.IntSlice(big))
	sorted := sort.IsSorted(sort.IntSlice(big))
	pairs := make(byAge, 150)
	for i := range pairs { pairs[i] = person{fmt.Sprint(i), (i * 31) % 7} }
	sort.Stable(pairs)
	stable := sort.IsSorted(pairs)
	for i := 1; i < len(pairs); i++ {
		if pairs[i].age == pairs[i-1].age && len(pairs[i].name) == len(pairs[i-1].name) && pairs[i].name < pairs[i-1].name { stable = false }
	}
	add(ints, sorted, stable, sort.Search(100, func(i int) bool { return i*i >= 50 }))
	return strings.Join(out, "|")
}`)
	want := "[1 2 2 5 8 9] true 5 9 1 true|4 true [1 2 5 8 9] 3|[Apple apple fig pear] 2|[1 2 3 4] [1 4] [1 2 3] true|[1 3] [9 3] -1|[[1 2] [3 4] [5]] [a b]|1y|0x|[a b c] [1 2 3] true 3|map[a:1] 1 -1 true 7x|[1 2 3] [a b c] 1 true [{Dee 20} {Bob 25} {Eve 25} {Ann 30} {Cid 30}]|[3 2 1] true true 8"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestByteResultBudget(t *testing.T) {
	const src = `import "bytes"
	func ReplaceAll(b []byte) int {return len(bytes.ReplaceAll(b, []byte("a"), b))}
	func ReplaceMany(b []byte) int {return len(bytes.Replace(b, []byte("a"), b, -1))}
	func ReplaceOnce(b []byte) int {return len(bytes.Replace(b, []byte("a"), b, 1))}
	func ReplaceNone(b []byte) int {return len(bytes.Replace(b, nil, b, 0))}
	func EmptyOld() int {return len(bytes.ReplaceAll([]byte("é"), nil, []byte("xy")))}
	func Repeat(b []byte) int {return len(bytes.Repeat(b, 64))}
	func Join(b []byte) int {return len(bytes.Join([][]byte{b,b,b,b,b,b,b,b}, b))}
	`
	pkgs := evmstd.Packages()
	mod, err := evmcompiler.Compile("bytes.go", src, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(mod, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	b := bytes.Repeat([]byte("a"), 256)
	args := []values.Value{values.SliceValue(unsafe.Pointer(unsafe.SliceData(b)), uint32(len(b)), uint32(len(b)))}
	for name, want := range map[string]int{"ReplaceAll": 65536, "ReplaceMany": 65536, "ReplaceOnce": 511, "ReplaceNone": 256, "EmptyOld": 6, "Repeat": 16384, "Join": 3840} {
		t.Run(name, func(t *testing.T) {
			in := args
			if name == "EmptyOld" {
				in = nil
			}
			for _, budget := range []uint64{2048, 1 << 20} {
				vm := evmtest.New(p)
				var out [1]values.Value
				err := vm.RunWithBudget(context.Background(), name, in, out[:], evm.Limits{AllocationBytes: budget})
				vm.Release()
				if budget < uint64(want) {
					if !errors.Is(err, evm.ErrAllocation) {
						t.Fatalf("small budget: %v", err)
					}
				} else if err != nil || int(out[0].AsInt()) != want {
					t.Fatalf("got %v, %v; want %d", out, err, want)
				}
			}
		})
	}
}
