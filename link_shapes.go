package evm

import "github.com/andreyvit/evm/values"

// sharedShapes holds the linked snapshots of shapes that every module of a
// link environment can list. Snapshots refer only to each other, so programs
// share them. Nothing here changes after the environment is built.
//
// A program still sees only the snapshots its module reaches: it records them
// in a bit set and treats the rest as absent.
type sharedShapes struct {
	index     map[*values.Shape]int32 // source shape to snapshot
	position  map[*values.Shape]int32 // snapshot to its index
	snapshots []*values.Shape
	children  [][]int32 // the snapshots each one refers to directly

	byType   map[*values.Type]sharedShape // first snapshot of the type
	sameType []int32                      // next snapshot of the same type, or -1

	nativeGo   []int32 // concrete snapshots with a native Go type
	interfaces []int32 // interface snapshots with methods
}

// sharedShape is a snapshot with its index, so that the usual lookup of a
// type costs one map read and one bit test.
type sharedShape struct {
	shape *values.Shape
	index int32
}

// newSharedShapes snapshots the shapes reachable from roots. A root that
// cannot be snapshotted or stored stays out, along with what only it reaches:
// a link that lists it snapshots it again and reports the error.
func newSharedShapes(roots []*values.Shape) *sharedShapes {
	b := &sharedShapes{index: make(map[*values.Shape]int32), position: make(map[*values.Shape]int32), byType: make(map[*values.Type]sharedShape)}
	for _, root := range roots {
		if root == nil {
			continue
		}
		if _, ok := b.index[root]; ok {
			continue
		}
		group := shapeSnapshots{shared: b, own: make(map[*values.Shape]*values.Shape)}
		if _, err := group.snapshot(root); err != nil {
			continue
		}
		valid := true
		states := make(map[*values.Shape]shapeValidationState)
		for _, c := range group.order {
			if !validateStorageShape(c, states, b) {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		for _, c := range group.order {
			b.position[c] = int32(len(b.snapshots))
			b.snapshots = append(b.snapshots, c)
		}
		for source, c := range group.own {
			b.index[source] = b.position[c]
		}
	}
	b.children = make([][]int32, len(b.snapshots))
	b.sameType = make([]int32, len(b.snapshots))
	last := make(map[*values.Type]int32)
	for i, s := range b.snapshots {
		var children []int32
		eachShapeChild(s, func(child *values.Shape) {
			children = append(children, b.position[child])
		})
		b.children[i] = children
		b.sameType[i] = -1
		if prev, ok := last[s.Type]; ok {
			b.sameType[prev] = int32(i)
		} else {
			b.byType[s.Type] = sharedShape{s, int32(i)}
		}
		last[s.Type] = int32(i)
		if s.Kind == values.InterfaceKind {
			if len(s.Methods) != 0 {
				b.interfaces = append(b.interfaces, int32(i))
			}
		} else if s.Native != nil && s.Native.GoType != nil {
			b.nativeGo = append(b.nativeGo, int32(i))
		}
	}
	return b
}

// eachShapeChild visits the shapes a snapshot refers to, as snapshotShape does.
func eachShapeChild(s *values.Shape, visit func(*values.Shape)) {
	for i := range s.Methods {
		visit(s.Methods[i].Signature)
	}
	if s.Elem != nil {
		visit(s.Elem)
	}
	if s.Key != nil {
		visit(s.Key)
	}
	for _, c := range s.Params {
		visit(c)
	}
	for _, c := range s.Results {
		visit(c)
	}
	for i := range s.Fields {
		visit(s.Fields[i].Shape)
	}
}

// ofType returns a snapshot of the type among those a program reaches.
func (b *sharedShapes) ofType(t *values.Type, seen []uint64) *values.Shape {
	first, ok := b.byType[t]
	if !ok {
		return nil
	}
	if i := first.index; seen[i>>6]&(1<<(uint(i)&63)) != 0 {
		return first.shape
	}
	// Rare: several source shapes have this type, and the module reaches
	// another one.
	for i := b.sameType[first.index]; i >= 0; i = b.sameType[i] {
		if seen[i>>6]&(1<<(uint(i)&63)) != 0 {
			return b.snapshots[i]
		}
	}
	return nil
}

// shapeSnapshots maps the source shapes of one module to linked snapshots:
// the environment's shared ones and the program's own.
type shapeSnapshots struct {
	shared *sharedShapes
	seen   []uint64 // shared snapshots the module reaches; nil while the shared table is built
	own    map[*values.Shape]*values.Shape

	// order lists the own snapshots as the module first named them. Link
	// derives its tables in this order, so that a type with several shapes
	// always resolves to the same one.
	order []*values.Shape

	sharedInterface bool // a reached shared snapshot is an interface
	stack           []int32
}

func newShapeSnapshots(shared *sharedShapes) *shapeSnapshots {
	t := &shapeSnapshots{shared: shared, own: make(map[*values.Shape]*values.Shape)}
	if shared != nil {
		t.seen = make([]uint64, (len(shared.snapshots)+63)/64)
	}
	return t
}

// reach marks a shared snapshot and everything it refers to. A marked
// snapshot always has its references marked.
func (t *shapeSnapshots) reach(i int32) {
	if t.seen == nil || t.seen[i>>6]&(1<<(uint(i)&63)) != 0 {
		return
	}
	t.seen[i>>6] |= 1 << (uint(i) & 63)
	stack := append(t.stack[:0], i)
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if t.shared.snapshots[i].Kind == values.InterfaceKind {
			t.sharedInterface = true
		}
		for _, c := range t.shared.children[i] {
			if t.seen[c>>6]&(1<<(uint(c)&63)) == 0 {
				t.seen[c>>6] |= 1 << (uint(c) & 63)
				stack = append(stack, c)
			}
		}
	}
	t.stack = stack
}

// eachShared visits the reached shared snapshots among candidates.
func (t *shapeSnapshots) eachShared(candidates []int32, visit func(*values.Shape) error) error {
	for _, i := range candidates {
		if t.seen[i>>6]&(1<<(uint(i)&63)) != 0 {
			if err := visit(t.shared.snapshots[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

// ofType returns a reached snapshot of the type, or nil.
func (t *shapeSnapshots) ofType(typ *values.Type) *values.Shape {
	for _, s := range t.order {
		if s.Type == typ {
			return s
		}
	}
	if t.seen != nil {
		return t.shared.ofType(typ, t.seen)
	}
	return nil
}

// hasInterface reports whether the module reaches an interface shape.
func (t *shapeSnapshots) hasInterface() bool {
	if t.sharedInterface {
		return true
	}
	for _, s := range t.order {
		if s.Kind == values.InterfaceKind {
			return true
		}
	}
	return false
}
