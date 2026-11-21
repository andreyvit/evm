package evmcompiler

import (
	"go/ast"
	"go/token"
)

// Building a function costs more than its length in ways that are all in the
// SSA form and none in the syntax tree. The builder looks for the next block
// that needs a phi by scanning a bit per block, for each such block. It copies
// its table of local variables for each block. It keeps a dominance frontier
// entry for each pair of branches that leave for the same join, and for each
// block between a label and a goto to it. And a variable assigned on the way
// to a join takes a phi operand there for each edge into it, which lowering
// turns into a move. funcSizes estimates these from syntax, before any of it
// is built.
type funcSizes struct {
	funcs  []funcSize // the function bodies around the current node, innermost last
	joins  []joinSize // the statements around it that join control flow
	inits  funcSize   // package variable initializers run in one function
	conds  int        // whether the node is inside the condition of the innermost join
	saved  []int      // conds of the functions around a function literal
	weight int        // of the functions finished so far
	last   funcSize   // the function finished last
	serial int32
}

// funcSize is the estimate for one function body.
type funcSize struct {
	pos    token.Pos
	blocks int // basic blocks
	locals int // local variables, parameters and results
	edges  int // phi operands
	fans   int // the squares of the edges into each join, summed
	jumps  int // goto statements
	names  map[string]int32
	marks  []int32 // per name, the last join that counted it
	gotos  map[string]int
}

// joinSize is a statement after which, or at whose head, branches meet.
type joinSize struct {
	node     ast.Node
	fn       int     // index of its function in funcs
	edges    int     // edges into its joins, counted as if into one
	assigned []int32 // the names assigned inside it, with repeats
}

// weight adds the costs up in units of about eight bytes allocated, or two
// nanoseconds: a block's copy of one local variable. Four pairs of blocks
// cost as much in bit scans, a frontier entry twice as much, and a phi operand
// 64 times as much: the operand, its referrer, the move and its position.
// The weights come from measuring this builder on functions that are all of
// one construct.
func (f *funcSize) weight() int {
	return f.blocks*f.blocks/4 + f.blocks*f.locals + 2*f.fans + 2*f.jumps*f.blocks + 64*f.edges
}

func (s *funcSizes) current() *funcSize {
	if len(s.funcs) == 0 {
		return &s.inits
	}
	return &s.funcs[len(s.funcs)-1]
}

func (s *funcSizes) enter(n, parent ast.Node) {
	switch n := n.(type) {
	case *ast.FuncDecl:
		s.function(n, n.Name.Pos(), n.Recv, n.Type)
		return
	case *ast.FuncLit:
		s.saved = append(s.saved, s.conds)
		s.conds = 0
		s.function(n, n.Pos(), nil, n.Type)
		return
	}
	if s.conds > 0 {
		s.conds++
	} else if len(s.joins) > 0 {
		switch p := parent.(type) {
		case *ast.IfStmt:
			if n == ast.Node(p.Cond) && s.joins[len(s.joins)-1].node == parent {
				s.conds = 1
			}
		case *ast.ForStmt:
			if n == ast.Node(p.Cond) && s.joins[len(s.joins)-1].node == parent {
				s.conds = 1
			}
		}
	}
	f := s.current()
	switch n := n.(type) {
	case *ast.IfStmt:
		f.blocks += 2
		if n.Else != nil {
			f.blocks++
		}
		s.join(n, 2)
	case *ast.ForStmt:
		f.blocks += 4
		s.join(n, 3)
	case *ast.RangeStmt:
		f.blocks += 4
		s.join(n, 3)
		for _, e := range []ast.Expr{n.Key, n.Value} {
			s.assign(e, n.Tok == token.DEFINE)
		}
	case *ast.SwitchStmt:
		f.blocks++
		s.join(n, len(n.Body.List)+1)
	case *ast.SelectStmt:
		f.blocks++
		s.join(n, len(n.Body.List)+1)
	case *ast.TypeSwitchStmt:
		f.blocks++
		f.locals += len(n.Body.List) // each clause declares the variable anew
		s.join(n, len(n.Body.List)+1)
	case *ast.CaseClause:
		f.blocks += 1 + max(1, len(n.List))
	case *ast.CommClause:
		f.blocks += 2
	case *ast.LabeledStmt:
		f.blocks++
		s.join(n, 2)
	case *ast.BranchStmt:
		s.jump(n)
	case *ast.BinaryExpr:
		if n.Op != token.LAND && n.Op != token.LOR {
			break
		}
		f.blocks++
		if s.conds > 0 {
			s.joins[len(s.joins)-1].edges++ // one more way for the condition to fail
		} else {
			f.edges += 2 // the value is a phi of the two operands
		}
		if !f.pos.IsValid() {
			f.pos = n.Pos() // of a package variable: where its initializers first branch
		}
	case *ast.AssignStmt:
		for _, e := range n.Lhs {
			s.assign(e, n.Tok == token.DEFINE)
		}
	case *ast.IncDecStmt:
		s.assign(n.X, false)
	case *ast.ValueSpec:
		if len(s.funcs) > 0 {
			for _, name := range n.Names {
				s.assign(name, true)
			}
		}
	}
}

func (s *funcSizes) leave(n ast.Node) {
	if s.conds > 0 {
		s.conds--
	}
	if len(s.joins) == 0 || s.joins[len(s.joins)-1].node != n {
		return
	}
	j := &s.joins[len(s.joins)-1]
	f := &s.funcs[j.fn]
	// Count each name once.
	s.serial++
	distinct := j.assigned[:0]
	for _, name := range j.assigned {
		if f.marks[name] != s.serial {
			f.marks[name] = s.serial
			distinct = append(distinct, name)
		}
	}
	s.joins = s.joins[:len(s.joins)-1]
	switch n.(type) {
	case *ast.FuncDecl, *ast.FuncLit:
		// A goto can make a join of any label, for any variable.
		for _, jumps := range f.gotos {
			f.edges += (jumps + 1) * len(distinct)
			f.fans += (jumps + 1) * (jumps + 1)
			f.jumps += jumps
		}
		s.finished(f)
		s.funcs = s.funcs[:len(s.funcs)-1]
		if _, literal := n.(*ast.FuncLit); literal {
			s.conds = s.saved[len(s.saved)-1]
			s.saved = s.saved[:len(s.saved)-1]
		}
		return
	}
	f.edges += j.edges * len(distinct)
	f.fans += j.edges * j.edges
	// What is assigned inside a statement is assigned inside the ones around it.
	outer := &s.joins[len(s.joins)-1]
	outer.assigned = append(outer.assigned, distinct...)
}

// finish accounts for the initializers of package variables.
func (s *funcSizes) finish() { s.finished(&s.inits) }

func (s *funcSizes) finished(f *funcSize) {
	s.weight += f.weight()
	s.last = *f
}

func (s *funcSizes) function(n ast.Node, pos token.Pos, recv *ast.FieldList, typ *ast.FuncType) {
	s.funcs = append(s.funcs, funcSize{pos: pos, blocks: 1, names: make(map[string]int32)})
	s.joins = append(s.joins, joinSize{node: n, fn: len(s.funcs) - 1})
	for _, fields := range []*ast.FieldList{recv, typ.Params, typ.Results} {
		if fields == nil {
			continue
		}
		for _, field := range fields.List {
			for _, name := range field.Names {
				s.assign(name, true)
			}
		}
	}
}

func (s *funcSizes) join(n ast.Node, edges int) {
	if len(s.funcs) > 0 {
		s.joins = append(s.joins, joinSize{node: n, fn: len(s.funcs) - 1, edges: edges})
	}
}

// assign records an assignment to a local variable. A definition of a name
// not seen before declares it; any other may change a variable that outlives
// the statements around it, and needs a phi where they join.
func (s *funcSizes) assign(e ast.Expr, define bool) {
	name, ok := e.(*ast.Ident)
	if !ok || name.Name == "_" || len(s.funcs) == 0 {
		return
	}
	f := &s.funcs[len(s.funcs)-1]
	if define {
		f.locals++ // Unless it redeclares a variable of its own block, which is rare.
	}
	id, seen := f.names[name.Name]
	if !seen {
		if define {
			f.names[name.Name] = int32(len(f.marks))
			f.marks = append(f.marks, 0)
		}
		return // Else a package or captured variable, which has no phis.
	}
	j := &s.joins[len(s.joins)-1]
	j.assigned = append(j.assigned, id)
}

// jump adds an edge to the join a break, continue, goto or fallthrough leads to.
func (s *funcSizes) jump(n *ast.BranchStmt) {
	if len(s.funcs) == 0 {
		return
	}
	fn := len(s.funcs) - 1
	if n.Tok == token.GOTO {
		f := &s.funcs[fn]
		if f.gotos == nil {
			f.gotos = make(map[string]int)
		}
		if n.Label != nil {
			f.gotos[n.Label.Name]++
		}
		return
	}
	for i := len(s.joins) - 1; i >= 0 && s.joins[i].fn == fn; i-- {
		j := &s.joins[i]
		target := false
		switch node := j.node.(type) {
		case *ast.LabeledStmt:
			target = n.Label != nil && n.Label.Name == node.Label.Name
		case *ast.ForStmt, *ast.RangeStmt:
			target = n.Label == nil && n.Tok != token.FALLTHROUGH
		case *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
			target = n.Label == nil && n.Tok != token.CONTINUE
		}
		if target {
			j.edges++
			return
		}
	}
}
