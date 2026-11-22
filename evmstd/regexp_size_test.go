package evmstd

import (
	"regexp"
	"testing"
)

func TestRegexpExpansionSize(t *testing.T) {
	re := regexp.MustCompile(`(?P<n>a+)|(?P<n>b)|(?P<01>é)|(?P<1000000000>c)`)
	src := "aa b é c!"
	matches := re.FindAllStringSubmatchIndex(src, -1)
	unmatched := len(src)
	for _, m := range matches {
		unmatched -= m[1] - m[0]
	}
	for _, template := range []string{"", "$", "$$", "$0", "$n", "$01", "$1000000000", "$0000", "${missing}", "$é", "$${n}", "${n", "$1x-$2.$1!", "${01}$n$$💧$0"} {
		want := len(re.ReplaceAllString(src, template))
		if got := regexpExpansionSize(unmatched, template, matches, re.SubexpNames()); got != want {
			t.Errorf("template %q: size %d, want %d", template, got, want)
		}
	}
}
