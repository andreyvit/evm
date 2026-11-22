package evmstd_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmstd"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
)

func TestRegexpReplacementBudgets(t *testing.T) {
	const src = `import "regexp"
	func Literal(s,r string) int {return len(regexp.MustCompile("x").ReplaceAllLiteral([]byte(s), []byte(r)))}
	func LiteralString(s,r string) int {return len(regexp.MustCompile("x").ReplaceAllLiteralString(s,r))}
	func Expand(s,r string) int {return len(regexp.MustCompile("(x)").ReplaceAll([]byte(s),[]byte(r)))}
	func ExpandString(s,r string) int {return len(regexp.MustCompile("(x)").ReplaceAllString(s,r))}
	func Callback(s,r string) int {b:=[]byte(r);return len(regexp.MustCompile("x").ReplaceAllFunc([]byte(s),func(m []byte) []byte{return b}))}
	func StringCallback(s,r string) int {return len(regexp.MustCompile("x").ReplaceAllStringFunc(s,func(m string) string{return r}))}`
	pkgs := evmstd.Packages()
	mod, err := evmcompiler.Compile("replace.go", src, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(mod, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Literal", "LiteralString", "Expand", "ExpandString", "Callback", "StringCallback"} {
		t.Run(name, func(t *testing.T) {
			repl := strings.Repeat("z", 256)
			if strings.HasPrefix(name, "Expand") {
				repl = strings.Repeat("$1", 256)
			}
			args := []values.Value{values.String(strings.Repeat("x", 256)), values.String(repl)}
			for _, budget := range []uint64{32 << 10, 1 << 20} {
				vm := evmtest.New(p)
				var out [1]values.Value
				err := vm.RunWithBudget(context.Background(), name, args, out[:], evm.Limits{AllocationBytes: budget})
				vm.Release()
				if budget < 65536 {
					if !errors.Is(err, evm.ErrAllocation) {
						t.Fatalf("small budget: %v", err)
					}
				} else if err != nil || out[0].AsInt() != 65536 {
					t.Fatalf("got %d, %v", out[0].AsInt(), err)
				}
			}
		})
	}
}

func TestRegexpReplacementExpansion(t *testing.T) {
	for _, tc := range []struct{ pattern, src, repl string }{
		{`(?P<n>a)|(?P<n>b)`, "a b", `$n-${n}-$0-$2-$1-$$-$00-$9-$unknown-$1x-$-${oops`},
		{`^|é|$`, "é β", `[$0]$`},
		{`\b(a*)\b`, "foo a!", `<$1>`},
		{`(?P<01>ab)`, "ab", `$01`},
		{`(a)`, "a", `$9999999999-$é-$000`},
		{"z", "abc", "$0"}, {"", "", "$$"}, {"(a)", "a", "$2"},
	} {
		re := regexp.MustCompile(tc.pattern)
		want := re.ReplaceAllString(tc.src, tc.repl)
		script := fmt.Sprintf(`import "regexp";func Run()string{r:=regexp.MustCompile(%q);return r.ReplaceAllString(%q,%q)+"|"+string(r.ReplaceAll([]byte(%q),[]byte(%q)))}`, tc.pattern, tc.src, tc.repl, tc.src, tc.repl)
		if got := run(t, script); got != want+"|"+want {
			t.Errorf("%q on %q: %q, want %q", tc.pattern, tc.src, got, want)
		}
	}
}

func TestRegexpReplacementCallbackOrder(t *testing.T) {
	// The first callback changes what the regexp can match next.
	r := regexp.MustCompile("a")
	src := []byte("abaa")
	n := 0
	want := r.ReplaceAllFunc(src, func(m []byte) []byte {
		n++
		if n == 1 {
			src[2] = 'x'
		}
		return bytes.Repeat(m, n)
	})
	got := run(t, `import("regexp";"bytes");func Run()string{
		src:=[]byte("abaa");n:=0;r:=regexp.MustCompile("a");replace:=r.ReplaceAllFunc
		return string(replace(src,func(m []byte)[]byte{n++;if n==1{src[2]='x'};return bytes.Repeat(m,n)}))}`)
	if got != string(want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRegexpEmptyMatchesAndCallbackPanic(t *testing.T) {
	got := run(t, `import "regexp";func Run()string{
		r:=regexp.MustCompile("a*")
		return r.ReplaceAllStringFunc("aé!",func(s string)string{return "["+s+"]"})
	}`)
	want := regexp.MustCompile("a*").ReplaceAllStringFunc("aé!", func(s string) string { return "[" + s + "]" })
	if got != want {
		t.Fatalf("empty matches: got %q, want %q", got, want)
	}
	got = run(t, `import "regexp";func Run()(result string){
		defer func(){result=recover().(string)}()
		return regexp.MustCompile("x").ReplaceAllStringFunc("x",func(string)string{panic("stop")})
	}`)
	if got != "stop" {
		t.Fatalf("recovered %q", got)
	}
}
