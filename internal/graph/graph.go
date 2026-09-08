package graph

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/stephen/advisor/internal/parser"
	"github.com/stephen/advisor/internal/store"
)

// Graph resolves cross-package references to source files and answers
// caller/import queries. It is a query-time layer over the content-addressed
// index — no separate store.
type Graph struct {
	root  string
	store *store.Store
}

// New builds a graph rooted at the project.
func New(root string) *Graph {
	return &Graph{
		root:  root,
		store: store.New(filepath.Join(root, ".advisor")),
	}
}

// fileData loads a file's parsed data by index entry.
func (g *Graph) fileData(idx *store.Index, path string) (*parser.FileData, bool) {
	ent, ok := idx.Files[path]
	if !ok {
		return nil, false
	}
	var fd parser.FileData
	if err := g.store.GetFile(ent.IndexRef, &fd); err != nil {
		return nil, false
	}
	return &fd, true
}

// resolvePkg maps a package/module reference to candidate source file paths
// in the project. Go module paths (github.com/x/y/z) map to the last segment
// matching a package dir; relative JS/TS paths map directly.
func (g *Graph) resolvePkg(idx *store.Index, pkg string) []string {
	var out []string
	pkg = strings.TrimPrefix(pkg, "./")
	pkg = strings.TrimSuffix(pkg, "/")
	// Try exact dir matching first (pkg == dir path).
	for path := range idx.Files {
		dir := filepath.Dir(path)
		if dir == pkg {
			out = append(out, path)
		}
	}
	if len(out) > 0 {
		sort.Strings(out)
		return out
	}
	// Go: last segment of the module path matches a package dir.
	last := pkg
	if i := strings.LastIndex(pkg, "/"); i >= 0 {
		last = pkg[i+1:]
	}
	for path := range idx.Files {
		dir := filepath.Dir(path)
		if dir == last {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}

// IsTestFile reports whether a path is a test file.
func IsTestFile(path string) bool {
	base := filepath.Base(path)
	ext := filepath.Ext(path)
	name := strings.TrimSuffix(base, ext)
	if strings.HasSuffix(name, "_test") || strings.HasSuffix(name, "_spec") || strings.HasSuffix(name, ".test") {
		return true
	}
	if strings.HasSuffix(base, ".test.ts") || strings.HasSuffix(base, ".test.tsx") || strings.HasSuffix(base, ".test.js") {
		return true
	}
	return false
}

// pkgOf extracts the package name from a file path. For Go, it's the
// directory; for others, the module-relative dir.
func (g *Graph) pkgOf(path string) string {
	return filepath.Dir(path)
}

// CallersOfSymbol returns files that reference a symbol defined in any file
// under dir. It matches refs whose Pkg resolves to dir and Sym equals name.
func (g *Graph) CallersOfSymbol(idx *store.Index, name string) ([]Caller, error) {
	var callers []Caller
	type symLoc struct {
		path string
		sym  parser.Symbol
	}
	// Find all definitions of the name.
	var defs []symLoc
	for path, ent := range idx.Files {
		var fd parser.FileData
		if err := g.store.GetFile(ent.IndexRef, &fd); err != nil {
			continue
		}
		for _, s := range fd.Symbols {
			if s.Name == name {
				defs = append(defs, symLoc{path, s})
			}
		}
	}
	// For each def, find files whose pkg resolves there and reference it.
	for _, d := range defs {
		defPkg := g.pkgOf(d.path)
		for path, ent := range idx.Files {
			if path == d.path {
				continue
			}
			var fd parser.FileData
			if err := g.store.GetFile(ent.IndexRef, &fd); err != nil {
				continue
			}
			for _, r := range fd.Refs {
				if r.Sym == name && r.Pkg != "" {
					if matched := g.resolvePkg(idx, r.Pkg); matchesAny(matched, defPkg) {
						callers = append(callers, Caller{File: path, Sym: name, Pkg: r.Pkg})
						break
					}
				}
			}
		}
	}
	sort.Slice(callers, func(i, j int) bool { return callers[i].File < callers[j].File })
	return dedupeCallers(callers), nil
}

// CallersOfFile returns files that import from the given file's package.
func (g *Graph) CallersOfFile(idx *store.Index, path string) ([]string, error) {
	pkg := g.pkgOf(path)
	var out []string
	for p := range idx.Files {
		if p == path {
			continue
		}
		var fd parser.FileData
		if err := g.store.GetFile(idx.Files[p].IndexRef, &fd); err != nil {
			continue
		}
		for _, imp := range fd.Imports {
			for _, candidate := range g.resolvePkg(idx, strings.TrimSpace(imp.Target)) {
				if filepath.Dir(candidate) == pkg {
					out = append(out, p)
					break
				}
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// AffectedTests returns test files that depend (transitively) on the given
// source files — used for test impact analysis after a change. A test is
// affected if it lives in an affected package or imports one.
func (g *Graph) AffectedTests(idx *store.Index, changed []string) ([]string, error) {
	pkgOf := func(path string) string { return filepath.Dir(path) }

	// Seed with the packages of the changed files.
	affectedPkgs := map[string]bool{}
	for _, c := range changed {
		affectedPkgs[pkgOf(c)] = true
	}

	// Fixpoint: a package is affected if it imports from an affected package.
	// Compute forward edges: importerPkg -> set of imported pkgs (in-repo).
	forward := map[string]map[string]bool{} // importerPkg -> imported pkgs
	for path, ent := range idx.Files {
		var fd parser.FileData
		if err := g.store.GetFile(ent.IndexRef, &fd); err != nil {
			continue
		}
		imp := pkgOf(path)
		if forward[imp] == nil {
			forward[imp] = map[string]bool{}
		}
		for _, i := range fd.Imports {
			for _, cand := range g.resolvePkg(idx, strings.TrimSpace(i.Target)) {
				target := pkgOf(cand)
				if target != imp && target != "." && target != "" {
					forward[imp][target] = true
				}
			}
		}
	}

	for changedFlag := true; changedFlag; {
		changedFlag = false
		for imp, deps := range forward {
			if affectedPkgs[imp] {
				continue
			}
			for dep := range deps {
				if affectedPkgs[dep] {
					affectedPkgs[imp] = true
					changedFlag = true
					break
				}
			}
		}
	}

	// Tests: those in affected packages, or in any package importing an
	// affected package. A test inside an affected package is directly affected.
	var tests []string
	for path := range idx.Files {
		if !IsTestFile(path) {
			continue
		}
		if affectedPkgs[pkgOf(path)] {
			tests = append(tests, path)
			continue
		}
		// Test in an unaffected package but importing an affected one.
		var fd parser.FileData
		if err := g.store.GetFile(idx.Files[path].IndexRef, &fd); err != nil {
			continue
		}
		for _, i := range fd.Imports {
			for _, cand := range g.resolvePkg(idx, strings.TrimSpace(i.Target)) {
				if affectedPkgs[pkgOf(cand)] {
					tests = append(tests, path)
					break
				}
			}
			if len(tests) > 0 && tests[len(tests)-1] == path {
				break
			}
		}
	}
	sort.Strings(tests)
	return tests, nil
}

// matchesAny reports whether any resolved path lives under dir.
func matchesAny(paths []string, dir string) bool {
	for _, p := range paths {
		if filepath.Dir(p) == dir {
			return true
		}
	}
	return false
}

func dedupeCallers(in []Caller) []Caller {
	seen := map[string]bool{}
	var out []Caller
	for _, c := range in {
		k := c.File + "|" + c.Sym + "|" + c.Pkg
		if !seen[k] {
			seen[k] = true
			out = append(out, c)
		}
	}
	return out
}

// Caller is a file that references a symbol along with the referenced symbol.
type Caller struct {
	File string `json:"file"`
	Sym  string `json:"sym"`
	Pkg  string `json:"pkg,omitempty"`
}
