package gox

import (
	"fmt"
	"go/scanner"
	"go/token"
	"html"
	"strconv"
	"strings"
	"unicode"
)

type part struct {
	text string
	arg  int
}
type attribute struct {
	keyArg  int
	name    string
	parts   []part
	boolean bool
}
type markupKind uint8

const (
	markupInvalid markupKind = iota
	markupOpen
	markupClose
	markupRaw
	markupValue
)

func (k markupKind) String() string {
	switch k {
	case markupInvalid:
		return "invalid"
	case markupOpen:
		return "open"
	case markupClose:
		return "close"
	case markupRaw:
		return "raw"
	case markupValue:
		return "value"
	default:
		return "markupKind(" + strconv.Itoa(int(k)) + ")"
	}
}

type markup struct {
	kind        markupKind
	name        string
	attrs       []attribute
	text        string
	void        bool
	selfClosing bool
	pos         evmPos
}
type evmPos struct {
	file         string
	line, column int
}
type preprocessor struct {
	filename, source string
	data             []byte
	i                int
	out              strings.Builder
	marks            []markup
	mapping          []int
	lower            string // source with ASCII letters folded, once a raw body needs it
	// position resumes from the last offset it resolved: markers arrive in
	// source order, and counting lines from the start for each is quadratic.
	lineAt, line, lineStart int
}

// MaxTagNesting bounds elements inside elements. The formatter indents one
// level per element, so its output grows with the square of the depth, and a
// component with children compiles to a function literal per level.
const MaxTagNesting = 100

func preprocess(filename, source string, base int) (string, []markup, []int, error) {
	p := &preprocessor{filename: filename, source: source, data: []byte(source)}
	data := p.data
	var scan scanner.Scanner
	var file *token.File
	offset := 0
	reset := func() {
		offset = p.i
		file = token.NewFileSet().AddFile(filename, -1, len(data)-offset)
		scan.Init(file, data[offset:], func(token.Position, string) {}, scanner.ScanComments)
	}
	reset()
	var stack []token.Token
	prev := token.ILLEGAL
	for {
		pos, tok, _ := scan.Scan()
		i := offset + file.Offset(pos)
		if tok == token.EOF {
			p.copySource(len(source))
			break
		}
		enabled := len(stack) == 0 || stack[len(stack)-1] == token.LBRACE
		if tok == token.LSS && enabled {
			p.copySource(i)
			p.i = i
			if endsStatement(prev) {
				p.write(";", i)
			}
			if err := p.markupTag(base); err != nil {
				return "", nil, nil, err
			}
			prev = token.SEMICOLON
			reset()
			continue
		}
		switch tok {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			stack = append(stack, tok)
		case token.RPAREN, token.RBRACK, token.RBRACE:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
		if tok != token.COMMENT {
			prev = tok
		}
	}

	if id, ok := tooDeep(p.marks); ok {
		pos := p.marks[id].pos
		return "", nil, nil, fmt.Errorf("%s:%d:%d: tags nest more than %d levels", pos.file, pos.line, pos.column, MaxTagNesting)
	}
	return p.out.String(), p.marks, p.mapping, nil
}

// tooDeep finds the first opening tag nested more than MaxTagNesting levels.
// It pairs tags the way parseSource does: a void tag opens a level only when
// a closing tag names it, which is known once the whole file is read.
func tooDeep(marks []markup) (int, bool) {
	var stack []int
	paired := make([]bool, len(marks))
	for id, m := range marks {
		switch m.kind {
		case markupOpen:
			if !m.selfClosing {
				stack = append(stack, id)
			}
		case markupClose:
			for len(stack) > 0 && marks[stack[len(stack)-1]].name != m.name && marks[stack[len(stack)-1]].void {
				stack = stack[:len(stack)-1]
			}
			if len(stack) > 0 && marks[stack[len(stack)-1]].name == m.name {
				paired[stack[len(stack)-1]], paired[id] = true, true
				stack = stack[:len(stack)-1]
			}
		}
	}
	depth := 0
	for id, m := range marks {
		switch {
		case m.kind == markupOpen && !m.selfClosing && (paired[id] || !m.void):
			// An unclosed tag counts too; its own error comes later.
			depth++
			if depth > MaxTagNesting {
				return id, true
			}
		case m.kind == markupClose && paired[id]:
			depth--
		}
	}
	return 0, false
}
func (p *preprocessor) copySource(end int) {
	for p.i < end {
		p.write(p.source[p.i:p.i+1], p.i)
		p.i++
	}
}
func (p *preprocessor) write(s string, origin int) {
	p.out.WriteString(s)
	for range len(s) {
		p.mapping = append(p.mapping, origin)
	}
}
func endsStatement(t token.Token) bool {
	switch t {
	case token.IDENT, token.INT, token.FLOAT, token.IMAG, token.CHAR, token.STRING,
		token.BREAK, token.CONTINUE, token.FALLTHROUGH, token.RETURN,
		token.INC, token.DEC, token.RPAREN, token.RBRACK, token.RBRACE:
		return true
	}
	return false
}
func (p *preprocessor) position(i int) evmPos {
	if i < p.lineAt {
		p.lineAt, p.line, p.lineStart = 0, 0, 0
	}
	between := p.source[p.lineAt:i]
	if last := strings.LastIndexByte(between, '\n'); last >= 0 {
		p.line += strings.Count(between, "\n")
		p.lineStart = p.lineAt + last + 1
	}
	p.lineAt = i
	return evmPos{p.filename, p.line + 1, i - p.lineStart + 1}
}
func (p *preprocessor) fail(i int, s string) error {
	q := p.position(i)
	return fmt.Errorf("%s:%d:%d: %s", q.file, q.line, q.column, s)
}
func (p *preprocessor) marker(m markup, args []string, base, origin int) {
	p.markerAt(m, args, -1, base, origin)
}

// markerAt writes a marker whose single argument starts at argAt in the
// source, or searches for each argument after origin when argAt is negative.
func (p *preprocessor) markerAt(m markup, args []string, argAt, base, origin int) {
	m.pos = p.position(origin)
	id := base + len(p.marks)
	p.marks = append(p.marks, m)
	p.write(fmt.Sprintf("_goxMark(%d", id), origin)
	search := origin
	for _, arg := range args {
		argOrigin := origin
		if argAt >= 0 {
			argOrigin = argAt
		} else {
			for _, prefix := range []string{"{", "{="} {
				if offset := strings.Index(p.source[search:], prefix+arg+"}"); offset >= 0 {
					argOrigin = search + offset + len(prefix)
					search = argOrigin + len(arg)
					break
				}
			}
		}
		p.write(",(", argOrigin)
		for offset := range len(arg) {
			p.write(arg[offset:offset+1], min(argOrigin+offset, len(p.source)))
		}
		p.write(")", argOrigin)
	}
	p.write(")", origin)
	// An if initializer already supplies its separating semicolon.
	end := p.i
	for end < len(p.source) && unicode.IsSpace(rune(p.source[end])) {
		end++
	}
	if m.kind != markupOpen || !m.selfClosing || end >= len(p.source) || p.source[end] != ';' {
		p.write(";", origin)
	}
}
func (p *preprocessor) markupTag(base int) error {
	i := p.i
	if strings.HasPrefix(p.source[i:], "<!--") {
		end := strings.Index(p.source[i+4:], "-->")
		if end < 0 {
			return p.fail(i, "unterminated HTML comment")
		}
		p.i = i + 4 + end + 3
		p.marker(markup{kind: markupRaw, text: p.source[i:p.i]}, nil, base, i)
		return nil
	}
	if len(p.source)-i >= 9 && strings.EqualFold(p.source[i:i+9], "<!doctype") {
		end := strings.IndexByte(p.source[i:], '>')
		if end < 0 {
			return p.fail(i, "unterminated doctype")
		}
		p.i = i + end + 1
		p.marker(markup{kind: markupRaw, text: p.source[i:p.i]}, nil, base, i)
		return nil
	}
	m, args, end, err := p.tag(i)
	if err != nil {
		return err
	}
	p.i = end
	p.marker(m, args, base, i)
	if m.kind == markupOpen && !m.void && (m.name == "script" || m.name == "style") {
		if err = p.rawBody(m.name, base); err != nil {
			return err
		}
	}
	return nil
}
func braced(s []byte, start int) (string, int, error) {
	data := s[start+1:]
	file := token.NewFileSet().AddFile("", -1, len(data))
	var scan scanner.Scanner
	scan.Init(file, data, func(token.Position, string) {}, scanner.ScanComments)
	depth := 1
	for {
		pos, tok, _ := scan.Scan()
		switch tok {
		case token.EOF:
			return "", start, fmt.Errorf("unterminated interpolation")
		case token.LBRACE:
			depth++
		case token.RBRACE:
			depth--
			if depth == 0 {
				end := start + 1 + file.Offset(pos)
				return string(s[start+1 : end]), end + 1, nil
			}
		}
	}
}
func (p *preprocessor) tag(start int) (markup, []string, int, error) {
	s := p.source
	i := start + 1
	m := markup{kind: markupOpen}
	var args []string
	if i < len(s) && s[i] == '/' {
		m.kind = markupClose
		i++
	}
	if i < len(s) && s[i] == '>' {
		return m, nil, i + 1, nil
	}
	n := i
	for i < len(s) && nameByte(s[i]) {
		i++
	}
	if n == i {
		return m, nil, i, p.fail(start, "expected element name")
	}
	m.name = s[n:i]
	for {
		for i < len(s) && unicode.IsSpace(rune(s[i])) {
			i++
		}
		if i >= len(s) {
			return m, nil, i, p.fail(start, "unterminated tag")
		}
		if s[i] == '>' {
			i++
			break
		}
		if s[i] == '/' && i+1 < len(s) && s[i+1] == '>' {
			m.void = true
			m.selfClosing = true
			i += 2
			break
		}
		if m.kind == markupClose {
			return m, nil, i, p.fail(i, "closing tag has attributes")
		}
		n = i
		for i < len(s) && nameByte(s[i]) {
			i++
		}
		if n == i {
			return m, nil, i, p.fail(i, "invalid attribute name")
		}
		a := attribute{name: s[n:i], boolean: true}
		if strings.HasSuffix(a.name, ":") && i < len(s) && s[i] == '{' {
			expr, end, err := braced(p.data, i)
			if err != nil {
				return m, nil, i, p.fail(i, err.Error())
			}
			a.keyArg = len(args) + 1
			args = append(args, expr)
			i = end
		}

		for i < len(s) && unicode.IsSpace(rune(s[i])) {
			i++
		}
		if i < len(s) && s[i] == '=' {
			a.boolean = false
			i++
			for i < len(s) && unicode.IsSpace(rune(s[i])) {
				i++
			}
			if i >= len(s) {
				return m, nil, i, p.fail(start, "missing attribute value")
			}
			if s[i] == '{' {
				expr, e, err := braced(p.data, i)
				if err != nil {
					return m, nil, i, p.fail(i, err.Error())
				}
				a.parts = append(a.parts, part{arg: len(args) + 1})
				args = append(args, expr)
				i = e
			} else if s[i] == '"' || s[i] == '\'' {
				q := s[i]
				i++
				n = i
				for i < len(s) && s[i] != q {
					if s[i] == '{' {
						if i > n {
							a.parts = append(a.parts, part{text: html.UnescapeString(s[n:i])})
						}
						expr, e, err := braced(p.data, i)
						if err != nil {
							return m, nil, i, p.fail(i, err.Error())
						}
						a.parts = append(a.parts, part{arg: len(args) + 1})
						args = append(args, expr)
						i = e
						n = i
					} else {
						i++
					}
				}
				if i >= len(s) {
					return m, nil, i, p.fail(start, "unterminated quoted attribute")
				}
				if i > n || len(a.parts) == 0 {
					a.parts = append(a.parts, part{text: html.UnescapeString(s[n:i])})
				}
				i++
			} else {
				return m, nil, i, p.fail(i, "attribute values must be quoted or enclosed in braces")
			}
		}
		for _, old := range m.attrs {
			if old.name == a.name && old.keyArg == 0 && a.keyArg == 0 {
				return m, nil, i, p.fail(start, "duplicate attribute "+a.name)
			}
		}
		m.attrs = append(m.attrs, a)
	}
	if voidTags[m.name] {
		m.void = true
	}
	return m, args, i, nil
}
func nameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == ':' || c == '.'
}
func (p *preprocessor) rawBody(tag string, base int) error {
	if p.lower == "" {
		// ASCII folding preserves byte offsets even for Unicode JS/CSS literals.
		lowerBytes := []byte(p.source)
		for i, ch := range lowerBytes {
			if ch >= 'A' && ch <= 'Z' {
				lowerBytes[i] = ch + ('a' - 'A')
			}
		}
		p.lower = string(lowerBytes)
	}
	lower := p.lower
	for p.i < len(p.source) {
		start := p.i
		end := rawClose(lower, tag, start)
		// An interpolation past the closing tag belongs to later markup.
		body := p.source[start:]
		if end >= 0 {
			body = p.source[start:end]
		}
		j := strings.Index(body, "{=")
		if j >= 0 {
			j += start
		}
		if end >= 0 && (j < 0 || end <= j) {
			p.marker(markup{kind: markupRaw, text: p.source[start:end]}, nil, base, start)
			p.i = end
			return nil
		}
		if j < 0 {
			return p.fail(start, "unclosed "+tag)
		}
		if j > start {
			p.marker(markup{kind: markupRaw, text: p.source[start:j]}, nil, base, start)
		}
		expr, next, err := braced(p.data, j)
		if err != nil {
			return p.fail(j, err.Error())
		}
		// A search for the argument reads to the end of the file when the
		// body holds no other {x}, which is quadratic over many interpolations.
		p.markerAt(markup{kind: markupValue}, []string{strings.TrimPrefix(expr, "=")}, j+2, base, j)
		p.i = next
	}
	return p.fail(p.i, "unclosed "+tag)
}

func rawClose(source, tag string, start int) int {
	needle := "</" + tag
	for start < len(source) {
		i := strings.Index(source[start:], needle)
		if i < 0 {
			return -1
		}
		i += start
		end := i + len(needle)
		if end < len(source) && (source[end] == '>' || source[end] == '/' || unicode.IsSpace(rune(source[end]))) {
			return i
		}
		start = end
	}
	return -1
}

var voidTags = map[string]bool{"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true, "input": true, "link": true, "meta": true, "param": true, "source": true, "track": true, "wbr": true}

func quote(s string) string { return strconv.Quote(s) }
