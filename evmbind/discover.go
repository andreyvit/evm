package evmbind

import (
	"fmt"
	"go/token"
	"io/fs"
	"maps"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/andreyvit/evm"
)

// PackageExposure configures discovered native types. Pattern is an exact Go
// import path or a path ending in /... (including that path itself). The most
// specific match wins; exact matches beat descendant rules.
type PackageExposure struct {
	Pattern string
	// Package replaces the guest import path. Empty preserves the native path.
	Package string
	// Methods includes all exported native methods of discovered types.
	Methods bool
	// Exclude rejects newly discovered types in this package.
	Exclude bool
}

// Discovery supplies roots whose reachable types are exposed without source.
// Existing schemas in Options.Imports retain their declarations and identities.
type Discovery struct {
	Options  Options
	Packages []PackageExposure
}

// Discover binds functions in path and recursively exposes their concrete types.
// Options.Types adds roots, including concrete values otherwise hidden by any.
// Returned packages include newly discovered dependency bindings and must all
// be linked alongside the caller-owned packages from Options.Imports.
// Go reflection cannot enumerate package globals, functions or constants.
func Discover(path string, config Discovery) (*Schema, []evm.Package, error) {
	d := &discovery{config: config, schemas: maps.Clone(config.Options.Imports), groups: map[string]*discoveryGroup{}, visiting: map[reflect.Type]bool{}, owners: map[string]string{}}
	if d.schemas == nil {
		d.schemas = map[string]*Schema{}
	}
	for _, r := range config.Packages {
		if r.Pattern == "" {
			return nil, nil, fmt.Errorf("empty package exposure pattern")
		}
	}
	for _, t := range config.Options.Types {
		if err := d.visit(t); err != nil {
			return nil, nil, err
		}
	}
	for name, f := range config.Options.Functions {
		t := reflect.TypeOf(f)
		if !token.IsExported(name) || t == nil || t.Kind() != reflect.Func || reflect.ValueOf(f).IsNil() {
			return nil, nil, fmt.Errorf("invalid native function %s", name)
		}
		if err := d.visit(t); err != nil {
			return nil, nil, err
		}
	}
	paths := make([]string, 0, len(d.groups))
	for p := range d.groups {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if _, err := d.build(p); err != nil {
			return nil, nil, err
		}
	}
	g := &discoveryGroup{path: path, guest: path, types: map[string]reflect.Type{}, imports: map[string]string{}, methods: map[string][]string{}}
	names := make([]string, 0, len(config.Options.Functions))
	for n := range config.Options.Functions {
		names = append(names, n)
	}
	sort.Strings(names)
	var body strings.Builder
	for _, n := range names {
		sig, err := d.signature(g, reflect.TypeOf(config.Options.Functions[n]), 0)
		if err != nil {
			return nil, nil, err
		}
		fmt.Fprintf(&body, "func %s%s\n", n, sig)
	}
	opt := config.Options
	opt.Imports = d.schemas
	opt.Types = nil
	opt.NativePackage = path
	s, err := NewTypes(path, discoverySource(g, body.String()), []string{}, opt)
	if err != nil {
		return nil, nil, err
	}
	d.packages = append(d.packages, s.Bind(nil))
	return s, d.packages, nil
}

type discoveryGroup struct {
	path, guest string
	types       map[string]reflect.Type
	imports     map[string]string
	methods     map[string][]string
	building    bool
}
type discovery struct {
	config   Discovery
	schemas  map[string]*Schema
	groups   map[string]*discoveryGroup
	visiting map[reflect.Type]bool
	owners   map[string]string
	packages []evm.Package
}

func (d *discovery) rule(p string) PackageExposure {
	var best PackageExposure
	score := -1
	for _, r := range d.config.Packages {
		base := strings.TrimSuffix(r.Pattern, "/...")
		exact := base == r.Pattern
		match := p == base || !exact && strings.HasPrefix(p, base+"/")
		n := len(base) * 2
		if exact {
			n++
		}
		if match && n >= score {
			best = r
			score = n
		}
	}
	return best
}
func (d *discovery) visit(t reflect.Type) error {
	if t == nil {
		return fmt.Errorf("nil native type root")
	}
	if t == reflect.TypeFor[time.Time]() {
		return nil
	}
	if d.visiting[t] {
		return nil
	}
	d.visiting[t] = true
	if t.Name() != "" && t.PkgPath() != "" {
		if s := d.schemas[t.PkgPath()]; s != nil {
			if s.NativeType(t) == nil && s.NativeType(reflect.PointerTo(t)) == nil && s.nativeModels[reflect.PointerTo(t)] == nil {
				return fmt.Errorf("existing schema %s does not expose %s", t.PkgPath(), t)
			}
			return nil
		}
		r := d.rule(t.PkgPath())
		if r.Exclude {
			return fmt.Errorf("native type %s excluded by %s", t, r.Pattern)
		}
		if !token.IsIdentifier(t.Name()) {
			return fmt.Errorf("native generic type %s requires explicit instance bindings", t)
		}
		g := d.groups[t.PkgPath()]
		if g == nil {
			guest := r.Package
			if guest == "" {
				guest = t.PkgPath()
			}
			g = &discoveryGroup{path: t.PkgPath(), guest: guest, types: map[string]reflect.Type{}, imports: map[string]string{}, methods: map[string][]string{}}
			d.groups[g.path] = g
		}
		key := g.guest + "." + t.Name()
		if old := d.owners[key]; old != "" && old != g.path {
			return fmt.Errorf("guest type collision %s between %s and %s", key, old, g.path)
		}
		d.owners[key] = g.path
		g.types[t.Name()] = t
		if r.Methods && t.Kind() != reflect.Interface {
			for _, receiver := range []reflect.Type{t, reflect.PointerTo(t)} {
				for i := 0; i < receiver.NumMethod(); i++ {
					m := receiver.Method(i)
					if err := d.visit(m.Type); err != nil {
						return fmt.Errorf("%s.%s: %w", t, m.Name, err)
					}
				}
			}
		}
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return d.visit(t.Elem())
	case reflect.Map:
		if err := d.visit(t.Key()); err != nil {
			return err
		}
		return d.visit(t.Elem())
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.IsExported() {
				if err := d.visit(f.Type); err != nil {
					return fmt.Errorf("%s.%s: %w", t, f.Name, err)
				}
			}
		}
	case reflect.Func:
		for i := 0; i < t.NumIn(); i++ {
			if err := d.visit(t.In(i)); err != nil {
				return err
			}
		}
		for i := 0; i < t.NumOut(); i++ {
			if err := d.visit(t.Out(i)); err != nil {
				return err
			}
		}
	case reflect.Interface:
		for i := 0; i < t.NumMethod(); i++ {
			m := t.Method(i)
			if m.PkgPath != "" {
				return fmt.Errorf("native interface %s has unexported method %s", t, m.Name)
			}
			if err := d.visit(m.Type); err != nil {
				return err
			}
		}
	case reflect.Chan, reflect.UnsafePointer, reflect.Uintptr:
		return fmt.Errorf("unsupported discovered native type %s", t)
	}
	return nil
}
func (d *discovery) typ(g *discoveryGroup, t reflect.Type, underlying bool) (string, error) {
	if !underlying && t.Name() != "" {
		if t.PkgPath() == "" || t.PkgPath() == g.path {
			return t.Name(), nil
		}
		alias := g.imports[t.PkgPath()]
		if alias == "" {
			alias = fmt.Sprintf("p%d", len(g.imports))
			g.imports[t.PkgPath()] = alias
		}
		return alias + "." + t.Name(), nil
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		e, err := d.typ(g, t.Elem(), false)
		prefix := "*"
		if t.Kind() == reflect.Slice {
			prefix = "[]"
		}
		if t.Kind() == reflect.Array {
			prefix = fmt.Sprintf("[%d]", t.Len())
		}
		return prefix + e, err
	case reflect.Map:
		k, err := d.typ(g, t.Key(), false)
		if err != nil {
			return "", err
		}
		v, err := d.typ(g, t.Elem(), false)
		return "map[" + k + "]" + v, err
	case reflect.Struct:
		var b strings.Builder
		b.WriteString("struct {\n")
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				fmt.Fprintf(&b, "%s any\n", f.Name)
				continue
			}
			s, err := d.typ(g, f.Type, false)
			if err != nil {
				return "", err
			}
			if !f.Anonymous {
				b.WriteString(f.Name + " ")
			}
			b.WriteString(s)
			if f.Tag != "" {
				b.WriteString(" " + strconv.Quote(string(f.Tag)))
			}
			b.WriteString("\n")
		}
		b.WriteString("}")
		return b.String(), nil
	case reflect.Func:
		s, err := d.signature(g, t, 0)
		return "func" + s, err
	case reflect.Interface:
		var b strings.Builder
		b.WriteString("interface {\n")
		for i := 0; i < t.NumMethod(); i++ {
			m := t.Method(i)
			s, err := d.signature(g, m.Type, 0)
			if err != nil {
				return "", err
			}
			b.WriteString(m.Name + s + "\n")
		}
		b.WriteString("}")
		return b.String(), nil
	default:
		return t.Kind().String(), nil
	}
}
func (d *discovery) signature(g *discoveryGroup, t reflect.Type, skip int) (string, error) {
	var in, out []string
	for i := skip; i < t.NumIn(); i++ {
		a := t.In(i)
		prefix := ""
		if t.IsVariadic() && i == t.NumIn()-1 {
			a = a.Elem()
			prefix = "..."
		}
		s, err := d.typ(g, a, false)
		if err != nil {
			return "", err
		}
		in = append(in, prefix+s)
	}
	for i := 0; i < t.NumOut(); i++ {
		s, err := d.typ(g, t.Out(i), false)
		if err != nil {
			return "", err
		}
		out = append(out, s)
	}
	s := "(" + strings.Join(in, ",") + ")"
	if len(out) > 0 {
		s += " (" + strings.Join(out, ",") + ")"
	}
	return s, nil
}
func discoverySource(g *discoveryGroup, body string) fs.FS {
	var b strings.Builder
	b.WriteString("package native\n")
	paths := make([]string, 0, len(g.imports))
	for p := range g.imports {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		fmt.Fprintf(&b, "import %s %q\n", g.imports[p], p)
	}
	b.WriteString(body)
	return discoveryFS{b.String()}
}
func (d *discovery) build(path string) (*Schema, error) {
	if s := d.schemas[path]; s != nil {
		return s, nil
	}
	g := d.groups[path]
	if g == nil {
		return nil, fmt.Errorf("missing native package %s", path)
	}
	if g.building {
		return nil, fmt.Errorf("cyclic discovered package %s", path)
	}
	g.building = true
	names := make([]string, 0, len(g.types))
	for n := range g.types {
		names = append(names, n)
	}
	sort.Strings(names)
	var body strings.Builder
	var roots []reflect.Type
	for _, n := range names {
		t := g.types[n]
		roots = append(roots, t)
		s, err := d.typ(g, t, true)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&body, "type %s %s\n", n, s)
		if d.rule(path).Methods && t.Kind() != reflect.Interface {
			seen := map[string]bool{}
			for _, r := range []reflect.Type{t, reflect.PointerTo(t)} {
				for i := 0; i < r.NumMethod(); i++ {
					m := r.Method(i)
					if seen[m.Name] {
						continue
					}
					seen[m.Name] = true
					sig, err := d.signature(g, m.Type, 1)
					if err != nil {
						return nil, err
					}
					recv := n
					if r.Kind() == reflect.Pointer {
						recv = "*" + n
					}
					fmt.Fprintf(&body, "func (%s) %s%s\n", recv, m.Name, sig)
					g.methods[n] = append(g.methods[n], m.Name)
				}
			}
		}
	}
	for p := range g.imports {
		if p == "time" && d.groups[p] == nil {
			continue
		}
		if _, err := d.build(p); err != nil {
			return nil, err
		}
	}
	omitted := map[string][]string{}
	for n, t := range g.types {
		if t.Kind() == reflect.Struct {
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				if !f.IsExported() {
					omitted[n] = append(omitted[n], f.Name)
				}
			}
		}
	}
	s, err := NewTypes(g.guest, discoverySource(g, body.String()), names, Options{NativePackage: path, Types: roots, Imports: d.schemas, Methods: g.methods, OmitFields: omitted})
	if err != nil {
		return nil, fmt.Errorf("discover %s: %w", path, err)
	}
	d.schemas[path] = s
	d.packages = append(d.packages, s.Bind(nil))
	return s, nil
}

type discoveryFS struct{ source string }

func (f discoveryFS) Open(name string) (fs.File, error) {
	if name != "native.go" {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return &discoveryFile{Reader: strings.NewReader(f.source), size: int64(len(f.source))}, nil
}
func (f discoveryFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name != "." {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	return []fs.DirEntry{fs.FileInfoToDirEntry(discoveryInfo{int64(len(f.source))})}, nil
}

type discoveryFile struct {
	*strings.Reader
	size int64
}

func (f *discoveryFile) Close() error               { return nil }
func (f *discoveryFile) Stat() (fs.FileInfo, error) { return discoveryInfo{f.size}, nil }

type discoveryInfo struct{ size int64 }

func (discoveryInfo) Name() string       { return "native.go" }
func (f discoveryInfo) Size() int64      { return f.size }
func (discoveryInfo) Mode() fs.FileMode  { return 0444 }
func (discoveryInfo) ModTime() time.Time { return time.Time{} }
func (discoveryInfo) IsDir() bool        { return false }
func (discoveryInfo) Sys() any           { return nil }
