package evmbind

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"slices"
	"sort"
	"strconv"
	"sync"

	"github.com/andreyvit/evm/evmcompiler"
)

// Native declarations keep their Go identities while their emitted script
// declarations may be renamed or consolidated.
type schemaImport struct {
	path   string
	schema *Schema
}
type nativeSchemaImporter struct {
	packages  map[string]*types.Package
	byPackage map[*types.Package]schemaImport
}

var standardSchemaImports = struct {
	sync.Mutex
	importer types.Importer
}{importer: evmcompiler.SourceImporter(token.NewFileSet())}

func (i nativeSchemaImporter) Import(path string) (*types.Package, error) {
	if p := i.packages[path]; p != nil {
		return p, nil
	}
	standardSchemaImports.Lock()
	defer standardSchemaImports.Unlock()
	return standardSchemaImports.importer.Import(path)
}

func schemaImporter(_ *token.FileSet, files []*ast.File, imports map[string]*Schema) (types.Importer, error) {
	packages := map[string]*types.Package{}
	byPackage := map[*types.Package]schemaImport{}
	seen := map[*Schema]bool{}
	var collect func(map[string]*Schema) error
	collect = func(imports map[string]*Schema) error {
		names := make([]string, 0, len(imports))
		for name := range imports {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, path := range names {
			dep := imports[path]
			if dep == nil {
				return fmt.Errorf("nil imported schema for %s", path)
			}
			if old := packages[path]; old != nil && old != dep.src.goPackage {
				return fmt.Errorf("conflicting imported schema for %s", path)
			}
			packages[path] = dep.src.goPackage
			if old, ok := byPackage[dep.src.goPackage]; !ok || path < old.path {
				byPackage[dep.src.goPackage] = schemaImport{path, dep}
			}
			if seen[dep] {
				continue
			}
			seen[dep] = true
			if err := collect(dep.src.imports); err != nil {
				return err
			}
		}
		return nil
	}
	if err := collect(imports); err != nil {
		return nil, err
	}
	for _, f := range files {
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if dep := imports[path]; dep != nil && imp.Name == nil {
				imp.Name = ast.NewIdent(dep.src.name)
			}
		}
	}
	return nativeSchemaImporter{packages: packages, byPackage: byPackage}, nil
}

// Keep ordinary names stable for generated bindings. Disambiguate native
// packages with the same declared name using their full import paths.
func (s *source) importQualifier(path string, dep *Schema) string {
	for _, other := range s.nativeImports {
		if other.schema != dep && other.schema.src.name == dep.src.name {
			return path
		}
	}
	return dep.src.name
}

// Clone binding traversal metadata, but share immutable VM shapes and native
// layouts. Imported declarations stay in their own package and are not emitted
// again under the consuming package.
func (s *source) importNode(path string, dep *Schema, original *node) *node {
	if original.modelType != nil {
		return s.node(original.modelType)
	}
	if original.imported != nil && original.imported != dep {
		owner := original.imported
		binding, ok := s.nativeImports[owner.src.goPackage]
		if !ok {
			fail("missing transitive native import for %s", original.name)
		}
		ownerPath := binding.path
		if original.origin == nil {
			fail("missing transitive declaration for %s", original.name)
		}
		return s.importNode(ownerPath, owner, original.origin)
	}
	if n := s.importedNodes[original]; n != nil {
		return n
	}
	if !original.named && original.kind != nodePointer && original.kind != nodeRecord {
		if old := s.nodes[original.name]; old != nil && old.shape.Type == original.shape.Type && inferredNativeType(old) == inferredNativeType(original) {
			return old
		}
	}
	copy := *original
	n := &copy
	n.imported = dep
	n.origin = original
	if n.named {
		n.nativeImport = path
		if original.native != nil && original.native.PkgPath() != "" {
			n.nativeImport = original.native.PkgPath()
		}
		n.nativeName = original.name
		if original.native != nil && original.native.Name() != "" {
			n.nativeName = original.native.Name()
		}
		n.name = s.importQualifier(path, dep) + "." + original.name
	}
	s.importedNodes[original] = n
	if n.kind == nodeInterface {
		// A consumer may return an implementation without scripts importing
		// the owning package explicitly. Keep its concrete entry types linked.
		for _, typ := range dep.pkg.EntryTypes {
			if !slices.Contains(s.pkg.EntryTypes, typ) {
				s.pkg.EntryTypes = append(s.pkg.EntryTypes, typ)
			}
		}
	}
	if n.kind == nodePointer || n.kind == nodeRecord && n.elem != nil {
		n.elem = s.importNode(path, dep, original.elem)
		n.name = "*" + n.elem.name
	}
	if old := s.nodes[n.name]; old != nil && old != n {
		fail("imported type name collision: %s", n.name)
	}
	s.nodes[n.name] = n
	// Containers must refer to imported traversal nodes as well. Keeping the
	// source nodes here would look up their native types in the consumer schema.
	if n.kind == nodeSlice || n.kind == nodeMap {
		n.elem = s.importNode(path, dep, original.elem)
	}
	if n.kind == nodeMap {
		n.key = s.importNode(path, dep, original.key)
	}
	n.fields = nil
	for _, f := range original.fields {
		f.t = s.importNode(path, dep, f.t)
		n.fields = append(n.fields, f)
	}
	return n
}

// A consolidated package keeps the owning schema's typed constant declaration.
func (s *source) reexportedConstant(value *types.Const) bool {
	duplicate := false
	for _, owner := range s.nativeImports {
		if owner.schema.src.pkg.Path != s.pkg.Path {
			continue
		}
		for _, decl := range owner.schema.pkg.Constants {
			if decl.Name != value.Name() {
				continue
			}
			original, ok := owner.schema.src.goPackage.Scope().Lookup(value.Name()).(*types.Const)
			if !ok || !types.Identical(original.Type(), value.Type()) || !constant.Compare(original.Val(), token.EQL, value.Val()) {
				fail("constant %s conflicts with its consolidated declaration", value.Name())
			}
			duplicate = true
		}
	}
	return duplicate
}
