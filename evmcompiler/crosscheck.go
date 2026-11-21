//go:build evmcrosscheck

package evmcompiler

import (
	"fmt"
	"os"
	"sync/atomic"

	"github.com/andreyvit/evm"
	"golang.org/x/tools/go/ssa"
)

// With the evmcrosscheck build tag, every package-list compile is repeated
// through a prepared Environment and the two results must match. Run the test
// suites of this module with the tag to check the prepared path against every
// source they compile:
//
//	go test -tags evmcrosscheck ./...
//
// A mismatch panics. EVM_CROSSCHECK_LOG names a file that receives one line
// per compile whose environment cannot be prepared.
//
// The tag also makes the SSA builder check each function it builds.
func init() {
	crossCheck = compareWithPrepared
	ssaChecks = ssa.SanityCheckFunctions
}

var crossChecks, crossCheckSkips atomic.Int64

func compareWithPrepared(sources map[string]string, packages []evm.Package, options Options, lazy *evm.Module, lazyErr error) {
	crossChecks.Add(1)
	env, err := NewEnvironment(packages...)
	if err != nil {
		// A package that fails to import fails a prepared environment, and a
		// package-list compile only when the source imports it.
		crossCheckSkips.Add(1)
		if path := os.Getenv("EVM_CROSSCHECK_LOG"); path != "" {
			if f, openErr := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); openErr == nil {
				fmt.Fprintf(f, "skip: lazy error %q, environment error %q\n", fmt.Sprint(lazyErr), err)
				f.Close()
			}
		}
		return
	}
	for round := 0; round < 2; round++ { // The second compile sees whatever the first left behind.
		prepared, preparedErr := env.CompileFiles(sources, options)
		if fmt.Sprint(lazyErr) != fmt.Sprint(preparedErr) {
			panic(fmt.Sprintf("prepared environment (round %d): error mismatch\npackage list: %v\nprepared:     %v\nsources: %q", round, lazyErr, preparedErr, sources))
		}
		if lazyErr != nil {
			continue
		}
		if difference := moduleDifference(lazy, prepared); difference != "" {
			panic(fmt.Sprintf("prepared environment (round %d): module mismatch: %s\nsources: %q", round, difference, sources))
		}
	}
}
