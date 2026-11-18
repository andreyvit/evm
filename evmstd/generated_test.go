package evmstd_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The checked-in declarations, bindings and API report match what the
// generator produces from this toolchain's standard library.
func TestGeneratedFilesAreCurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the generator")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, root)
	cmd := exec.Command("go", "run", "./internal/genstd")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generator failed: %v\n%s", err, out)
	}
	after := snapshot(t, root)
	for name, text := range after {
		if before[name] != text {
			if err := os.WriteFile(filepath.Join(root, name), []byte(before[name]), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Errorf("%s is stale: run go generate ./evmstd", name)
		}
	}
}

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root, "evmstd/_decl/*/decl.go"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, filepath.Join(root, "evmstd/bindings_gen.go"), filepath.Join(root, "evmstd/API.md"))
	out := map[string]string{}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		rel, err := filepath.Rel(root, file)
		if err != nil {
			t.Fatal(err)
		}
		out[rel] = string(data)
	}
	return out
}
