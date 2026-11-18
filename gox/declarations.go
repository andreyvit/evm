package gox

import (
	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"strings"
)

// PackageSource prints declarations with GoX argument annotations.
func PackageSource(path string, packages ...evm.Package) (string, error) {
	return (evmcompiler.DeclarationOptions{Annotations: func(_, _, doc string) []string {
		var lines []string
		for _, line := range strings.Split(doc, "\n") {
			if strings.HasPrefix(line, "//gox:arg ") {
				lines = append(lines, line)
			}
		}
		return lines
	}}).PackageSource(path, packages...)
}

// PackagesSource prints all exposed packages with GoX annotations.
func PackagesSource(packages ...evm.Package) (string, error) {
	var sources []string
	for _, path := range evmcompiler.PackagePaths(packages...) {
		source, err := PackageSource(path, packages...)
		if err != nil {
			return "", err
		}
		sources = append(sources, source)
	}
	return strings.Join(sources, "\n"), nil
}
