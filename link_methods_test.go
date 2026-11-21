package evm_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

// A module whose method tables or method imports are wrong fails to link with
// the same error from the package list, through an environment that binds
// method imports for each program, and through one that bound the module's
// method tables when it was built. None of them crash or link the wrong
// native.
func TestMethodImportErrors(t *testing.T) {
	packages := linkTestPackages(t)
	compiler, err := evmcompiler.NewEnvironment(packages...)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := evm.NewLinkEnvironment(packages...)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := compiler.NewLinkEnvironment(packages...)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := prepared.Fingerprint()

	// table returns a type with a method table that names only method
	// imports, and the import its first method names.
	table := func(m *evm.Module) (int, int) {
		for i, typ := range m.Types {
			if typ.Shape != nil && typ.Shape.Type.Name() == "shop.Code" {
				n, ok := typ.Methods[0].Function.MethodImport()
				if !ok {
					t.Fatal("shop.Code names an import that is not a method import")
				}
				return i, n
			}
		}
		t.Fatal("the module does not list shop.Code")
		return 0, 0
	}
	for name, change := range map[string]struct {
		change func(m *evm.Module)
		want   string
	}{
		"unchanged": {func(m *evm.Module) {}, ""},
		"reference past the method imports": {func(m *evm.Module) {
			i, _ := table(m)
			m.Types[i].Methods[0].Function = evm.MethodImportRef(len(m.MethodImports))
		}, "invalid dynamic method target"},
		"reference far past the method imports": {func(m *evm.Module) {
			i, _ := table(m)
			m.Types[i].Methods[0].Function = evm.FunctionRef{Native: true, Index: 1<<32 - 2}
		}, "invalid dynamic method target"},
		"native function value as a method": {func(m *evm.Module) {
			i, _ := table(m)
			m.Types[i].Methods[0].Function = evm.FunctionRef{Native: true, Index: 1<<32 - 1}
		}, "invalid dynamic method target"},
		"import of another receiver": {func(m *evm.Module) {
			i, n := table(m)
			for other := range m.MethodImports {
				if m.MethodImports[other].Receiver != m.MethodImports[n].Receiver {
					m.Types[i].Methods[0].Function = evm.MethodImportRef(other)
					return
				}
			}
			t.Fatal("no other receiver")
		}, "invalid dynamic method target"},
		"unknown method": {func(m *evm.Module) {
			_, n := table(m)
			m.MethodImports[n].Name = "Missing"
		}, "unresolved import shop.Missing"},
		"method of another signature": {func(m *evm.Module) {
			_, n := table(m)
			m.MethodImports[n].Signature.Results = []*values.Type{values.IntType}
		}, "signature mismatch for shop."},
		"method without a receiver": {func(m *evm.Module) {
			_, n := table(m)
			m.MethodImports[n].Receiver = nil
		}, "unresolved import shop."},
		"method with type arguments": {func(m *evm.Module) {
			_, n := table(m)
			m.MethodImports[n].TypeArgs = []*values.Type{values.IntType}
		}, "unexpected type arguments for shop."},
		"method with ownership effects": {func(m *evm.Module) {
			_, n := table(m)
			m.MethodImports[n].Ownership = &evm.OwnershipEffects{NoRetain: true}
		}, "ownership effects mismatch for shop."},
		"method import as a function value": {func(m *evm.Module) {
			_, n := table(m)
			m.FunctionRefs = append(slices.Clone(m.FunctionRefs), evm.MethodImportRef(n))
		}, "invalid function reference"},
		"deferred method import": {func(m *evm.Module) {
			_, n := table(m)
			m.Defers = append(slices.Clone(m.Defers), evm.DeferOp{Kind: evm.DeferDirect, Target: evm.MethodImportRef(n)})
		}, "invalid deferred target"},
		"duplicate method": {func(m *evm.Module) {
			i, _ := table(m)
			m.Types[i].Methods = append(m.Types[i].Methods, m.Types[i].Methods[0])
		}, "duplicate method identity"},
		"unnamed method": {func(m *evm.Module) {
			i, _ := table(m)
			m.Types[i].Methods[0].ID = ""
		}, "empty method identity"},
	} {
		for _, compile := range []string{"package list", "environment"} {
			t.Run(name+"/"+compile, func(t *testing.T) {
				var m *evm.Module
				if compile == "environment" {
					m, err = compiler.Compile("methods.go", linkTestSources["methods"], evmcompiler.Options{})
				} else {
					m, err = evmcompiler.Compile("methods.go", linkTestSources["methods"], packages...)
				}
				if err != nil {
					t.Fatal(err)
				}
				// The module shares its method tables with its compile
				// environment; change a copy.
				m.MethodImports = slices.Clone(m.MethodImports)
				for i := range m.MethodImports {
					m.MethodImports[i].Signature = evm.Signature{Params: slices.Clone(m.MethodImports[i].Signature.Params), Results: slices.Clone(m.MethodImports[i].Signature.Results)}
				}
				m.Types = slices.Clone(m.Types)
				for i := range m.Types {
					m.Types[i].Methods = slices.Clone(m.Types[i].Methods)
				}
				change.change(m)
				listing := m.Disassemble()

				reference, wantErr := evm.Link(m, packages...)
				if (wantErr == nil) != (change.want == "") || wantErr != nil && !strings.Contains(wantErr.Error(), change.want) {
					t.Fatalf("package list: error %v, want %q", wantErr, change.want)
				}
				own, err := evm.LinkOptions{Methods: []evm.MethodTables{{Imports: m.MethodImports, Types: m.Types}}}.NewEnvironment(packages...)
				if err != nil {
					t.Fatalf("environment with the module's method tables: %v", err)
				}
				for envName, env := range map[string]*evm.LinkEnvironment{"plain": plain, "prepared": prepared, "own": own} {
					for round := 0; round < 2; round++ {
						p, err := env.Link(m)
						if fmt.Sprint(err) != fmt.Sprint(wantErr) {
							t.Fatalf("%s: error %v, package list error %v", envName, err, wantErr)
						}
						if err != nil {
							continue
						}
						if a, b := evm.ModuleProgramListing(m, reference), evm.ModuleProgramListing(m, p); a != b {
							t.Fatalf("%s: programs differ\npackage list:\n%s\nenvironment:\n%s", envName, a, b)
						}
						if a, b := linkTestRun(reference), linkTestRun(p); a != b || strings.HasPrefix(a, "error") {
							t.Fatalf("%s: package list result %q, environment result %q", envName, a, b)
						}
						shared, _, natives := env.SharedMethods(p)
						if (shared != 0 || natives) != (envName == "own") {
							t.Fatalf("%s: shares %d method tables, natives %v", envName, shared, natives)
						}
					}
				}
				if m.Disassemble() != listing {
					t.Fatal("linking changed the module")
				}
			})
		}
	}
	if prepared.Fingerprint() != fingerprint {
		t.Error("linking wrote to data the programs share")
	}
}

// Modules of one compile environment share its method imports and the method
// tables of host types; a module compiled from the package list owns its.
func TestModulesShareMethodTables(t *testing.T) {
	packages := linkTestPackages(t)
	compiler, err := evmcompiler.NewEnvironment(packages...)
	if err != nil {
		t.Fatal(err)
	}
	tables := compiler.MethodTables()
	if len(tables.Imports) == 0 || len(tables.Types) == 0 {
		t.Fatalf("the environment shares %d method imports and %d method tables", len(tables.Imports), len(tables.Types))
	}
	shared := make(map[*values.Shape][]evm.DynamicMethod)
	for _, typ := range tables.Types {
		shared[typ.Shape] = typ.Methods
	}
	a, err := compiler.Compile("methods.go", linkTestSources["methods"], evmcompiler.Options{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := compiler.Compile("methods.go", strings.Replace(linkTestSources["methods"], `"local"`, `"other"`, 1), evmcompiler.Options{})
	if err != nil {
		t.Fatal(err)
	}
	lazy, err := evmcompiler.Compile("methods.go", linkTestSources["methods"], packages...)
	if err != nil {
		t.Fatal(err)
	}
	if &a.MethodImports[0] != &tables.Imports[0] || &b.MethodImports[0] != &tables.Imports[0] || len(a.MethodImports) != len(tables.Imports) {
		t.Fatal("modules of one environment do not share its method imports")
	}
	if len(lazy.MethodImports) == 0 || len(lazy.MethodImports) >= len(a.MethodImports) {
		t.Fatalf("a package-list module lists %d method imports, the environment %d", len(lazy.MethodImports), len(a.MethodImports))
	}
	sharedTables := 0
	for i, typ := range a.Types {
		if len(typ.Methods) == 0 {
			continue
		}
		other := b.Types[i]
		if table := shared[typ.Shape]; table != nil && &table[0] == &typ.Methods[0] {
			sharedTables++
			if &other.Methods[0] != &typ.Methods[0] {
				t.Errorf("%s: two modules have different method tables", typ.Shape.Type.Name())
			}
			for _, method := range typ.Methods {
				if _, ok := method.Function.MethodImport(); !ok {
					t.Errorf("%s.%s: a shared method table names the module's own import or function", typ.Shape.Type.Name(), method.ID)
				}
			}
		}
	}
	// shop.Code, *shop.Item and time.Duration have native methods alone.
	if sharedTables < 2 {
		t.Fatalf("the modules share %d method tables", sharedTables)
	}
	// Bytecode never indexes the method imports, and no import is listed
	// only for a method table.
	if len(a.Imports) != len(lazy.Imports) {
		t.Fatalf("imports: %d through the environment, %d from the package list", len(a.Imports), len(lazy.Imports))
	}
}

// Modules of one compile environment select the shapes of the host types
// they reach from the environment's list. A program knows the same shapes
// whichever way its module lists them, and a wrong selection fails the link.
func TestHostShapeSelection(t *testing.T) {
	packages := linkTestPackages(t)
	compiler, err := evmcompiler.NewEnvironment(packages...)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := compiler.NewLinkEnvironment(packages...)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := evm.NewLinkEnvironment(packages...)
	if err != nil {
		t.Fatal(err)
	}
	a, err := compiler.Compile("methods.go", linkTestSources["methods"], evmcompiler.Options{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := compiler.Compile("host.go", linkTestSources["host"], evmcompiler.Options{})
	if err != nil {
		t.Fatal(err)
	}
	lazy, err := evmcompiler.Compile("methods.go", linkTestSources["methods"], packages...)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.HostShapes.Table) == 0 || &a.HostShapes.Table[0] != &b.HostShapes.Table[0] {
		t.Fatal("modules of one environment do not share its list of shapes")
	}
	if len(lazy.HostShapes.Table) != 0 || len(lazy.HostShapes.Set) != 0 {
		t.Fatal("a package-list module selects shapes from a list")
	}
	selected := 0
	listed := make(map[*values.Type]bool)
	for s := range a.ListedShapes() {
		listed[s.Type] = true
		selected++
	}
	if selected -= len(a.Shapes); selected <= 0 || len(a.Shapes) >= len(lazy.Shapes) {
		t.Fatalf("the module selects %d shapes and lists %d itself; the package-list module lists %d", selected, len(a.Shapes), len(lazy.Shapes))
	}
	for _, s := range lazy.Shapes {
		// Shapes of derived types have a new identity in every compile.
		if s.Type.Name() == "shop.Code" && !listed[s.Type] {
			t.Fatalf("the module does not list a shape of %s", s.Type.Name())
		}
	}

	change := func(change func(m *evm.Module)) *evm.Module {
		m := *a
		m.HostShapes.Set = slices.Clone(m.HostShapes.Set)
		change(&m)
		return &m
	}
	for name, c := range map[string]struct {
		module *evm.Module
		want   string
	}{
		"unchanged":         {change(func(m *evm.Module) {}), ""},
		"selection first":   {change(func(m *evm.Module) { m.HostShapes.After = -3 }), ""},
		"selection last":    {change(func(m *evm.Module) { m.HostShapes.After = 1 << 40 }), ""},
		"bit past the list": {change(func(m *evm.Module) { m.HostShapes.Set = append(m.HostShapes.Set, 1) }), "invalid host shape selection"},
		"bit past a short list": {change(func(m *evm.Module) {
			m.HostShapes.Table = m.HostShapes.Table[:len(m.HostShapes.Table)/2]
			m.HostShapes.Set[len(m.HostShapes.Set)-1] |= 1
		}), "invalid host shape selection"},
		"nil shape": {change(func(m *evm.Module) {
			m.HostShapes.Table = append([]*values.Shape{nil}, m.HostShapes.Table[1:]...)
			m.HostShapes.Set[0] |= 1
		}), "nil storage shape"},
	} {
		t.Run(name, func(t *testing.T) {
			m := c.module
			reference, wantErr := evm.Link(m, packages...)
			if (wantErr == nil) != (c.want == "") || wantErr != nil && !strings.Contains(wantErr.Error(), c.want) {
				t.Fatalf("package list: error %v, want %q", wantErr, c.want)
			}
			for envName, env := range map[string]*evm.LinkEnvironment{"plain": plain, "prepared": prepared} {
				p, err := env.Link(m)
				if fmt.Sprint(err) != fmt.Sprint(wantErr) {
					t.Fatalf("%s: error %v, package list error %v", envName, err, wantErr)
				}
				if err != nil {
					continue
				}
				if a, b := evm.ModuleProgramListing(m, reference), evm.ModuleProgramListing(m, p); a != b {
					t.Fatalf("%s: programs differ\npackage list:\n%s\nenvironment:\n%s", envName, a, b)
				}
				if a, b := linkTestRun(reference), linkTestRun(p); a != b || strings.HasPrefix(a, "error") {
					t.Fatalf("%s: package list result %q, environment result %q", envName, a, b)
				}
				// The program knows a selected shape by type.
				for s := range m.ListedShapes() {
					if p.Shape(s.Type) == nil {
						t.Fatalf("%s: the program does not know the shape of %s", envName, s.Type.Name())
					}
				}
			}
		})
	}
}
