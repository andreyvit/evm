package gox

import "github.com/andreyvit/evm"

// Preprocessed holds lowered Go and the rendering plan needed to compile it.
// GeneratedSources is for inspection, not standalone native Go builds.
// Treat the result and its maps as immutable.
type Preprocessed struct {
	GeneratedSources map[string]string
	compiler         *compiler
}

// Preprocess lowers one GoX file with the supplied EVM package descriptions.
// It validates types without generating bytecode or linking host implementations.
func Preprocess(filename, source string, packages ...evm.Package) (*Preprocessed, error) {
	return Options{}.preprocessSources(filename, map[string]string{filename: source}, packages...)
}

// PreprocessSources lowers a mixed Go/GoX package without selecting a render entry.
func PreprocessSources(sources map[string]string, packages ...evm.Package) (*Preprocessed, error) {
	return Options{}.preprocessSources("", sources, packages...)
}

func (o Options) Preprocess(filename, source string, packages ...evm.Package) (*Preprocessed, error) {
	return o.preprocessSources(filename, map[string]string{filename: source}, packages...)
}

func (o Options) PreprocessSources(sources map[string]string, packages ...evm.Package) (*Preprocessed, error) {
	return o.preprocessSources("", sources, packages...)
}

// PreprocessFiles selects a default renderer by function name or GoX filename.
func PreprocessFiles(entry string, sources map[string]string, packages ...evm.Package) (*Preprocessed, error) {
	return Options{}.preprocessSources(entry, sources, packages...)
}

func (o Options) PreprocessFiles(entry string, sources map[string]string, packages ...evm.Package) (*Preprocessed, error) {
	return o.preprocessSources(entry, sources, packages...)
}
