// Command lintimports checks layer import rules of the vending-machine repo.
//
// Layers are derived from the file location relative to the repository root
// (the directory containing go.work):
//
//	core/domain      -> stdlib only
//	core/ports       -> stdlib + .../core/domain
//	core/internal/gate -> stdlib + .../core/domain only
//	core/internal/app -> stdlib + .../core/domain + .../core/ports
//	                    + .../core/internal/gate + .../core/internal/resilience.
//	                    Store, cli, journal, config, contracts and providers
//	                    are forbidden there.
//	core/internal/*  -> anything except .../providers/* (any provider).
//	                    Only core/cmd (the composition root) wires providers.
//	providers/<name> -> stdlib + .../core/domain + .../core/ports
//	                    + .../core/porttest (only in _test.go)
//	                    + own provider module packages.
//	                    core/internal/* and other providers/* are forbidden.
//
// All other locations (tools, core/cmd, core/contracts, ...)
// are not restricted by this tool.
package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const modulePrefix = "github.com/LevapVeeskela/lv24-vending-machine"

// isStdlib reports whether importPath refers to the standard library.
// A stdlib import path has no dot in its first segment.
func isStdlib(importPath string) bool {
	seg := importPath
	if i := strings.IndexByte(seg, '/'); i >= 0 {
		seg = seg[:i]
	}
	return !strings.Contains(seg, ".")
}

// classify reports whether an import is allowed in a layer.
// layer is one of "core/domain", "core/ports", "core/internal",
// "providers/<name>" or "other".
func classify(layer, importPath string, isTest bool) bool {
	if isStdlib(importPath) {
		return true
	}
	inModule := func(suffix string) bool {
		return importPath == modulePrefix+suffix ||
			strings.HasPrefix(importPath, modulePrefix+suffix+"/")
	}
	switch {
	case layer == "core/domain":
		return inModule("/core/domain")
	case layer == "core/ports":
		return inModule("/core/domain") || inModule("/core/ports")
	case layer == "core/internal/gate":
		return inModule("/core/domain")
	case layer == "core/internal/app":
		return inModule("/core/domain") || inModule("/core/ports") ||
			inModule("/core/internal/gate") || inModule("/core/internal/resilience")
	case layer == "core/internal":
		// Only the cmd composition root wires providers.
		return !strings.HasPrefix(importPath, modulePrefix+"/providers/")
	case strings.HasPrefix(layer, "providers/"):
		name := strings.TrimPrefix(layer, "providers/")
		if inModule("/core/domain") || inModule("/core/ports") {
			return true
		}
		if inModule("/core/porttest") {
			return isTest
		}
		if name != "" && (importPath == modulePrefix+"/providers/"+name ||
			strings.HasPrefix(importPath, modulePrefix+"/providers/"+name+"/")) {
			return true
		}
		return false
	default:
		return true
	}
}

// layerOf maps a repo-relative slash path to a layer name.
func layerOf(relSlash string) string {
	switch {
	case strings.HasPrefix(relSlash, "core/domain/"):
		return "core/domain"
	case strings.HasPrefix(relSlash, "core/ports/"):
		return "core/ports"
	case strings.HasPrefix(relSlash, "core/internal/gate/"):
		return "core/internal/gate"
	case strings.HasPrefix(relSlash, "core/internal/app/"):
		return "core/internal/app"
	case strings.HasPrefix(relSlash, "core/internal/"):
		return "core/internal"
	case strings.HasPrefix(relSlash, "providers/"):
		rest := strings.TrimPrefix(relSlash, "providers/")
		name := rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			name = rest[:i]
		}
		return "providers/" + name
	default:
		return "other"
	}
}

// findRoot walks up from dir until a directory containing go.work is found.
func findRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(abs, "go.work")); err == nil {
			return abs, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", fmt.Errorf("go.work not found above %s", dir)
		}
		abs = parent
	}
}

func run(root string) int {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "lintimports: walk: %v\n", err)
		return 1
	}
	sort.Strings(files)

	fset := token.NewFileSet()
	violations := 0
	for _, path := range files {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		relSlash := filepath.ToSlash(rel)
		layer := layerOf(relSlash)
		isTest := strings.HasSuffix(filepath.Base(path), "_test.go")
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: cannot parse: %v\n", relSlash, err)
			violations++
			continue
		}
		for _, imp := range f.Imports {
			impPath, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			if !classify(layer, impPath, isTest) {
				pos := fset.Position(imp.Pos())
				fmt.Fprintf(os.Stderr, "%s:%d: import %q is not allowed in %s\n",
					relSlash, pos.Line, impPath, layer)
				violations++
			}
		}
	}
	if violations > 0 {
		return 1
	}
	return 0
}

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "lintimports: %v\n", err)
		os.Exit(1)
	}
	root, err := findRoot(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lintimports: %v\n", err)
		os.Exit(1)
	}
	os.Exit(run(root))
}
