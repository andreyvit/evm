package evmbind

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
)

// FunctionMetadata retains source names and comments for reflection-based bindings.
// Consumers decide which comment annotations to interpret or publish.
type FunctionMetadata struct {
	ParamNames []string
	Doc        string
}

// ReadFunctionMetadata reads package functions from Go source files.
func ReadFunctionMetadata(source fs.FS) (map[string]FunctionMetadata, error) {
	result := make(map[string]FunctionMetadata)
	err := fs.WalkDir(source, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := fs.ReadFile(source, path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, data, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			metadata := FunctionMetadata{}
			for _, field := range fn.Type.Params.List {
				for _, name := range field.Names {
					metadata.ParamNames = append(metadata.ParamNames, name.Name)
				}
			}
			if fn.Doc != nil {
				for _, comment := range fn.Doc.List {
					metadata.Doc += comment.Text + "\n"
				}
			}
			result[fn.Name.Name] = metadata
		}
		return nil
	})
	return result, err
}
