package evmcompiler_test

import (
	"runtime"
	"testing"

	"github.com/andreyvit/evm/evmcompiler"
)

func TestCompiledModuleReleasesCompilerState(t *testing.T) {
	const source = `import ("fmt"; "strings")
func Run() int { return len(strings.Repeat(fmt.Sprint(1), 2)) }`
	if _, err := evmcompiler.Compile("main.go", source); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	m, err := evmcompiler.Compile("main.go", source)
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if retained := int64(after.HeapAlloc) - int64(before.HeapAlloc); retained > 512<<10 {
		t.Fatalf("module retains %d KB", retained>>10)
	}
	runtime.KeepAlive(m)
}
