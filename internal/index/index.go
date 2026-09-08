package index

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stephen/advisor/internal/parser"
	"github.com/stephen/advisor/internal/store"
)

// Indexer walks a project tree, parses supported files, and maintains the
// content-addressed index.
type Indexer struct {
	store *store.Store
	root  string
}

// New creates an Indexer rooted at root.
func New(root string) *Indexer {
	return &Indexer{
		store: store.New(filepath.Join(root, ".advisor")),
		root:  root,
	}
}

// Store exposes the underlying store.
func (ix *Indexer) Store() *store.Store { return ix.store }

// Init builds the index from scratch, parsing every supported file.
func (ix *Indexer) Init() (*store.Index, error) {
	idx, err := ix.store.LoadIndex()
	if err != nil {
		return nil, err
	}
	// Reset derived state; notes are preserved (they are human-authored).
	idx.Files = map[string]*store.FileEntry{}
	idx.Dirs = map[string]*store.DirEntry{}

	var files []string
	if err := filepath.Walk(ix.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if path != ix.root && shouldSkipDir(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if _, ok := parser.DetectLanguage(path); ok {
			rel, err := filepath.Rel(ix.root, path)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Strings(files)

	for _, path := range files {
		if err := ix.indexFile(idx, path); err != nil {
			return nil, err
		}
	}
	if err := ix.recomputeDirs(idx); err != nil {
		return nil, err
	}
	if err := ix.store.SaveIndex(idx); err != nil {
		return nil, err
	}
	return idx, nil
}

// ReindexFile re-parses a single file and updates the index. Used by the
// watcher and by lazy re-parse at query time.
func (ix *Indexer) ReindexFile(idx *store.Index, path string) error {
	rel, err := filepath.Rel(ix.root, path)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(rel)
	if err := ix.indexFile(idx, rel); err != nil {
		return err
	}
	// Recompute fingerprints for the file's directory and all ancestors.
	dir := filepath.Dir(rel)
	for {
		if err := ix.recomputeDir(idx, dir); err != nil {
			return err
		}
		if dir == "." || dir == "/" {
			break
		}
		dir = filepath.Dir(dir)
	}
	return ix.store.SaveIndex(idx)
}

// RemoveFile drops a file from the index (deleted on disk).
func (ix *Indexer) RemoveFile(idx *store.Index, path string) error {
	rel, err := filepath.Rel(ix.root, path)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(rel)
	delete(idx.Files, rel)
	dir := filepath.Dir(rel)
	for {
		if err := ix.recomputeDir(idx, dir); err != nil {
			return err
		}
		if dir == "." || dir == "/" {
			break
		}
		dir = filepath.Dir(dir)
	}
	return ix.store.SaveIndex(idx)
}

// Locate finds a symbol by name across the index, returning the file entry
// and the symbol. It lazily re-parses stale files and checks their fresh
// symbols, so new symbols in changed files are found.
func (ix *Indexer) Locate(idx *store.Index, name string) (*store.FileEntry, *parser.Symbol, error) {
	var exact []*store.FileEntry
	var fuzzy []*store.FileEntry
	seen := map[string]bool{}

	for _, f := range idx.Files {
		var fd parser.FileData
		if err := ix.store.GetFile(f.IndexRef, &fd); err != nil {
			return nil, nil, err
		}
		for i := range fd.Symbols {
			if fd.Symbols[i].Name == name {
				if !seen[f.Path] {
					seen[f.Path] = true
					exact = append(exact, f)
				}
			} else if strings.Contains(strings.ToLower(fd.Symbols[i].Name), strings.ToLower(name)) {
				if !seen[f.Path] {
					seen[f.Path] = true
					fuzzy = append(fuzzy, f)
				}
			}
		}
	}
	sort.Slice(exact, func(i, j int) bool { return exact[i].Path < exact[j].Path })
	sort.Slice(fuzzy, func(i, j int) bool { return fuzzy[i].Path < fuzzy[j].Path })

	matches := append(exact, fuzzy...)
	for _, f := range matches {
		// Lazy freshness check: hash the file, re-parse if stale.
		abs := filepath.Join(ix.root, filepath.FromSlash(f.Path))
		cur, err := store.HashFile(abs)
		if err != nil {
			continue // file gone; skip
		}
		if cur != f.FileHash {
			if err := ix.ReindexFile(idx, abs); err != nil {
				return nil, nil, err
			}
			f = idx.Files[f.Path]
			if f == nil {
				continue
			}
		}
		var fd parser.FileData
		if err := ix.store.GetFile(f.IndexRef, &fd); err != nil {
			return nil, nil, err
		}
		for i := range fd.Symbols {
			if fd.Symbols[i].Name == name {
				return f, &fd.Symbols[i], nil
			}
		}
	}

	// New symbols: a changed file may now define the symbol even though the
	// old index didn't list it. Re-parse stale files and check fresh symbols.
	for _, f := range idx.Files {
		if seen[f.Path] {
			continue
		}
		abs := filepath.Join(ix.root, filepath.FromSlash(f.Path))
		cur, err := store.HashFile(abs)
		if err != nil {
			continue
		}
		if cur == f.FileHash {
			continue
		}
		if err := ix.ReindexFile(idx, abs); err != nil {
			return nil, nil, err
		}
		f = idx.Files[f.Path]
		if f == nil {
			continue
		}
		var fd parser.FileData
		if err := ix.store.GetFile(f.IndexRef, &fd); err != nil {
			return nil, nil, err
		}
		for i := range fd.Symbols {
			if fd.Symbols[i].Name == name {
				return f, &fd.Symbols[i], nil
			}
		}
	}
	return nil, nil, nil
}

// indexFile parses one file and writes its derived artifact.
func (ix *Indexer) indexFile(idx *store.Index, rel string) error {
	abs := filepath.Join(ix.root, filepath.FromSlash(rel))
	content, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	lang, ok := parser.DetectLanguage(rel)
	if !ok {
		return nil
	}
	fd, err := parser.Parse(lang, rel, content)
	if err != nil {
		return err
	}
	fd.Hash = store.Hash(content)
	hash := fd.Hash
	if err := ix.store.PutFile(hash, fd); err != nil {
		return err
	}
	idx.Files[rel] = &store.FileEntry{
		Path:     rel,
		FileHash: hash,
		IndexRef: hash,
	}
	return nil
}

// recomputeDirs recomputes fingerprints for all directories in the index.
func (ix *Indexer) recomputeDirs(idx *store.Index) error {
	dirs := map[string][]string{}
	for path, f := range idx.Files {
		dir := filepath.Dir(path)
		dirs[dir] = append(dirs[dir], f.FileHash)
	}
	for dir, hashes := range dirs {
		idx.Dirs[dir] = &store.DirEntry{
			Fingerprint: store.DirFingerprint(hashes),
		}
	}
	return nil
}

// recomputeDir recomputes the fingerprint for a single directory.
func (ix *Indexer) recomputeDir(idx *store.Index, dir string) error {
	var hashes []string
	for path, f := range idx.Files {
		if filepath.Dir(path) == dir {
			hashes = append(hashes, f.FileHash)
		}
	}
	if len(hashes) == 0 {
		delete(idx.Dirs, dir)
		return nil
	}
	idx.Dirs[dir] = &store.DirEntry{
		Fingerprint: store.DirFingerprint(hashes),
	}
	return nil
}

func shouldSkipDir(path string) bool {
	base := filepath.Base(path)
	switch base {
	case ".git", ".advisor", "node_modules", "vendor", "dist", "build", ".venv", "venv", "__pycache__":
		return true
	}
	return false
}

// DirFingerprintFor returns the current fingerprint for a directory, or "".
func (ix *Indexer) DirFingerprintFor(idx *store.Index, dir string) string {
	if d, ok := idx.Dirs[dir]; ok {
		return d.Fingerprint
	}
	return ""
}

// FileEntryFor returns the index entry for a path, or nil.
func (ix *Indexer) FileEntryFor(idx *store.Index, path string) *store.FileEntry {
	return idx.Files[path]
}

// Summary returns a compact per-file summary for a directory.
func (ix *Indexer) Summary(idx *store.Index, dir string) ([]string, error) {
	var paths []string
	for p := range idx.Files {
		if filepath.Dir(p) == dir {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	var out []string
	for _, p := range paths {
		f := idx.Files[p]
		var fd parser.FileData
		if err := ix.store.GetFile(f.IndexRef, &fd); err != nil {
			return nil, err
		}
		line := fmt.Sprintf("%s: %s", p, fd.Summary)
		out = append(out, line)
	}
	return out, nil
}
