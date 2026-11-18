package gox

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/scanner"
	"go/token"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
)

// CompileSources compiles ordinary Go and GoX files into one package. Each GoX
// renderer declares its name and builder parameter. Use RenderFunc to select one.
// Every file imports the packages it names, including this one.
func CompileSources(sources map[string]string, packages ...evm.Package) (*Template, error) {
	return Options{}.CompileSources(sources, packages...)
}
func (options Options) CompileSources(sources map[string]string, packages ...evm.Package) (*Template, error) {
	return options.compileSources("", sources, packages...)
}

// CompileFiles compiles one package and selects the default renderer by function
// name or filename. Component tags use exact Go callable names.
func CompileFiles(entry string, sources map[string]string, packages ...evm.Package) (*Template, error) {
	return Options{}.CompileFiles(entry, sources, packages...)
}
func (options Options) CompileFiles(entry string, sources map[string]string, packages ...evm.Package) (*Template, error) {
	return options.compileSources(entry, sources, packages...)
}

func (options Options) compileSources(entry string, sources map[string]string, packages ...evm.Package) (*Template, error) {
	result, err := options.preprocessSources(entry, sources, packages...)
	if err != nil {
		return nil, err
	}
	return result.Compile()
}

func (options Options) preprocessSources(entry string, sources map[string]string, packages ...evm.Package) (*Preprocessed, error) {
	if options.Format > CSSFormat {
		return nil, fmt.Errorf("invalid output format")
	}
	c := &compiler{
		implicitTypes: options.ImplicitTypes,
		files:         token.NewFileSet(),
		mappings:      map[string][]evm.SourcePosition{}, originals: map[string]*token.File{},
		outputMaps: map[string]*generatedMap{},
		packages:   withBuiltinPackages(packages),
		template:   &Template{sources: maps.Clone(sources), format: options.Format, GeneratedSources: map[string]string{}},
	}
	keys := slices.Sorted(maps.Keys(sources))
	if len(keys) == 0 {
		return nil, fmt.Errorf("a source file is required")
	}
	total := 0
	for _, source := range sources {
		total += len(source)
		if total > evmcompiler.MaxSourceSize {
			return nil, fmt.Errorf("combined GoX sources exceed %d bytes", evmcompiler.MaxSourceSize)
		}
	}
	c.template.files = make(map[string]File)
	c.template.functionFiles = make(map[string]string)
	prepared := maps.Clone(sources)
	for _, name := range keys {
		if path.Ext(name) != ".gox" {
			continue
		}
		raw, body, err := SplitFrontmatter(name, sources[name])
		if err != nil {
			return nil, err
		}
		file := File{Filename: name, Name: strings.TrimSuffix(path.Base(name), ".gox")}
		if raw != nil {
			value, err := options.parseFrontmatter(name, raw)
			if err != nil {
				return nil, err
			}
			file.Frontmatter, file.HasFrontmatter = value, true
			prepared[name] = blankFrontmatter(sources[name], body)
		}
		c.template.files[name] = file
	}
	pkgName := "plugin"
	if options.AllowMissingPackage {
		for _, name := range keys {
			var scan scanner.Scanner
			source := prepared[name]
			scan.Init(token.NewFileSet().AddFile(name, -1, len(source)), []byte(source), nil, 0)
			if _, kind, _ := scan.Scan(); kind == token.PACKAGE {
				if _, kind, name := scan.Scan(); kind == token.IDENT {
					pkgName = name
					break
				}
			}
		}
	}
	for _, name := range keys {
		source := prepared[name]
		if name == "_gox_helpers.go" || name == syntheticFilename {
			return nil, fmt.Errorf("%s is reserved for GoX helpers", name)
		}
		generated := source
		var mapping []int
		if path.Ext(name) == ".gox" {
			var marks []markup
			var err error
			generated, marks, mapping, err = preprocess(name, source, len(c.marks))
			if err != nil {
				return nil, err
			}
			c.marks = append(c.marks, marks...)
		} else if path.Ext(name) != ".go" {
			return nil, fmt.Errorf("%s: source filename must end in .go or .gox", name)
		}
		positions := make([]evm.SourcePosition, len(source)+1)
		line, column := 1, 1
		for i := range positions {
			positions[i] = evm.SourcePosition{File: name, Line: line, Column: column}
			if i < len(source) && source[i] == '\n' {
				line, column = line+1, 1
			} else {
				column++
			}
		}
		if mapping != nil {
			mapped := make([]evm.SourcePosition, len(mapping))
			for i, offset := range mapping {
				mapped[i] = positions[offset]
			}
			positions = mapped
		}
		if options.AllowMissingPackage && !hasPackageClause(name, generated) {
			prefix := "package " + pkgName + "\n"
			generated = prefix + generated
			positions = append(make([]evm.SourcePosition, len(prefix)), positions...)
		}
		c.mappings[name] = positions
		file, err := parser.ParseFile(c.files, name, generated, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, c.parseError(err)
		}
		c.originals[name] = c.files.File(file.Pos())
		c.syntax = append(c.syntax, file)
		for _, d := range file.Decls {
			f, ok := d.(*ast.FuncDecl)
			if !ok || f.Body == nil {
				continue
			}
			if file, ok := c.template.files[name]; ok && f.Recv == nil && f.Name.Name != "init" && f.Name.Name != "_" {
				file.Functions = append(file.Functions, f.Name.Name)
				c.template.files[name] = file
				c.template.functionFiles[f.Name.Name] = name
			}
			if err := c.collectArgDirectives(f); err != nil {
				return nil, err
			}
			builder := findBuilder(f.Type)
			if hasMarkup(f.Body) && builder == "" {
				return nil, c.error(f.Pos(), "rendering functions require an explicit first *gox.Builder parameter")
			}
			if builder == "" {
				continue
			}
			if len(f.Type.Params.List[0].Names) != 1 || f.Type.Params.List[0].Names[0].Name != builder {
				return nil, c.error(f.Pos(), "the builder must be the first parameter")
			}
			if f.Type.Results != nil && len(f.Type.Results.List) > 0 {
				if hasMarkup(f.Body) {
					return nil, c.error(f.Pos(), "rendering functions must not return values")
				}
				// Ordinary Go helpers may accept a builder and return a value.
				// They are callable from renderers, but are not render entries.
				continue
			}
			if f.Recv != nil {
				continue
			}
			if c.template.FunctionParameters == nil {
				c.template.FunctionParameters = make(map[string][]Parameter)
			}
			for _, field := range f.Type.Params.List[1:] {
				for _, id := range field.Names {
					c.template.FunctionParameters[f.Name.Name] = append(c.template.FunctionParameters[f.Name.Name], Parameter{Name: id.Name, Type: nodeString(field.Type)})
				}
			}
			if f.Name.Name == entry || name == entry && c.template.Entry == "" {
				c.template.Entry = f.Name.Name
				for _, field := range f.Type.Params.List[1:] {
					for _, id := range field.Names {
						c.template.Parameters = append(c.template.Parameters, Parameter{Name: id.Name, Type: nodeString(field.Type)})
					}
				}
			}
		}
	}
	if entry != "" && c.template.Entry == "" {
		return nil, fmt.Errorf("renderer %q is missing", entry)
	}
	// Helpers occupy their own file, so normal Go imports remain file-local.
	helper, err := parser.ParseFile(c.files, "_gox_helpers.go", "package "+c.syntax[0].Name.Name+`
func _goxMark(id int, args ...any) {}
`, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	c.syntax = append(c.syntax, helper)
	for round := 0; ; round++ {
		if round >= 64 {
			return nil, fmt.Errorf("GoX type lowering did not converge")
		}
		c.pkg, c.info, err = evmcompiler.AnalyzeFiles(c.files, c.syntax, c.packages...)
		if c.info == nil {
			return nil, err
		}
		if round == 0 {
			undefined := map[evm.SourcePosition]string{}
			if diagnostics, ok := err.(evmcompiler.Diagnostics); ok {
				for _, d := range diagnostics {
					if name, ok := strings.CutPrefix(d.Message, "undefined: "); ok {
						undefined[d.Position] = name
					}
				}
			}
			for _, file := range c.syntax {
				if path.Ext(c.files.File(file.Pos()).Name()) == ".gox" {
					c.file = file
					if err := c.checkImports(undefined); err != nil {
						return nil, err
					}
				}
			}
		}
		changed := false
		for _, file := range c.syntax {
			if path.Ext(c.files.File(file.Pos()).Name()) != ".gox" {
				continue
			}
			c.file = file
			changed = c.pipelines() || changed
			changed = c.resolveCallsAndFields() || changed
			if c.resolveErr != nil {
				return nil, c.resolveErr
			}
		}
		if changed {
			continue
		}
		for _, file := range c.syntax {
			c.file = file
			componentChanged, componentErr := c.components()
			if componentErr != nil {
				return nil, componentErr
			}
			changed = changed || componentChanged
		}
		if !changed {
			break
		}
	}
	c.pkg, c.info, err = evmcompiler.AnalyzeFiles(c.files, c.syntax, c.packages...)
	if c.info == nil {
		return nil, err
	}
	if err := c.validateAmbientDeclarations(); err != nil {
		return nil, err
	}
	c.captureParameters()
	for _, file := range c.syntax {
		c.file = file
		if file != helper {
			c.collectClasses()
		}
		if path.Ext(c.files.File(file.Pos()).Name()) != ".gox" {
			continue
		}
		for _, d := range file.Decls {
			f, ok := d.(*ast.FuncDecl)
			if !ok || f.Body == nil {
				continue
			}
			c.builder = findBuilder(f.Type)
			state := c.initialState(c.builder != "")
			if err := c.block(f.Body, &state); err != nil {
				return nil, err
			}
			if len(state.tags) != 0 {
				return nil, c.error(f.End(), state.unclosedMessage())
			}
			if c.template.format == CSSFormat && c.builder != "" {
				if _, err := state.context.Text("</style>"); err != nil {
					return nil, c.error(f.End(), err.Error())
				}
			}
			if err := state.context.Finish(); err != nil {
				return nil, c.error(f.End(), err.Error())
			}
		}
		c.batchOperations()
	}
	helper.Decls = slices.DeleteFunc(helper.Decls, func(d ast.Decl) bool {
		f, ok := d.(*ast.FuncDecl)
		return ok && f.Name.Name == "_goxMark"
	})
	for _, file := range c.syntax {
		c.file = file
		name := c.files.File(file.Pos()).Name()
		if path.Ext(name) == ".gox" {
			c.pruneImports()
		}
		var output bytes.Buffer
		if err := (&printer.Config{Mode: printer.UseSpaces | printer.TabIndent | printer.SourcePos, Tabwidth: 8}).Fprint(&output, c.files, file); err != nil {
			return nil, err
		}
		c.template.GeneratedSources[name+".go"] = output.String()
		c.template.GeneratedSource += output.String() + "\n"
	}
	// Final type checking belongs to preprocessing; SSA and bytecode do not.
	loweredFiles := token.NewFileSet()
	var lowered []*ast.File
	for _, name := range slices.Sorted(maps.Keys(c.template.GeneratedSources)) {
		source := withoutLineDirectives(c.template.GeneratedSources[name])
		file, parseErr := parser.ParseFile(loweredFiles, name, source, parser.SkipObjectResolution)
		if parseErr != nil {
			return nil, parseErr
		}
		lowered = append(lowered, file)
	}
	if _, _, err := evmcompiler.AnalyzeFiles(loweredFiles, lowered, c.packages...); err != nil {
		return nil, c.remapCompileError(err)
	}
	return &Preprocessed{GeneratedSources: c.template.GeneratedSources, compiler: c}, nil
}

// Compile validates the lowered Go and emits EVM bytecode, retaining the rendering plan.
func (p *Preprocessed) Compile() (*Template, error) {
	c := *p.compiler
	tmpl := *c.template
	c.template = &tmpl
	c.outputMaps = map[string]*generatedMap{}
	module, err := evmcompiler.CompileFiles(c.template.GeneratedSources, c.packages...)
	if err != nil {
		return nil, c.remapCompileError(err)
	}
	c.remapModule(module)
	c.template.Module = module
	return c.template, nil
}

// Mark calls while the original element and fragment boundaries still exist.
// Statements outside them retain ordinary Go argument checking.
type generatedMap struct {
	file  *token.File
	files *token.FileSet
	lines []int
}

func (c *compiler) generatedPosition(pos evm.SourcePosition) evm.SourcePosition {
	source, ok := c.template.GeneratedSources[pos.File]
	if !ok {
		return pos
	}
	m := c.outputMaps[pos.File]
	if m == nil {
		m = &generatedMap{files: token.NewFileSet(), lines: []int{0}}
		// The scanner resolves a relative //line filename against the
		// directory of the file that contains it. Register the generated
		// file without a directory so original names stay as written.
		m.file = m.files.AddFile(path.Base(pos.File), -1, len(source))
		var scan scanner.Scanner
		scan.Init(m.file, []byte(source), nil, scanner.ScanComments)
		for {
			if _, t, _ := scan.Scan(); t == token.EOF {
				break
			}
		}
		for i := range source {
			if source[i] == '\n' {
				m.lines = append(m.lines, i+1)
			}
		}
		c.outputMaps[pos.File] = m
	}
	offset := 0
	if pos.Line > 0 && pos.Line <= len(m.lines) {
		offset = m.lines[pos.Line-1]
	}
	offset = min(len(source), offset+max(0, pos.Column-1))
	p := m.files.PositionFor(m.file.Pos(offset), true)
	if p.Filename == syntheticFilename {
		return evm.SourcePosition{}
	}
	if c.originals[p.Filename] == nil {
		// The scanner also cleans //line filenames; recover the name as given.
		for name := range c.originals {
			if filepath.Clean(name) == p.Filename {
				p.Filename = name
				break
			}
		}
	}
	if original := c.originals[p.Filename]; original != nil && p.Line > 0 && p.Line <= original.LineCount() {
		offset := original.Offset(original.LineStart(p.Line)) + max(0, p.Column-1)
		if positions := c.mappings[p.Filename]; offset >= 0 && offset < len(positions) {
			return positions[offset]
		}
	}
	return evm.SourcePosition{File: p.Filename, Line: p.Line, Column: p.Column}
}
