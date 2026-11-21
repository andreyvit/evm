package gox

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/scanner"
	"go/token"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmstd"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/imports"
)

const formatLineWidth = 80

type sourceEdit struct {
	start, end int
	text       string
}

func editSource(source string, edits []sourceEdit) string {
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

// FormatSource formats GoX and organizes imports, adding the missing ones.
// Packages supply virtual import paths; no host implementations or type
// checking are needed. Unknown packages remain unresolved. Layout is fixed,
// with an 80-column merge/split threshold.
func FormatSource(filename, source string, packages ...evm.Package) (string, error) {
	return Options{}.FormatSource(filename, source, packages...)
}

// FormatSource formats source, accepting a missing package clause only in legacy mode.
// Frontmatter stays opaque even if Options supplies a parser.
func (o Options) FormatSource(filename, source string, packages ...evm.Package) (_ string, err error) {
	defer evmcompiler.CatchInternalError(&err)
	var header string
	if path.Ext(filename) == ".gox" {
		_, body, err := SplitFrontmatter(filename, source)
		if err != nil {
			return "", err
		}
		header = source[:len(source)-len(body)]
	}
	s, err := parseSource(filename, source, o.AllowMissingPackage)
	if err != nil {
		return "", err
	}
	if path.Ext(filename) == ".go" {
		out, err := imports.Process(filename, []byte(source), nil)
		return string(out), err
	}
	prefix := "_goxFormat"
	for strings.Contains(source, prefix) {
		prefix += "X"
	}
	var edits []sourceEdit
	offset := func(pos token.Pos) int { return s.files.Position(pos).Offset }
	// Put marker statements and output expressions on distinct source lines so
	// go/printer does not retain their original semicolon-separated layout.
	ast.Inspect(s.file, func(n ast.Node) bool {
		var list []ast.Stmt
		switch n := n.(type) {
		case *ast.BlockStmt:
			list = n.List
		case *ast.CaseClause:
			list = n.Body
		case *ast.CommClause:
			list = n.Body
		}
		containsMarkup := false
		for _, stmt := range list {
			if _, ok := s.marker(stmt); ok {
				containsMarkup = true
			}
		}
		if containsMarkup {
			for _, stmt := range list {
				at := offset(stmt.Pos())
				if !strings.HasSuffix(strings.TrimRight(s.generated[:at], " \t"), "\n") {
					edits = append(edits, sourceEdit{at, at, "\n"})
				}
			}
			if len(list) > 0 {
				at := offset(list[len(list)-1].End())
				if !strings.HasPrefix(strings.TrimLeft(s.generated[at:], " ;\t"), "\n") {
					edits = append(edits, sourceEdit{at, at, "\n"})
				}
			}
		}
		if id, ok := s.marker(n); ok {
			call, _ := statementCall(n)
			edits = append(edits, sourceEdit{offset(call.Fun.Pos()), offset(call.Fun.End()), prefix + strconv.Itoa(id)})
			m := s.marks[id]
			if m.kind == markupOpen && strings.Contains(m.name, ".") {
				at := offset(call.Rparen)
				edits = append(edits, sourceEdit{at, at, ", " + m.name})
			}
		}
		return true
	})
	surrogate := editSource(s.generated, edits)
	files := token.NewFileSet()
	file, err := parser.ParseFile(files, filename, surrogate, parser.ParseComments)
	if err != nil {
		return "", err
	}
	if o.Environment != nil {
		if len(packages) != 0 {
			return "", fmt.Errorf("Options.Environment supplies the packages; pass none")
		}
		packages = o.Environment.packages
	}
	addFormattingImports(files, file, packages)
	var buf bytes.Buffer
	if err := format.Node(&buf, files, file); err != nil {
		return "", err
	}
	out, err := imports.Process(filename, buf.Bytes(), nil)
	if err != nil {
		return "", err
	}
	surrogate = string(out)
	if s.prefix != 0 {
		pf := token.NewFileSet()
		parsed, err := parser.ParseFile(pf, filename, surrogate, parser.PackageClauseOnly)
		if err != nil {
			return "", err
		}
		start, end := pf.Position(parsed.Package).Offset, pf.Position(parsed.Name.End()).Offset
		for end < len(surrogate) && surrogate[end] == '\n' {
			end++
		}
		surrogate = surrogate[:start] + surrogate[end:]
	}
	// Parse with a temporary package clause again to recover exact marker spans.
	parsePrefix := ""
	if s.prefix != 0 {
		parsePrefix = "package plugin\n\n"
	}
	files = token.NewFileSet()
	file, err = parser.ParseFile(files, filename, parsePrefix+surrogate, parser.ParseComments)
	if err != nil {
		return "", err
	}
	// gofmt removes condition parentheses. GoX needs them to distinguish < from
	// markup, so restore only comparisons not protected by () or [].
	surrogate, err = protectComparisons(filename, surrogate, parsePrefix, files, file)
	if err != nil {
		return "", err
	}
	files = token.NewFileSet()
	file, err = parser.ParseFile(files, filename, parsePrefix+surrogate, parser.ParseComments)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSuffix(surrogate, "\n"), "\n")
	rows := make([]formatRow, len(lines))
	for i, line := range lines {
		rows[i] = formatRow{text: line, marker: -1}
	}
	lineOffset := strings.Count(parsePrefix, "\n")
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			return true
		}
		start := files.Position(n.Pos()).Line - 1 - lineOffset
		end := files.Position(n.End()).Line - 1 - lineOffset
		switch n := n.(type) {
		case *ast.BasicLit:
			if n.Kind == token.STRING {
				for i := start + 1; i <= end && i < len(rows); i++ {
					if i >= 0 {
						rows[i].literal = true
					}
				}
			}
		case *ast.Comment:
			if start >= 0 && start < len(rows) {
				rows[start].value = false
			}
			for i := start + 1; i <= end && i < len(rows); i++ {
				if i >= 0 {
					rows[i].literal = true
				}
			}
		case *ast.ExprStmt, *ast.AssignStmt:
			if start < 0 || start >= len(rows) {
				break
			}
			_, expressionStatement := n.(*ast.ExprStmt)
			rows[start].value = expressionStatement && start == end
			call, ok := statementCall(n)
			if !ok {
				break
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok || !strings.HasPrefix(ident.Name, prefix) {
				break
			}
			id, err := strconv.Atoi(strings.TrimPrefix(ident.Name, prefix))
			if err != nil || id < 0 || id >= len(s.marks) {
				break
			}
			rows[start].marker = id
			if _, ok := n.(*ast.AssignStmt); ok {
				column := files.Position(call.Pos()).Column - 1
				rows[start].prefix = strings.TrimLeft(rows[start].text[:column], "\t ")
			}
			rows[start].last = end
			rows[start].suffix = rows[end].text[files.Position(n.End()).Column-1:]
		}
		return true
	})
	// Comments may be visited before their statement, which marks the row as a value.
	for _, group := range file.Comments {
		for _, comment := range group.List {
			line := files.Position(comment.Pos()).Line - 1 - lineOffset
			if line >= 0 && line < len(rows) {
				rows[line].value = false
			}
		}
	}
	f := sourceFormatter{syntax: s, rows: rows, pairs: map[int]int{}}
	var stack []int
	for i := 0; i < len(rows); i++ {
		r := rows[i]
		if r.marker < 0 {
			continue
		}
		m := s.marks[r.marker]
		if m.kind == markupOpen && !m.void {
			stack = append(stack, i)
		}
		if m.kind == markupClose {
			if len(stack) == 0 {
				return "", fmt.Errorf("unmatched closing tag")
			}
			f.pairs[stack[len(stack)-1]] = i
			stack = stack[:len(stack)-1]
		}
		i = r.last
	}
	formatted, err := f.render(0, len(rows), 0)
	if err != nil {
		return "", err
	}
	var result strings.Builder
	for _, row := range formatted {
		result.WriteString(row.text)
		result.WriteByte('\n')
	}
	output := result.String()
	if header != "" {
		output = header + "\n" + strings.TrimLeft(output, "\r\n")
	}
	if _, err := parseSource(filename, output, o.AllowMissingPackage); err != nil {
		return "", fmt.Errorf("formatted source: %w", err)
	}
	return output, nil
}

func addFormattingImports(files *token.FileSet, file *ast.File, packages []evm.Package) {
	names := map[string]string{"gox": "github.com/andreyvit/evm/gox"}
	for _, name := range evm.StandardLibraryPaths() {
		names[path.Base(name)] = name
	}
	for _, name := range evmstd.Paths() {
		// Two standard packages can share a last element, such as path and
		// filepath's parent; an ambiguous name is left for the author.
		if old, ok := names[path.Base(name)]; ok && old != name {
			names[path.Base(name)] = ""
		} else {
			names[path.Base(name)] = name
		}
	}
	for _, pkg := range packages {
		name := path.Base(pkg.Path)
		if old, ok := names[name]; ok && old != pkg.Path {
			names[name] = ""
		} else if !ok {
			names[name] = pkg.Path
		}
	}
	for _, imp := range file.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		name := path.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		delete(names, name)
	}
	used := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Obj == nil {
				used[id.Name] = true
			}
		}
		return true
	})
	var keys []string
	for name, p := range names {
		if p != "" && used[name] {
			keys = append(keys, name)
		}
	}
	slices.Sort(keys)
	for _, name := range keys {
		astutil.AddImport(files, file, names[name])
	}
}

func protectComparisons(filename, source, prefix string, files *token.FileSet, file *ast.File) (string, error) {
	protected := map[int]bool{}
	var scan scanner.Scanner
	tf := token.NewFileSet().AddFile(filename, -1, len(prefix+source))
	scan.Init(tf, []byte(prefix+source), nil, scanner.ScanComments)
	var stack []token.Token
	for {
		pos, tok, _ := scan.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.LSS {
			protected[tf.Offset(pos)] = len(stack) > 0 && stack[len(stack)-1] != token.LBRACE
		}
		switch tok {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			stack = append(stack, tok)
		case token.RPAREN, token.RBRACK, token.RBRACE:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	var edits []sourceEdit
	ast.Inspect(file, func(n ast.Node) bool {
		if b, ok := n.(*ast.BinaryExpr); ok && b.Op == token.LSS && !protected[files.Position(b.OpPos).Offset] {
			start, end := files.Position(b.Pos()).Offset-len(prefix), files.Position(b.End()).Offset-len(prefix)
			edits = append(edits, sourceEdit{start, start, "("}, sourceEdit{end, end, ")"})
		}
		return true
	})
	return editSource(source, edits), nil
}

type formatRow struct {
	text           string
	marker, last   int
	suffix         string
	prefix         string
	value, literal bool
}
type sourceFormatter struct {
	syntax *sourceSyntax
	rows   []formatRow
	pairs  map[int]int
}

func (f *sourceFormatter) render(start, end, depth int) ([]formatRow, error) {
	var out []formatRow
	for i := start; i < end; i++ {
		r := f.rows[i]
		if r.marker < 0 {
			if r.text != "" && !r.literal {
				r.text = strings.Repeat("\t", depth) + r.text
			}
			out = append(out, r)
			continue
		}
		m := f.syntax.marks[r.marker]
		indent := r.text[:len(r.text)-len(strings.TrimLeft(r.text, "\t "))] + strings.Repeat("\t", depth)
		if m.kind == markupOpen {
			opening, err := f.tag(r.marker, indent, r.prefix)
			if err != nil {
				return nil, err
			}
			if close, ok := f.pairs[i]; ok {
				closing := f.rows[close]
				closeMark := f.syntax.marks[closing.marker]
				if m.name == "script" || m.name == "style" {
					p := preprocessor{source: f.syntax.source, data: []byte(f.syntax.source)}
					_, _, bodyStart, err := p.tag(f.syntax.markOffset(r.marker))
					if err != nil {
						return nil, err
					}
					_, _, bodyEnd, err := p.tag(f.syntax.markOffset(closing.marker))
					if err != nil {
						return nil, err
					}
					out = append(out, formatRow{text: indent + opening + f.syntax.source[bodyStart:bodyEnd] + closing.suffix})
				} else {
					body, err := f.render(r.last+1, close, depth+1)
					if err != nil {
						return nil, err
					}
					closingText := "</" + closeMark.name + ">"
					single := ""
					canMerge := !strings.Contains(opening, "\n") && r.suffix == "" && closing.suffix == ""
					if len(body) == 1 && body[0].value && !strings.Contains(body[0].text, "\n") {
						single = strings.TrimLeft(body[0].text, "\t ")
					} else if len(body) != 0 {
						canMerge = false
					}
					candidate := indent + opening + single + closingText
					if canMerge && compactLayout(candidate) {
						out = append(out, formatRow{text: candidate, value: true})
					} else {
						out = append(out, formatRow{text: indent + r.prefix + opening + r.suffix})
						out = append(out, body...)
						out = append(out, formatRow{text: indent + closingText + closing.suffix})
					}
				}
				i = closing.last
			} else {
				out = append(out, formatRow{text: indent + r.prefix + opening + r.suffix, value: r.prefix == "" && r.suffix == "" && !strings.Contains(opening, "\n")})
				i = r.last
			}
		} else if m.kind == markupRaw {
			out = append(out, formatRow{text: indent + m.text + r.suffix})
			i = r.last
		} else {
			return nil, fmt.Errorf("unexpected markup outside element")
		}
	}
	return out, nil
}

func compactLayout(line string) bool {
	width := 0
	for _, r := range line {
		if r == '\t' {
			width += 4
		} else {
			width++
		}
	}
	return width <= formatLineWidth
}

func (f *sourceFormatter) tag(id int, indent, prefix string) (string, error) {
	m := f.syntax.marks[id]
	start := f.syntax.markOffset(id)
	p := preprocessor{source: f.syntax.source, data: []byte(f.syntax.source)}
	_, _, end, err := p.tag(start)
	if err != nil {
		return "", err
	}
	raw := f.syntax.source[start:end]
	head := "<" + m.name
	tail := ">"
	if m.selfClosing {
		tail = "/>"
	}
	pos := 1 + len(m.name)
	var attrs []string
	for pos < len(raw) {
		for pos < len(raw) && unicode.IsSpace(rune(raw[pos])) {
			pos++
		}
		if pos >= len(raw) || raw[pos] == '>' || raw[pos] == '/' {
			break
		}
		a := pos
		for pos < len(raw) && nameByte(raw[pos]) {
			pos++
		}
		name := raw[a:pos]
		if strings.HasSuffix(name, ":") && pos < len(raw) && raw[pos] == '{' {
			_, end, err := braced([]byte(raw), pos)
			if err != nil {
				return "", err
			}
			key, err := formatAttribute(raw[pos:end])
			if err != nil {
				return "", err
			}
			name += key
			pos = end
		}
		for pos < len(raw) && unicode.IsSpace(rune(raw[pos])) {
			pos++
		}
		if pos < len(raw) && raw[pos] == '=' {
			pos++
			for pos < len(raw) && unicode.IsSpace(rune(raw[pos])) {
				pos++
			}
			valueStart := pos
			if raw[pos] == '{' {
				_, next, err := braced([]byte(raw), pos)
				if err != nil {
					return "", err
				}
				pos = next
			} else {
				q := raw[pos]
				pos++
				for pos < len(raw) && raw[pos] != q {
					if raw[pos] == '{' {
						_, next, err := braced([]byte(raw), pos)
						if err != nil {
							return "", err
						}
						pos = next
					} else {
						_, n := utf8.DecodeRuneInString(raw[pos:])
						pos += n
					}
				}
				pos++
			}
			value, err := formatAttribute(raw[valueStart:pos])
			if err != nil {
				return "", err
			}
			// Keep interpolation markers, entities and escapes as expressions:
			// quoted markup and Go strings interpret those differently.
			if strings.HasPrefix(value, "{") && strings.HasSuffix(value, "}") {
				literal := strings.TrimSpace(value[1 : len(value)-1])
				if decoded, err := strconv.Unquote(literal); err == nil &&
					strings.HasPrefix(literal, "\"") &&
					!strings.ContainsAny(decoded, "\"&<>{}\\") &&
					strings.IndexFunc(decoded, unicode.IsControl) < 0 {
					value = "\"" + decoded + "\""
				}
			}
			name += "=" + value
		}
		attrs = append(attrs, name)
	}
	single := head
	if len(attrs) > 0 {
		single += " " + strings.Join(attrs, " ")
	}
	if m.selfClosing {
		single += " "
	}
	single += tail
	compact := compactLayout(indent + prefix + single)
	if len(attrs) == 0 || !strings.Contains(single, "\n") && compact {
		return single, nil
	}
	for i, attr := range attrs {
		attrs[i] = indentAttribute(attr, indent+"\t")
	}
	return head + "\n" + indent + "\t" + strings.Join(attrs, "\n"+indent+"\t") + "\n" + indent + tail, nil
}

func formatAttribute(value string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(value); {
		if value[i] != '{' {
			out.WriteByte(value[i])
			i++
			continue
		}
		expression, next, err := braced([]byte(value), i)
		if err != nil {
			return "", err
		}
		if _, err := parser.ParseExpr(expression); err != nil {
			return "", err
		}
		const prefix = "package p\n\nvar _ = "
		formatted, err := format.Source([]byte("package p\nvar _ = " + strings.TrimSpace(expression)))
		if err != nil {
			return "", err
		}
		out.WriteByte('{')
		formatted = bytes.TrimPrefix(formatted, []byte(prefix))
		formatted = bytes.TrimSuffix(formatted, []byte("\n"))
		out.Write(formatted)
		var scan scanner.Scanner
		scan.Init(token.NewFileSet().AddFile("", -1, len(formatted)), formatted, nil, scanner.ScanComments)
		lastComment := false
		for {
			_, tok, lit := scan.Scan()
			if tok == token.EOF {
				break
			}
			if tok != token.SEMICOLON {
				lastComment = tok == token.COMMENT && strings.HasPrefix(lit, "//")
			}
		}
		if lastComment {
			out.WriteByte('\n')
		}
		out.WriteByte('}')
		i = next
	}
	return out.String(), nil
}

func indentAttribute(attr, indent string) string {
	_, value, _ := strings.Cut(attr, "=")
	if !strings.Contains(attr, "\n") || !strings.HasPrefix(value, "{") {
		return attr
	}
	lines := strings.Split(attr, "\n")
	protected := map[int]bool{}
	var scan scanner.Scanner
	file := token.NewFileSet().AddFile("", -1, len(attr))
	scan.Init(file, []byte(attr), nil, scanner.ScanComments)
	for {
		pos, tok, lit := scan.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.STRING {
			start := file.Line(pos) - 1
			for i := 1; i <= strings.Count(lit, "\n"); i++ {
				protected[start+i] = true
			}
		}
	}
	for i := 1; i < len(lines); i++ {
		if !protected[i] {
			lines[i] = indent + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}
