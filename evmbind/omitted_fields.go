package evmbind

import (
	"fmt"
	"go/ast"
	"go/token"
	"maps"
	"slices"
)

func omitTypeFields(files []*ast.File, omitted map[string][]string) error {
	remaining := make(map[string]map[string]bool, len(omitted))
	for name, fields := range omitted {
		remaining[name] = map[string]bool{}
		for _, field := range fields {
			if remaining[name][field] {
				return fmt.Errorf("duplicate omitted field %s.%s", name, field)
			}
			remaining[name][field] = true
		}
	}
	for _, file := range files {
		for _, decl := range file.Decls {
			group, ok := decl.(*ast.GenDecl)
			if !ok || group.Tok != token.TYPE {
				continue
			}
			for _, spec := range group.Specs {
				typ := spec.(*ast.TypeSpec)
				fields, ok := remaining[typ.Name.Name]
				if !ok {
					continue
				}
				body, ok := typ.Type.(*ast.StructType)
				if !ok {
					return fmt.Errorf("omitted fields require a struct: %s", typ.Name.Name)
				}
				var kept []*ast.Field
				for _, field := range body.Fields.List {
					names := field.Names
					if len(names) == 0 {
						name := embeddedFieldName(field.Type)
						if fields[name] {
							delete(fields, name)
							continue
						}
						kept = append(kept, field)
						continue
					}
					var keptNames []*ast.Ident
					for _, name := range names {
						if fields[name.Name] {
							delete(fields, name.Name)
						} else {
							keptNames = append(keptNames, name)
						}
					}
					if len(keptNames) > 0 {
						copy := *field
						copy.Names = keptNames
						kept = append(kept, &copy)
					}
				}
				for name := range fields {
					return fmt.Errorf("unknown omitted field %s.%s", typ.Name.Name, name)
				}
				body.Fields.List = kept
				delete(remaining, typ.Name.Name)
			}
		}
	}
	for name := range remaining {
		return fmt.Errorf("unknown omitted-field type %s", name)
	}
	return nil
}
func embeddedFieldName(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.StarExpr:
		return embeddedFieldName(x.X)
	case *ast.IndexExpr:
		return embeddedFieldName(x.X)
	case *ast.IndexListExpr:
		return embeddedFieldName(x.X)
	}
	return ""
}

// Normalize selection before pruning source imports. Native storage still uses
// the original struct; only the source-visible field set changes.
func selectedFieldOmissions(files []*ast.File, selected, omitted map[string][]string) (map[string][]string, error) {
	if len(selected) == 0 {
		return omitted, nil
	}
	result := maps.Clone(omitted)
	if result == nil {
		result = make(map[string][]string)
	}
	remaining := maps.Clone(selected)
	for _, file := range files {
		for _, decl := range file.Decls {
			group, ok := decl.(*ast.GenDecl)
			if !ok || group.Tok != token.TYPE {
				continue
			}
			for _, spec := range group.Specs {
				typ := spec.(*ast.TypeSpec)
				fields, ok := remaining[typ.Name.Name]
				if !ok {
					continue
				}
				if _, both := omitted[typ.Name.Name]; both {
					return nil, fmt.Errorf("both Fields and OmitFields configure %s", typ.Name.Name)
				}
				body, ok := typ.Type.(*ast.StructType)
				if !ok || typ.Assign.IsValid() {
					return nil, fmt.Errorf("selected fields require a named struct: %s", typ.Name.Name)
				}
				wanted := make(map[string]bool, len(fields))
				for _, name := range fields {
					if wanted[name] {
						return nil, fmt.Errorf("duplicate selected field %s.%s", typ.Name.Name, name)
					}
					wanted[name] = true
				}
				var hidden []string
				for _, field := range body.Fields.List {
					var names []string
					for _, name := range field.Names {
						names = append(names, name.Name)
					}
					if len(names) == 0 {
						names = []string{embeddedFieldName(field.Type)}
					}
					for _, name := range names {
						if wanted[name] {
							delete(wanted, name)
						} else {
							hidden = append(hidden, name)
						}
					}
				}
				if len(wanted) > 0 {
					return nil, fmt.Errorf("unknown selected field %s.%s", typ.Name.Name, slices.Sorted(maps.Keys(wanted))[0])
				}
				result[typ.Name.Name] = hidden
				delete(remaining, typ.Name.Name)
			}
		}
	}
	if len(remaining) > 0 {
		return nil, fmt.Errorf("unknown selected-field type %s", slices.Sorted(maps.Keys(remaining))[0])
	}
	return result, nil
}
