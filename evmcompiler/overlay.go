package evmcompiler

import (
	"fmt"
	"go/token"
	"go/types"
	"maps"
	"reflect"
	"slices"

	"github.com/andreyvit/evm/values"
	"golang.org/x/tools/go/types/typeutil"
)

// A compile adds types, shapes and layouts for the source it compiles. A type
// environment with a base keeps them in its own maps and reads everything
// else from the base, which it never writes. A type environment without a
// base owns all of its state.

// goOf returns the Go type bound to a native identity, or nil.
func (e *typeEnvironment) goOf(t *values.Type) types.Type {
	if gt := e.toGo[t]; gt != nil || e.base == nil {
		return gt
	}
	return e.base.toGo[t]
}

// typeOf returns the native identity of a Go type, or nil.
func (e *typeEnvironment) typeOf(t types.Type) *values.Type {
	if vt, _ := e.fromGo.At(t).(*values.Type); vt != nil || e.base == nil {
		return vt
	}
	vt, _ := e.base.fromGo.At(t).(*values.Type)
	return vt
}

// shapeOf returns the storage shape already built for a Go type, or nil.
func (e *typeEnvironment) shapeOf(t types.Type) *values.Shape {
	if s, _ := e.shapes.At(t).(*values.Shape); s != nil || e.base == nil {
		return s
	}
	s, _ := e.base.shapes.At(t).(*values.Shape)
	return s
}

func (e *typeEnvironment) nativeLayout(t *values.Type) *values.NativeLayout {
	if layout := e.nativeLayouts[t]; layout != nil || e.base == nil {
		return layout
	}
	return e.base.nativeLayouts[t]
}

func (e *typeEnvironment) syntheticType(s *values.Shape) reflect.Type {
	if t := e.syntheticTypes[s]; t != nil || e.base == nil {
		return t
	}
	return e.base.syntheticTypes[s]
}

// overlay returns an environment for one compile on top of a frozen one.
// It shares everything importing produced and starts with no types, shapes
// or layouts of its own.
func (e *typeEnvironment) overlay(files *token.FileSet) *typeEnvironment {
	if !e.frozen || e.base != nil {
		panic("overlay requires a frozen base environment")
	}
	o := *e
	o.base = e
	o.files = files
	o.toGo = make(map[*values.Type]types.Type)
	o.fromGo = typeutil.Map{}
	o.shapes = typeutil.Map{}
	o.shapeOrder = nil
	o.nativeLayouts = make(map[*values.Type]*values.NativeLayout)
	o.syntheticTypes = nil
	o.nativeSourceMemo = make(map[*values.Type]bool)
	o.shaping = nil
	o.buildingShapes = make(map[*values.Shape]bool)
	o.resolving = make(map[*values.Type]bool)
	// An empty slice can still share its array: appending here would write
	// into the base.
	o.pendingNativeStorage, o.pendingSourceChecks, o.pendingMapKeys, o.using = nil, nil, nil, nil
	return &o
}

// ownShapes yields the shapes built here, not those of a base. A lazily
// built environment has only shapes of imported packages.
func (e *typeEnvironment) ownShapes() func(yield func(*values.Shape) bool) {
	return func(yield func(*values.Shape) bool) {
		for _, s := range e.shapeOrder {
			if !yield(s) {
				return
			}
		}
	}
}

// frozenPackage answers an import from what freezing imported.
func (e *typeEnvironment) frozenPackage(p string) (*types.Package, error) {
	if err := e.importErrors[p]; err != nil {
		return nil, err
	}
	if pkg := e.imported[p]; pkg != nil {
		return pkg, nil
	}
	return nil, fmt.Errorf("package %q is not exposed", p)
}

// usedPackages lists, in path order, the packages whose code and types the
// guest can use. A lazily built environment imported exactly those. A frozen
// one imported everything, and the guest's imports select from it.
func (e *typeEnvironment) usedPackages(guest *types.Package) ([]*types.Package, packageSet) {
	if e.base == nil {
		packages := make([]*types.Package, 0, len(e.imported))
		for _, path := range slices.Sorted(maps.Keys(e.imported)) {
			packages = append(packages, e.imported[path])
		}
		return packages, nil
	}
	used := newPackageSet(len(e.base.importOrder))
	for _, direct := range guest.Imports() {
		used.addAll(e.base.reach[direct])
	}
	var packages []*types.Package
	for i, p := range e.base.importOrder {
		if used.has(i) {
			packages = append(packages, p)
		}
	}
	return packages, used
}

// packageSet is a set of indexes into importOrder.
type packageSet []uint64

func newPackageSet(n int) packageSet { return make(packageSet, (n+63)/64) }
func (s packageSet) add(i int)       { s[i/64] |= 1 << (i % 64) }
func (s packageSet) has(i int) bool  { return s[i/64]&(1<<(i%64)) != 0 }
func (s packageSet) addAll(other packageSet) (changed bool) {
	for i, word := range other {
		if s[i]|word != s[i] {
			s[i] |= word
			changed = true
		}
	}
	return changed
}
func (s packageSet) within(other packageSet) bool {
	for i, word := range s {
		if word&^other[i] != 0 {
			return false
		}
	}
	return true
}

// addSourceTexts adds the sources of the used host packages by the filenames
// positions report.
func (e *typeEnvironment) addSourceTexts(sources map[string]string, used []*types.Package) {
	if e.base != nil {
		for _, p := range used {
			maps.Copy(sources, e.base.sourceTexts[p])
		}
		return
	}
	for _, pkg := range e.standard {
		for i, file := range pkg.files {
			sources[e.files.PositionFor(file.Pos(), false).Filename] = pkg.sources[i].Text
		}
	}
}

// dereferenceWrite reports whether source assigns through the dereference at pos.
func (c *typedCompiler) dereferenceWrite(pos token.Pos) bool {
	return c.dereferenceWrites[pos] || c.env.base != nil && c.env.base.dereferenceWrites[pos]
}

// use records that the package being imported needs package p.
func (e *typeEnvironment) use(p string) {
	if p == "" || len(e.using) == 0 {
		return
	}
	user := e.using[len(e.using)-1]
	if e.uses[user] == nil {
		e.uses[user] = make(map[string]bool)
	}
	e.uses[user][p] = true
}

// enter records the use of p and makes it the user until the result is called.
func (e *typeEnvironment) enter(p string) (leave func()) {
	e.use(p)
	e.using = append(e.using, p)
	return func() { e.using = e.using[:len(e.using)-1] }
}
