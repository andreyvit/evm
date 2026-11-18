// Command gox checks syntax and formats GoX source files.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/andreyvit/evm/gox"
)

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(stdout, "Usage: gox check [-filename stdin.gox] [files or directories...]\n       gox fmt [-w] [-l] [-filename stdin.gox] [files or directories...]\n\nWith no paths, read stdin. Directories are searched recursively for .go and .gox files.\ncheck validates syntax only; host packages and types are checked during compilation.\nfmt adds missing standard library and GoX runtime imports; it does not know host packages.")
		return 0
	}
	command := args[0]
	if command != "fmt" && command != "check" {
		fmt.Fprintf(stderr, "gox: unknown command %q\n", command)
		return 2
	}
	flags := flag.NewFlagSet("gox "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	filename := flags.String("filename", "stdin.gox", "source filename for stdin and import resolution")
	var write, list bool
	if command == "fmt" {
		flags.BoolVar(&write, "w", false, "write formatted files in place")
		flags.BoolVar(&list, "l", false, "list files whose formatting differs")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if write && flags.NArg() == 0 {
		fmt.Fprintln(stderr, "gox: -w requires a file path")
		return 2
	}
	failed := false
	report := func(err error) { fmt.Fprintln(stderr, err); failed = true }
	process := func(name string, data []byte) {
		if command == "check" {
			if err := gox.CheckSyntax(name, string(data)); err != nil {
				report(err)
			}
			return
		}
		formatted, err := gox.FormatSource(name, string(data))
		if err != nil {
			report(err)
			return
		}
		if formatted != string(data) {
			if list {
				if _, err := fmt.Fprintln(stdout, name); err != nil {
					report(err)
				}
			}
			if write {
				if err := writeFile(name, []byte(formatted)); err != nil {
					report(err)
				}
			}
		}
		if !write && !list {
			if _, err := io.WriteString(stdout, formatted); err != nil {
				report(err)
			}
		}
	}
	if flags.NArg() == 0 {
		data, err := io.ReadAll(stdin)
		if err != nil {
			report(err)
		} else {
			process(*filename, data)
		}
	} else {
		names := map[string]bool{}
		for _, name := range flags.Args() {
			info, err := os.Stat(name)
			if err != nil {
				report(err)
				continue
			}
			if !info.IsDir() {
				names[name] = true
				continue
			}
			err = filepath.WalkDir(name, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					if p != name && (strings.HasPrefix(d.Name(), ".") || d.Name() == "vendor") {
						return filepath.SkipDir
					}
					return nil
				}
				if !d.Type().IsRegular() {
					return nil
				}
				if ext := filepath.Ext(p); ext == ".go" || ext == ".gox" {
					names[p] = true
				}
				return nil
			})
			if err != nil {
				report(err)
			}
		}
		paths := make([]string, 0, len(names))
		for name := range names {
			paths = append(paths, name)
		}
		slices.Sort(paths)
		for _, name := range paths {
			data, err := os.ReadFile(name)
			if err != nil {
				report(err)
				continue
			}
			process(name, data)
		}
	}
	if failed {
		return 1
	}
	return 0
}

func writeFile(name string, data []byte) error {
	target, err := filepath.EvalSymlinks(name)
	if err != nil {
		return err
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(target), ".gox-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(info.Mode().Perm()); err == nil {
		_, err = f.Write(data)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), target)
}
