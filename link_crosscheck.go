//go:build evmcrosscheck

package evm

import (
	"fmt"
	"strings"

	"github.com/andreyvit/evm/values"
)

// With the evmcrosscheck build tag, every link from a package list is
// repeated through prepared link environments and the programs must match.
// One environment shares no shapes; the other shares every shape the module
// names, as an environment built from the module's compile environment
// would. Run the test suites of this module with the tag to check the
// prepared path against every module they link:
//
//	go test -tags evmcrosscheck ./...
//
// A mismatch panics.
func init() { linkCrossCheck = compareWithPreparedLink }

func compareWithPreparedLink(m *Module, hostData any, packages []Package, linked *Program, linkErr error) {
	plain, err := NewLinkEnvironment(packages...)
	if err != nil {
		// Packages that fail an environment fail every link, with that
		// error or with an earlier one about the module.
		if linkErr == nil {
			panic(fmt.Sprintf("link environment failed for packages that link: %v", err))
		}
		return
	}
	var shapes []*values.Shape
	if m != nil {
		shapes = append(shapes, m.Shapes...)
		shapes = append(shapes, m.Globals...)
		for _, op := range m.ExtendedOps {
			shapes = append(shapes, op.Shape, op.Source)
		}
		for _, t := range m.Types {
			shapes = append(shapes, t.Shape)
		}
		for _, op := range m.Invokes {
			shapes = append(shapes, op.Method.Signature)
		}
	}
	shared, err := LinkOptions{Shapes: shapes}.NewEnvironment(packages...)
	if err != nil {
		panic(fmt.Sprintf("link environment with shared shapes: %v", err))
	}
	want := ""
	if linkErr == nil {
		want = programListing(linked)
	}
	for name, env := range map[string]*LinkEnvironment{"plain": plain, "shared": shared} {
		for round := 0; round < 2; round++ {
			p, err := env.LinkWithHostData(m, hostData)
			if fmt.Sprint(linkErr) != fmt.Sprint(err) {
				panic(fmt.Sprintf("%s link environment (round %d): error mismatch\npackage list: %v\nenvironment:  %v", name, round, linkErr, err))
			}
			if linkErr != nil {
				continue
			}
			if got := programListing(p); got != want {
				a, b := strings.Split(want, "\n"), strings.Split(got, "\n")
				for i := 0; i < len(a) && i < len(b); i++ {
					if a[i] != b[i] {
						panic(fmt.Sprintf("%s link environment (round %d): program mismatch at line %d\npackage list: %s\nenvironment:  %s", name, round, i+1, a[i], b[i]))
					}
				}
				panic(fmt.Sprintf("%s link environment (round %d): program listings have %d and %d lines", name, round, len(a), len(b)))
			}
		}
	}
}
