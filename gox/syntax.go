package gox

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"path"
	"strconv"
	"strings"

	"github.com/andreyvit/evm/evmcompiler"
)

type sourceSyntax struct {
	source, generated string
	files             *token.FileSet
	file              *ast.File
	marks             []markup
	mapping           []int
	prefix            int
}

// CheckSyntax checks Go and markup syntax without resolving names or types.
// Compilation still checks call signatures, rendering contexts and EVM support.
func CheckSyntax(filename, source string) error {
	return Options{}.CheckSyntax(filename, source)
}

// CheckSyntax validates host frontmatter and Go/markup syntax without type checking.
func (o Options) CheckSyntax(filename, source string) (err error) {
	defer evmcompiler.CatchInternalError(&err)
	if path.Ext(filename) == ".gox" {
		raw, _, err := SplitFrontmatter(filename, source)
		if err != nil {
			return err
		}
		if _, err := o.parseFrontmatter(filename, raw); err != nil {
			return err
		}
	}
	_, err = parseSource(filename, source, o.AllowMissingPackage)
	return err
}

func parseSource(filename, source string, allowMissingPackage bool) (*sourceSyntax, error) {
	if len(source) > evmcompiler.MaxSourceSize {
		return nil, fmt.Errorf("%s: source exceeds %d bytes", filename, evmcompiler.MaxSourceSize)
	}
	s := &sourceSyntax{source: source, generated: source, files: token.NewFileSet()}
	var err error
	switch path.Ext(filename) {
	case ".gox":
		_, body, splitErr := SplitFrontmatter(filename, source)
		if splitErr != nil {
			return nil, splitErr
		}
		s.generated, s.marks, s.mapping, err = preprocess(filename, blankFrontmatter(source, body), 0)
		if err != nil {
			return nil, err
		}
	case ".go":
	default:
		return nil, fmt.Errorf("%s: source filename must end in .go or .gox", filename)
	}
	if allowMissingPackage && !hasPackageClause(filename, s.generated) {
		prefix := "package plugin\n"
		s.prefix = len(prefix)
		s.generated = prefix + s.generated
	}
	s.file, err = parser.ParseFile(s.files, filename, s.generated, parser.ParseComments)
	if err != nil {
		if list, ok := err.(scanner.ErrorList); ok {
			for _, e := range list {
				offset := e.Pos.Offset - s.prefix
				if s.mapping != nil && offset >= 0 && offset < len(s.mapping) {
					offset = s.mapping[offset]
				}
				offset = min(max(0, offset), len(source))
				prefix := source[:offset]
				e.Pos.Line = strings.Count(prefix, "\n") + 1
				e.Pos.Column = offset - strings.LastIndexByte(prefix, '\n')
				e.Pos.Offset = offset
				e.Pos.Filename = filename
			}
		}
		return nil, err
	}
	var stack []int
	for id, m := range s.marks {
		switch m.kind {
		case markupOpen:
			if !m.selfClosing {
				stack = append(stack, id)
			}
		case markupClose:
			// A lowercase callable can shadow a void HTML tag. An explicit closing
			// tag establishes a pair without needing the callable's type.
			for len(stack) > 0 && s.marks[stack[len(stack)-1]].name != m.name && s.marks[stack[len(stack)-1]].void {
				stack = stack[:len(stack)-1]
			}
			if len(stack) == 0 || s.marks[stack[len(stack)-1]].name != m.name {
				return nil, fmt.Errorf("%s:%d:%d: unmatched closing tag </%s>", filename, m.pos.line, m.pos.column, m.name)
			}
			s.marks[stack[len(stack)-1]].void = false
			stack = stack[:len(stack)-1]
		}
	}
	for _, id := range stack {
		m := s.marks[id]
		if !m.void {
			return nil, fmt.Errorf("%s:%d:%d: unclosed tag <%s>", filename, m.pos.line, m.pos.column, m.name)
		}
	}
	return s, nil
}

func hasPackageClause(filename, source string) bool {
	var scan scanner.Scanner
	scan.Init(token.NewFileSet().AddFile(filename, -1, len(source)), []byte(source), nil, 0)
	_, kind, _ := scan.Scan()
	return kind == token.PACKAGE
}

func (s *sourceSyntax) marker(n ast.Node) (int, bool) {
	call, ok := statementCall(n)
	if !ok || !isIdent(call.Fun, "_goxMark") || len(call.Args) == 0 {
		return 0, false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok {
		return 0, false
	}
	id, err := strconv.Atoi(lit.Value)
	if err != nil || id < 0 || id >= len(s.marks) {
		return 0, false
	}
	offset := s.files.Position(call.Pos()).Offset - s.prefix
	if offset < 0 || offset >= len(s.mapping) || s.mapping[offset] != s.markOffset(id) {
		return 0, false
	}
	return id, true
}

func (s *sourceSyntax) markOffset(id int) int {
	m := s.marks[id]
	offset := 0
	for range m.pos.line - 1 {
		offset += strings.IndexByte(s.source[offset:], '\n') + 1
	}
	return offset + m.pos.column - 1
}

// Keep byte offsets while disabling directives during diagnostic-only analysis.
func withoutLineDirectives(source string) string {
	data := []byte(source)
	file := token.NewFileSet().AddFile("", -1, len(data))
	var scan scanner.Scanner
	scan.Init(file, data, nil, scanner.ScanComments)
	for {
		pos, tok, lit := scan.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.COMMENT && (strings.HasPrefix(lit, "//line ") || strings.HasPrefix(lit, "/*line ")) {
			copy(data[file.Offset(pos)+2:], "xxxx")
		}
	}
	return string(data)
}
