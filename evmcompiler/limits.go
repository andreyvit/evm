package evmcompiler

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"sync"

	"github.com/andreyvit/evm"
)

// Guest source reaches the compiler inside a running host. Type checking and
// compilation cost grows faster than the source with nesting depth and with
// folded constants, and Go can neither stop a goroutine nor cap its heap. The
// limits below reject such source from its syntax alone, before the type
// checker runs.
const (
	// MaxTypeNesting bounds type expressions such as [][]*map[string]T: building
	// storage shapes is worse than cubic in their depth.
	MaxTypeNesting = 32
	// MaxExprNesting bounds one expression; a+b+c nests two levels. The checker,
	// SSA builder and printer all recurse once per level.
	MaxExprNesting = 500
	// MaxStmtNesting bounds blocks, branches and loops inside a function, else-if
	// chains included: each level is a scope every inner name lookup walks.
	MaxStmtNesting = 100
	// MaxFuncNesting bounds function literals inside function literals: each
	// level captures the variables of all levels around it.
	MaxFuncNesting = 32
	// MaxConstStringSize bounds a constant string expression: the checker folds
	// a+a at compile time, so a few short lines can build gigabytes.
	MaxConstStringSize = 1 << 20
)

// maxLimitErrors keeps a file with one mistake per line from flooding the caller.
const maxLimitErrors = 10

// LimitError reports syntax that exceeds one of the limits.
type LimitError struct {
	Pos     token.Pos
	Message string
}

// CheckLimits reports syntax of one package that exceeds the limits. Compile
// and AnalyzeFiles apply it themselves; a frontend calls it on the source its
// author wrote, to report positions there before lowering changes the syntax.
// Packages supply the sizes of imported string constants.
func CheckLimits(syntax []*ast.File, packages ...evm.Package) []LimitError {
	return checkLimits(syntax, packages, 0)
}

func (c *typedCompiler) checkLimits(syntax []*ast.File, packages []evm.Package, options Options) {
	if len(c.errors) > 0 {
		return // A file failed to parse; its errors are the ones to show.
	}
	for _, e := range checkLimits(syntax, packages, options.LoweredStmtNesting) {
		c.fail(e.Pos, "%s", e.Message)
	}
}

// checkLimits walks each file once for nesting and once more for constant
// sizes. The first walk does not enter a node past a cap and the second runs
// only on files the first accepted, so their own stack and time stay bounded
// on any input the parser accepts.
func checkLimits(syntax []*ast.File, packages []evm.Package, loweredStmtNesting int) []LimitError {
	n := nestingChecker{maxStmt: MaxStmtNesting + loweredStmtNesting}
	for _, file := range syntax {
		n.check(file)
	}
	if len(n.errors) > 0 {
		return n.errors
	}
	if !n.additions {
		return nil
	}
	// Any imported constant is first taken as the largest one. Sizes by name
	// need the host's sources parsed, so only source that fails this test
	// pays for it.
	imports := guestImports(syntax, packages)
	coarse := imports.largestConst()
	errors := newConstSizes(n.consts, imports.dot, func(string) int { return coarse }).check(syntax)
	if len(errors) == 0 || coarse == 0 {
		return errors
	}
	return newConstSizes(n.consts, imports.dot, imports.constSizes(coarse)).check(syntax)
}

type nestingKind uint8

const (
	nestsType nestingKind = 1 << iota
	nestsExpr
	nestsStmt
	nestsFunc
	nestsBody // a function literal body: type and expression depth restart inside
)

type nestingFrame struct {
	node      ast.Node
	kinds     nestingKind
	typ, expr int
}

type nestingChecker struct {
	maxStmt   int
	errors    []LimitError
	consts    []*ast.GenDecl
	additions bool
	stack     []nestingFrame
	typ       int
	expr      int
	stmt      int
	fn        int
}

func (c *nestingChecker) fail(pos token.Pos, format string, args ...any) {
	c.errors = append(c.errors, LimitError{Pos: pos, Message: fmt.Sprintf(format, args...)})
}

func (c *nestingChecker) check(file *ast.File) {
	c.stack, c.typ, c.expr, c.stmt, c.fn = c.stack[:0], 0, 0, 0, 0
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			top := c.stack[len(c.stack)-1]
			c.stack = c.stack[:len(c.stack)-1]
			if top.kinds&nestsType != 0 {
				c.typ--
			}
			if top.kinds&nestsExpr != 0 {
				c.expr--
			}
			if top.kinds&nestsStmt != 0 {
				c.stmt--
			}
			if top.kinds&nestsFunc != 0 {
				c.fn--
			}
			if top.kinds&nestsBody != 0 {
				c.typ, c.expr = top.typ, top.expr
			}
			return true
		}
		if len(c.errors) >= maxLimitErrors {
			return false
		}
		var parent ast.Node
		if len(c.stack) > 0 {
			parent = c.stack[len(c.stack)-1].node
		}
		frame := nestingFrame{node: n, kinds: nestingKinds(n, parent)}
		switch n := n.(type) {
		case *ast.GenDecl:
			if n.Tok == token.CONST {
				c.consts = append(c.consts, n)
			}
		case *ast.BinaryExpr:
			if n.Op == token.ADD {
				c.additions = true
			}
		}
		// A node past a cap is reported and not entered, which is what bounds
		// the walk: every nested path counts towards at least one cap.
		switch {
		case frame.kinds&nestsType != 0 && c.typ >= MaxTypeNesting:
			// Without types, a dereference cannot be told from a pointer type,
			// nor an index from a type argument, so each is named as written.
			what := "type nests"
			switch n.(type) {
			case *ast.ArrayType, *ast.MapType, *ast.ChanType, *ast.FuncType, *ast.StructType, *ast.InterfaceType, *ast.Ellipsis:
			case *ast.StarExpr:
				what = "pointers nest"
			default:
				what = "index nests"
			}
			c.fail(n.Pos(), "%s more than %d levels", what, MaxTypeNesting)
			return false
		case frame.kinds&nestsExpr != 0 && c.expr >= MaxExprNesting:
			c.fail(n.Pos(), "expression nests more than %d levels", MaxExprNesting)
			return false
		case frame.kinds&nestsStmt != 0 && c.stmt >= c.maxStmt:
			c.fail(n.Pos(), "statements nest more than %d levels", MaxStmtNesting)
			return false
		case frame.kinds&nestsFunc != 0 && c.fn >= MaxFuncNesting:
			c.fail(n.Pos(), "function literals nest more than %d levels", MaxFuncNesting)
			return false
		}
		if frame.kinds&nestsType != 0 {
			c.typ++
		}
		if frame.kinds&nestsExpr != 0 {
			c.expr++
		}
		if frame.kinds&nestsStmt != 0 {
			c.stmt++
		}
		if frame.kinds&nestsFunc != 0 {
			c.fn++
		}
		if frame.kinds&nestsBody != 0 {
			frame.typ, frame.expr = c.typ, c.expr
			c.typ, c.expr = 0, 0
		}
		c.stack = append(c.stack, frame)
		return true
	})
}

// nestingKinds says which depths a node adds a level to.
func nestingKinds(n, parent ast.Node) nestingKind {
	var kinds nestingKind
	switch n.(type) {
	case *ast.Ident, *ast.BasicLit:
		// Operands end a nesting chain; they are not a level of it.
		return 0
	case *ast.FuncType:
		kinds = nestsExpr
		switch parent.(type) {
		case *ast.FuncDecl, *ast.FuncLit:
			// The signature of a function is not a type inside a type.
		default:
			kinds |= nestsType
		}
	case *ast.ArrayType, *ast.MapType, *ast.ChanType, *ast.StructType, *ast.InterfaceType, *ast.StarExpr, *ast.Ellipsis:
		kinds = nestsType | nestsExpr
	case *ast.FuncLit:
		kinds = nestsFunc | nestsExpr
	case ast.Expr:
		kinds = nestsExpr
	case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt, *ast.LabeledStmt:
		kinds = nestsStmt
	case *ast.BlockStmt:
		// A statement and its own braces are one level, and so is a function
		// declaration with its body.
		switch parent.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt, *ast.FuncDecl:
		case *ast.FuncLit:
			kinds = nestsStmt | nestsBody
		default:
			kinds = nestsStmt
		}
	}
	// Type arguments nest types the way brackets nest slices: L[L[L[int]]].
	switch parent := parent.(type) {
	case *ast.IndexExpr:
		if parent.Index == n {
			kinds |= nestsType
		}
	case *ast.IndexListExpr:
		if _, operand := n.(ast.Expr); operand && parent.X != n {
			kinds |= nestsType
		}
	}
	return kinds
}

// constSizes estimates how long a constant string expression can be without
// building it. A string literal counts its bytes, + adds, and a name takes the
// largest constant of that name declared anywhere in the package, since scopes
// are not resolved yet. Conversions and calls keep the size of their largest
// argument. Anything else is not a constant string and counts as nothing.
type constSizes struct {
	consts    map[string]*constName
	imported  func(name string) int
	dot       bool // a dot import can declare any name
	resolving bool
	pending   []*constName
	exceeded  bool // the sum being reported on already has its diagnostic
	errors    []LimitError
}

type constName struct {
	values []ast.Expr
	size   int
	state  constState
}

type constState uint8

const (
	constUnsized constState = iota
	constSizing
	constSized
)

func (s constState) String() string {
	switch s {
	case constUnsized:
		return "unsized"
	case constSizing:
		return "sizing"
	case constSized:
		return "sized"
	default:
		return "constState(" + strconv.Itoa(int(s)) + ")"
	}
}

func newConstSizes(decls []*ast.GenDecl, dot bool, imported func(string) int) *constSizes {
	s := &constSizes{consts: make(map[string]*constName), dot: dot, imported: imported}
	for _, decl := range decls {
		// A specification without values repeats the previous one.
		var values []ast.Expr
		for _, spec := range decl.Specs {
			spec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if len(spec.Values) > 0 {
				values = spec.Values
			}
			for i, name := range spec.Names {
				if i >= len(values) {
					break
				}
				c := s.consts[name.Name]
				if c == nil {
					c = &constName{}
					s.consts[name.Name] = c
				}
				c.values = append(c.values, values[i])
			}
		}
	}
	return s
}

// check reports each expression in the files that may exceed the limit.
func (s *constSizes) check(syntax []*ast.File) []LimitError {
	// A constant's value and any other sum are sized once, from the outside.
	// covered marks the operands that have been counted that way.
	type frame struct {
		node    ast.Node
		covered bool
	}
	var stack []frame
	for _, file := range syntax {
		stack = stack[:0]
		ast.Inspect(file, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			if len(s.errors) >= maxLimitErrors {
				return false
			}
			var parent frame
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			covered := false
			switch p := parent.node.(type) {
			case *ast.ParenExpr:
				covered = parent.covered
			case *ast.BinaryExpr:
				covered = parent.covered && p.Op == token.ADD
			case *ast.CallExpr:
				covered = parent.covered && p.Fun != n
			case *ast.ValueSpec:
				if value, ok := n.(ast.Expr); ok && parent.covered {
					covered = slices.Contains(p.Values, value)
				}
			}
			switch n := n.(type) {
			case *ast.ValueSpec:
				if decl, ok := parent.node.(*ast.GenDecl); ok && decl.Tok == token.CONST {
					for _, value := range n.Values {
						s.root(value)
					}
					covered = true
				}
			case *ast.BinaryExpr:
				if n.Op == token.ADD && !covered {
					s.root(n)
					covered = true
				}
			}
			stack = append(stack, frame{n, covered})
			return true
		})
	}
	return s.errors
}

const constTooLong = "constant string may exceed "

// root sizes an expression that no other one counts and reports it once.
func (s *constSizes) root(e ast.Expr) {
	s.exceeded = false
	if s.expr(e, true) > MaxConstStringSize && !s.exceeded {
		s.errors = append(s.errors, LimitError{Pos: e.Pos(), Message: constTooLong + strconv.Itoa(MaxConstStringSize) + " bytes"})
	}
}

// expr returns the size estimate of e, at most MaxConstStringSize+1. With
// report set it records the first innermost sum past the limit, once for the
// whole expression, and counts a constant that is itself past the limit as
// empty: check reports that constant where it is declared.
func (s *constSizes) expr(e ast.Expr, report bool) int {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			return min(max(0, len(e.Value)-2), MaxConstStringSize+1)
		}
	case *ast.ParenExpr:
		return s.expr(e.X, report)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			break
		}
		size := min(s.expr(e.X, report)+s.expr(e.Y, report), MaxConstStringSize+1)
		if report && s.exceeded {
			return 0
		}
		if report && size > MaxConstStringSize {
			s.errors = append(s.errors, LimitError{Pos: e.Pos(), Message: constTooLong + strconv.Itoa(MaxConstStringSize) + " bytes"})
			s.exceeded = true
			return 0
		}
		return size
	case *ast.Ident:
		size := s.name(e.Name)
		if report && size > MaxConstStringSize && s.consts[e.Name] != nil {
			return 0
		}
		return size
	case *ast.SelectorExpr:
		if _, ok := e.X.(*ast.Ident); ok {
			return min(s.imported(e.Sel.Name), MaxConstStringSize+1)
		}
	case *ast.CallExpr:
		// string(r) of a constant rune is a constant of up to four bytes.
		size := 4
		for _, arg := range e.Args {
			size = max(size, s.expr(arg, report))
		}
		if fun, ok := e.Fun.(*ast.Ident); ok && (fun.Name == "len" || fun.Name == "cap") {
			return 0
		}
		return size
	}
	return 0
}

// name returns the size of the largest constant with this name. Constants
// refer to each other in any order and in chains as long as the file, so the
// names are resolved with an explicit stack. A constant that refers to itself
// counts as empty there; the type checker reports the cycle.
func (s *constSizes) name(name string) int {
	c := s.consts[name]
	if c == nil {
		if s.dot {
			return min(s.imported(name), MaxConstStringSize+1)
		}
		return 0
	}
	if c.state == constSizing {
		return 0
	}
	if c.state == constUnsized {
		if s.resolving {
			// resolve sizes this one first and then returns to its user.
			s.pending = append(s.pending, c)
			return 0
		}
		s.resolve(c)
	}
	return c.size
}

func (s *constSizes) resolve(first *constName) {
	s.resolving = true
	stack := []*constName{first}
	for len(stack) > 0 {
		c := stack[len(stack)-1]
		if c.state == constSized {
			stack = stack[:len(stack)-1]
			continue
		}
		c.state = constSizing
		s.pending = s.pending[:0]
		size := 0
		for _, value := range c.values {
			size = max(size, s.expr(value, false))
		}
		if len(s.pending) > 0 {
			stack = append(stack, s.pending...)
			continue
		}
		c.size, c.state = size, constSized
		stack = stack[:len(stack)-1]
	}
	s.resolving = false
}

// limitImports are the packages the files import, as far as the host
// supplied them.
type limitImports struct {
	paths    []string
	dot      bool
	packages []evm.Package // the host's and the built-in time package, if imported
}

func guestImports(syntax []*ast.File, packages []evm.Package) limitImports {
	imports := limitImports{packages: packages}
	seen := map[string]bool{}
	for _, file := range syntax {
		for _, spec := range file.Imports {
			if spec.Name != nil && spec.Name.Name == "." {
				imports.dot = true
			}
			path, err := strconv.Unquote(spec.Path.Value)
			if err == nil && !seen[path] {
				seen[path] = true
				imports.paths = append(imports.paths, path)
			}
		}
	}
	if seen["time"] {
		// The type environment adds the built-in time package the same way.
		if merged, err := evm.WithTimePackage(packages); err == nil {
			imports.packages = merged
		}
	}
	return imports
}

// sourceConstSizes estimates each string constant that a host package
// declares in source. unknown is the size of a constant it takes from
// another package.
func sourceConstSizes(sources []evm.SourceFile, unknown int) (map[string]int, bool) {
	var decls []*ast.GenDecl
	files := token.NewFileSet()
	for _, source := range sources {
		file, err := parser.ParseFile(files, source.Name, source.Text, parser.SkipObjectResolution)
		if err != nil {
			return nil, false
		}
		for _, decl := range file.Decls {
			if decl, ok := decl.(*ast.GenDecl); ok && decl.Tok == token.CONST {
				decls = append(decls, decl)
			}
		}
	}
	// A selector names a constant of another package, of unknown size.
	s := newConstSizes(decls, false, func(string) int { return unknown })
	sizes := make(map[string]int, len(s.consts))
	for name := range s.consts {
		if size := s.name(name); size > 0 {
			sizes[name] = size
		}
	}
	return sizes, true
}

// The standard packages are the same in every compilation.
var standardConstSizes = sync.OnceValue(func() map[string]map[string]int {
	sizes := map[string]map[string]int{}
	for _, path := range evm.StandardLibraryPaths() {
		source, _ := evm.StandardLibrarySource(path)
		if consts, ok := sourceConstSizes([]evm.SourceFile{{Name: "source.go", Text: source}}, 0); ok {
			sizes[path] = consts
		} else {
			sizes[path] = map[string]int{"": len(source)}
		}
	}
	return sizes
})

// declaredConstSize returns the length of a string constant that a host
// declares by value, or false for a constant of another kind.
func declaredConstSize(c *evm.ExternalConstant) (int, bool) {
	switch {
	case c.Untyped != nil:
		if c.Untyped.Kind() == constant.String {
			return len(constant.StringVal(c.Untyped)), true
		}
	case c.Value.P != nil:
		// Only a string constant carries a pointer; R is its length.
		return int(min(c.Value.R, MaxConstStringSize+1)), true
	}
	return 0, false
}

// each calls f for every imported package the host supplied.
func (imports limitImports) each(f func(p *evm.Package)) {
	for _, path := range imports.paths {
		for i := range imports.packages {
			if p := &imports.packages[i]; p.Path == path {
				f(p)
			}
		}
	}
}

// largestConst returns an upper bound on every imported string constant, from
// data at hand: a declared constant gives its length, and a source package is
// taken to hold no constant longer than its source. That holds unless the
// host's own source repeats a constant until it outgrows the file.
func (imports limitImports) largestConst() int {
	largest := 0
	for _, path := range imports.paths {
		for _, size := range standardConstSizes()[path] {
			largest = max(largest, size)
		}
	}
	imports.each(func(p *evm.Package) {
		sources := len(p.Source)
		for _, file := range p.SourceFiles {
			sources += len(file.Text)
		}
		largest = max(largest, sources)
		for i := range p.Constants {
			if size, ok := declaredConstSize(&p.Constants[i]); ok {
				largest = max(largest, size)
			}
		}
	})
	return largest
}

// constSizes returns the size of an imported string constant by name: its
// length when the host declares it by value, an estimate when it is declared
// in source. A source package that does not parse can hold any name; the type
// checker reports it.
func (imports limitImports) constSizes(largest int) func(name string) int {
	sizes := map[string]int{}
	for _, path := range imports.paths {
		for name, size := range standardConstSizes()[path] {
			sizes[name] = max(sizes[name], size)
		}
	}
	imports.each(func(p *evm.Package) {
		for i := range p.Constants {
			if size, ok := declaredConstSize(&p.Constants[i]); ok {
				sizes[p.Constants[i].Name] = max(sizes[p.Constants[i].Name], size)
			}
		}
		if !p.HasSource() {
			return
		}
		consts, ok := sourceConstSizes(p.Sources(), largest)
		if !ok {
			sizes[""] = largest
		}
		for name, size := range consts {
			sizes[name] = max(sizes[name], size)
		}
	})
	return func(name string) int { return max(sizes[name], sizes[""]) }
}
