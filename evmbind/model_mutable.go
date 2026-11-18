package evmbind

import (
	"github.com/andreyvit/evm/values"
	"reflect"
	"strings"
)

func (s *source) annotateMutable(op *operation) {
	key := op.name
	if op.recv != nil {
		key = strings.TrimPrefix(op.recv.name, "*") + "." + op.name
	}
	annotation, ok := s.mutable[key]
	if !ok {
		return
	}
	s.mutableUsed[key] = true
	apply := func(nodes []*node, positions []int, direction string) {
		seen := map[int]bool{}
		for _, i := range positions {
			if i < 0 || i >= len(nodes) || seen[i] {
				fail("mutable %s: invalid or repeated %s index %d", key, direction, i)
			}
			seen[i] = true
			if direction == "parameter" && op.variadic && i == len(nodes)-1 {
				fail("mutable %s: variadic parameter is not supported", key)
			}
			n := nodes[i]
			if n.kind != nodeRecord || n.native == nil || n.native.Kind() != reflect.Pointer {
				fail("mutable %s: %s %d must be a direct native model pointer", key, direction, i)
			}
			if n.model.mutable == nil {
				fail("mutable %s: model %s has no editable row declaration", key, n.model.row.Model.Name())
			}
			name := n.name + "$mutable"
			copy := s.nodes[name]
			if copy == nil {
				clone := *n
				copy = &clone
				copy.name = name
				model := *n.model
				model.row = model.mutable
				copy.model = &model
				copy.shape = &values.Shape{Type: model.row.Pointer, Kind: values.TypedPairKind, PointerLike: true, Cells: 1}
				copy.nativeLayout = nil
				s.nodes[name] = copy
			}
			nodes[i] = copy
		}
	}
	if len(annotation.Params)+len(annotation.Results) == 0 {
		fail("mutable %s: no slots selected", key)
	}
	apply(op.args, annotation.Params, "parameter")
	apply(op.outs, annotation.Results, "result")
}
