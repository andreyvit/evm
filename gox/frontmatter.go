package gox

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/andreyvit/evm"
)

// SplitFrontmatter extracts an optional leading --- block without interpreting
// its contents. A nil result means absent; an empty non-nil result means present.
// Body excludes the delimiters. Delimiters must occupy whole lines.
func SplitFrontmatter(filename, source string) (data []byte, body string, err error) {
	start := 0
	if strings.HasPrefix(source, "\ufeff") {
		start = len("\ufeff")
	}
	end := strings.IndexByte(source[start:], '\n')
	if end < 0 || strings.TrimSuffix(source[start:start+end], "\r") != "---" {
		if source[start:] == "---" || source[start:] == "---\r" {
			return nil, "", Diagnostic{evm.SourcePosition{File: filename, Line: 1, Column: 1}, "unterminated frontmatter"}
		}
		return nil, source, nil
	}
	content := start + end + 1
	for at := content; at <= len(source); {
		next := strings.IndexByte(source[at:], '\n')
		lineEnd := len(source)
		if next >= 0 {
			lineEnd = at + next
		}
		if strings.TrimSuffix(source[at:lineEnd], "\r") == "---" {
			data = make([]byte, at-content)
			copy(data, source[content:at])
			bodyStart := lineEnd
			if next >= 0 {
				bodyStart++
			}
			return data, source[bodyStart:], nil
		}
		if next < 0 {
			break
		}
		at = lineEnd + 1
	}
	return nil, "", Diagnostic{evm.SourcePosition{File: filename, Line: 1, Column: 1}, "unterminated frontmatter"}
}

func blankFrontmatter(source, body string) string {
	prefix := []byte(source[:len(source)-len(body)])
	for i, c := range prefix {
		if c != '\n' && c != '\r' {
			prefix[i] = ' '
		}
	}
	return string(prefix) + body
}

func (o Options) parseFrontmatter(filename string, data []byte) (any, error) {
	if data == nil {
		return nil, nil
	}
	if o.ParseFrontmatter == nil {
		return nil, Diagnostic{evm.SourcePosition{File: filename, Line: 1, Column: 1}, "frontmatter requires a host parser"}
	}
	value, err := o.ParseFrontmatter(bytes.Clone(data))
	if err != nil {
		return nil, Diagnostic{evm.SourcePosition{File: filename, Line: 2, Column: 1}, fmt.Sprintf("invalid frontmatter: %v", err)}
	}
	return value, nil
}

// Frontmatter returns the host-parsed metadata of the file declaring name.
// Presence is independent of the parser's value, which may itself be nil.
// Treat returned metadata as immutable.
func (t *Template) Frontmatter(name string) (any, bool) {
	file, ok := t.FunctionFile(name)
	return file.Frontmatter, ok && file.HasFrontmatter
}

// Frontmatter returns the declaring file's metadata for a named function.
func (p *Program) Frontmatter(name string) (any, bool) {
	return p.template.Frontmatter(name)
}
