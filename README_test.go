package evm_test

import (
	"fmt"
	"os"
	"path"
	"strings"
	"testing"
)

// TestREADMEExcerpts keeps README code in step with the examples. A fenced
// block that follows a line of the form
//
//	<!-- excerpt: examples/evm-basic/main.go -->
//
// must appear in that file. Indentation is ignored, so an excerpt can show a
// function body without its leading tabs. A line holding only "// ..." marks
// omitted lines: the parts around it must appear in the file in that order.
//
// The helpers must and ensure are defined in the example sources only; no
// fenced block in the README may define them.
func TestREADMEExcerpts(t *testing.T) {
	const readme = "README.md"
	data, err := os.ReadFile(readme)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	excerpts := 0
	for i := 0; i < len(lines); i++ {
		source, marked := excerptSource(lines[i])
		if !marked && !strings.HasPrefix(lines[i], "```") {
			continue
		}
		open := i
		if marked {
			open = i + 1
			if open >= len(lines) || !strings.HasPrefix(lines[open], "```") {
				t.Errorf("%s:%d: a fenced block must follow the excerpt marker", readme, i+1)
				continue
			}
		}
		end := open + 1
		for end < len(lines) && !strings.HasPrefix(lines[end], "```") {
			end++
		}
		if end == len(lines) {
			t.Fatalf("%s:%d: unclosed fenced block", readme, open+1)
		}
		block := lines[open+1 : end]
		for j, line := range block {
			text := strings.TrimSpace(line)
			if strings.HasPrefix(text, "func must[") || strings.HasPrefix(text, "func must(") || strings.HasPrefix(text, "func ensure(") {
				t.Errorf("%s:%d: the README must not show the definition of a must/ensure helper", readme, open+2+j)
			}
		}
		if marked {
			excerpts++
			if err := checkExcerpt(source, block, open+2); err != nil {
				t.Errorf("%s:%v", readme, err)
			}
		}
		i = end
	}
	if excerpts == 0 {
		t.Errorf("%s has no excerpt markers; the headline examples must come from files under examples/", readme)
	}
}

// excerptSource returns the file named by an excerpt marker line.
func excerptSource(line string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "<!-- excerpt:")
	if !ok {
		return "", false
	}
	rest, ok = strings.CutSuffix(rest, "-->")
	return strings.TrimSpace(rest), ok
}

// checkExcerpt reports where block, which starts at line firstLine of the
// README, stops matching the source file.
func checkExcerpt(source string, block []string, firstLine int) error {
	if clean := path.Clean(source); clean != source || !strings.HasPrefix(source, "examples/") {
		return fmt.Errorf("%d: excerpt source %q must be a clean path under examples/", firstLine-1, source)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("%d: %v", firstLine-1, err)
	}
	file := strings.Split(string(data), "\n")
	for i := range file {
		file[i] = strings.TrimSpace(file[i])
	}
	from := 0 // each part must start at or after the end of the previous one
	start := 0
	for i := 0; i <= len(block); i++ {
		if i < len(block) && strings.TrimSpace(block[i]) != "// ..." {
			continue
		}
		part, partLine := block[start:i], firstLine+start
		start = i + 1
		for len(part) > 0 && strings.TrimSpace(part[0]) == "" {
			part, partLine = part[1:], partLine+1
		}
		for len(part) > 0 && strings.TrimSpace(part[len(part)-1]) == "" {
			part = part[:len(part)-1]
		}
		if len(part) == 0 {
			return fmt.Errorf("%d: empty part in the excerpt of %s", partLine, source)
		}
		at, matched := findLines(file[from:], part)
		if matched < len(part) {
			if matched == 0 {
				return fmt.Errorf("%d: %s has no line %q after line %d", partLine, source, strings.TrimSpace(part[0]), from)
			}
			return fmt.Errorf("%d: excerpt differs from %s:%d\n\tREADME: %s\n\tsource: %s", partLine+matched, source,
				from+at+matched+1, strings.TrimSpace(part[matched]), lineAt(file, from+at+matched))
		}
		from += at + len(part)
	}
	return nil
}

// findLines returns the position in file where the longest run of leading
// lines of part matches, and the length of that run.
func findLines(file, part []string) (at, matched int) {
	for i := range file {
		n := 0
		for n < len(part) && i+n < len(file) && file[i+n] == strings.TrimSpace(part[n]) {
			n++
		}
		if n > matched {
			at, matched = i, n
		}
		if matched == len(part) {
			break
		}
	}
	return at, matched
}

func lineAt(file []string, i int) string {
	if i >= len(file) {
		return "(end of file)"
	}
	return file[i]
}
