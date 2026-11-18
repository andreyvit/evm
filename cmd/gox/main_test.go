package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLI(t *testing.T) {
	const source = `package plugin
func Page(b *gox.Builder) {<p>name</p>}`
	invoke := func(args []string, input string) (int, string, string) {
		t.Helper()
		var out, err bytes.Buffer
		code := run(args, strings.NewReader(input), &out, &err)
		return code, out.String(), err.String()
	}
	if code, _, err := invoke([]string{"check"}, source); code != 0 || err != "" {
		t.Fatalf("%d %s", code, err)
	}
	if code, _, err := invoke([]string{"check"}, `package plugin
func Page(){<p>}`); code != 1 || !strings.Contains(err, "unclosed") {
		t.Fatalf("%d %s", code, err)
	}
	code, formatted, err := invoke([]string{"fmt"}, source)
	if code != 0 || err != "" || !strings.Contains(formatted, "\t<p>name</p>") {
		t.Fatalf("%d %s %s", code, formatted, err)
	}
	// fmt is the supported fix for a missing import of a package it knows.
	if !strings.Contains(formatted, "import \"github.com/andreyvit/evm/gox\"\n") {
		t.Fatalf("missing runtime import:\n%s", formatted)
	}
	if code, _, _ := invoke([]string{"fmt", "-w"}, source); code != 2 {
		t.Fatal(code)
	}
	dir := t.TempDir()
	name := filepath.Join(dir, "page.gox")
	if err := os.WriteFile(name, []byte(source), 0640); err != nil {
		t.Fatal(err)
	}
	if code, out, err := invoke([]string{"fmt", "-l", dir}, ""); code != 0 || out != name+"\n" || err != "" {
		t.Fatalf("%d %q %s", code, out, err)
	}
	data, e := os.ReadFile(name)
	if e != nil || string(data) != source {
		t.Fatalf("%s %v", data, e)
	}
	if code, out, err := invoke([]string{"fmt", "-w", "-l", dir}, ""); code != 0 || out != name+"\n" || err != "" {
		t.Fatalf("%d %q %s", code, out, err)
	}
	data, e = os.ReadFile(name)
	if e != nil || string(data) != formatted {
		t.Fatalf("%s %v", data, e)
	}
	info, e := os.Stat(name)
	if e != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("%v %v", info, e)
	}
	if code, out, err := invoke([]string{"fmt", "-l", dir}, ""); code != 0 || out != "" || err != "" {
		t.Fatalf("%d %q %s", code, out, err)
	}
	if e := os.WriteFile(name, []byte(`package plugin
func Page(){<p>}`), 0640); e != nil {
		t.Fatal(e)
	}
	if code, _, _ := invoke([]string{"fmt", "-w", name}, ""); code != 1 {
		t.Fatal(code)
	}
	data, e = os.ReadFile(name)
	if e != nil || string(data) != `package plugin
func Page(){<p>}` {
		t.Fatalf("%s %v", data, e)
	}
}
