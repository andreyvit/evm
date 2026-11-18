package gox

import "slices"

// File describes a GoX source file. Functions lists its named package functions
// in declaration order (excluding init, blank names and methods). Function
// literals have no package name and are not included. Files need not have
// frontmatter or functions.
type File struct {
	Filename       string // Source map key, including directories and .gox extension.
	Name           string // Basename without .gox, suitable for host resource naming.
	Functions      []string
	Frontmatter    any // Host-parsed value; treat as immutable.
	HasFrontmatter bool
}

// Files returns all GoX files in filename order. Returned slices may be modified.
func (t *Template) Files() []File {
	files := make([]File, 0, len(t.files))
	for _, file := range t.files {
		file.Functions = slices.Clone(file.Functions)
		files = append(files, file)
	}
	slices.SortFunc(files, func(a, b File) int {
		if a.Filename < b.Filename {
			return -1
		}
		if a.Filename > b.Filename {
			return 1
		}
		return 0
	})
	return files
}

// File looks up a GoX file by its exact source-map filename.
func (t *Template) File(filename string) (File, bool) {
	file, ok := t.files[filename]
	file.Functions = slices.Clone(file.Functions)
	return file, ok
}

// FunctionFile returns the GoX file declaring a named package function.
// Ordinary Go files and unknown function names return false.
func (t *Template) FunctionFile(name string) (File, bool) {
	filename, ok := t.functionFiles[name]
	if !ok {
		return File{}, false
	}
	return t.File(filename)
}

func (p *Program) Files() []File                         { return p.template.Files() }
func (p *Program) File(filename string) (File, bool)     { return p.template.File(filename) }
func (p *Program) FunctionFile(name string) (File, bool) { return p.template.FunctionFile(name) }
