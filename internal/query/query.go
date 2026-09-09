package query

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"astral/internal/graph"
	"astral/internal/index"
	"astral/internal/notes"
	"astral/internal/store"
)

// App wires the indexer and note manager for CLI commands.
type App struct {
	Indexer *index.Indexer
	Notes   *notes.Manager
	Graph   *graph.Graph
	Root    string
}

// New creates an App rooted at root.
func New(root string) *App {
	ix := index.New(root)
	return &App{
		Indexer: ix,
		Notes:   notes.New(ix.Store()),
		Graph:   graph.New(root),
		Root:    root,
	}
}

// Locate finds a symbol and returns the location plus the scoped note.
func (a *App) Locate(name string) (string, error) {
	idx, err := a.Indexer.Store().LoadIndex()
	if err != nil {
		return "", err
	}
	entry, sym, err := a.Indexer.Locate(idx, name)
	if err != nil {
		return "", err
	}
	if entry == nil || sym == nil {
		return fmt.Sprintf("not found: %s", name), nil
	}
	dir := filepath.Dir(entry.Path)
	note, err := a.scopedNote(idx, dir)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s:%d  %s %s\n", entry.Path, sym.Line, sym.Kind, sym.Name)
	if note != "" {
		b.WriteString(note)
	}
	return b.String(), nil
}

// Module prints per-file summaries plus the module note.
func (a *App) Module(dir string) (string, error) {
	idx, err := a.Indexer.Store().LoadIndex()
	if err != nil {
		return "", err
	}
	dir = filepath.ToSlash(strings.TrimPrefix(dir, "./"))
	lines, err := a.Indexer.Summary(idx, dir)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\n")
	}
	note, err := a.scopedNote(idx, dir)
	if err != nil {
		return "", err
	}
	if note != "" {
		b.WriteString(note)
	}
	return b.String(), nil
}

// NotePrompt emits the LLM-handoff prompt for a module.
func (a *App) NotePrompt(dir string) (string, error) {
	idx, err := a.Indexer.Store().LoadIndex()
	if err != nil {
		return "", err
	}
	dir = filepath.ToSlash(strings.TrimPrefix(dir, "./"))
	lines, err := a.Indexer.Summary(idx, dir)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("Write a conventions note for this module. Fill in the JSON below.\n\n")
	b.WriteString("Module structure:\n")
	for _, l := range lines {
		b.WriteString("  " + l + "\n")
	}
	b.WriteString(`
{
  "purpose": "one line: what this module does",
  "conventions": { "naming": "...", "errors": "...", "placement": "..." },
  "invariants": ["hard constraints the model must respect when writing here"]
}
`)
	return b.String(), nil
}

// NoteSet stores a note as draft.
func (a *App) NoteSet(dir, jsonStr string) error {
	n, err := parseNote(jsonStr)
	if err != nil {
		return err
	}
	return a.Notes.Set(dir, n)
}

// NoteApprove promotes a note to reviewed, stamping the current fingerprint.
func (a *App) NoteApprove(dir string) error {
	idx, err := a.Indexer.Store().LoadIndex()
	if err != nil {
		return err
	}
	fp := a.Indexer.DirFingerprintFor(idx, dir)
	n, err := a.Notes.Load(dir)
	if err != nil {
		return err
	}
	if n == nil {
		return fmt.Errorf("no note for %s", dir)
	}
	return a.Notes.Approve(dir, n, fp)
}

// Review lists all notes with evaluated state.
func (a *App) Review() (string, error) {
	idx, err := a.Indexer.Store().LoadIndex()
	if err != nil {
		return "", err
	}
	fps := map[string]string{}
	for dir := range idx.Dirs {
		fps[dir] = idx.Dirs[dir].Fingerprint
	}
	items, err := a.Notes.Review(fps)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "%s: %s\n", it.Dir, it.State)
		if it.State == notes.StateStale || it.State == notes.StateConflict {
			draft := notes.Draft(it.Dir, fps[it.Dir])
			b.WriteString("  " + notes.Diff(it.Note, draft) + "\n")
		}
	}
	return b.String(), nil
}

// Status prints freshness and coverage.
func (a *App) Status() (string, error) {
	idx, err := a.Indexer.Store().LoadIndex()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "files indexed: %d\n", len(idx.Files))
	fmt.Fprintf(&b, "directories: %d\n", len(idx.Dirs))
	dirs, _ := a.Indexer.Store().ListNotes()
	fmt.Fprintf(&b, "notes: %d\n", len(dirs))
	return b.String(), nil
}

// GC prunes orphaned derived artifacts.
func (a *App) GC() error {
	idx, err := a.Indexer.Store().LoadIndex()
	if err != nil {
		return err
	}
	return a.Indexer.Store().GC(idx)
}

// scopedNote returns the merged note chain for a directory, formatted, with
// staleness evaluated against the current directory fingerprints.
func (a *App) scopedNote(idx *store.Index, dir string) (string, error) {
	chain, err := a.Notes.Chain(dir)
	if err != nil {
		return "", err
	}
	if len(chain) == 0 {
		return "", nil
	}
	fps := map[string]string{}
	for d := range idx.Dirs {
		fps[d] = idx.Dirs[d].Fingerprint
	}
	merged := notes.Merge(chain, fps)
	var b strings.Builder
	if merged.Purpose != "" {
		fmt.Fprintf(&b, "purpose: %s\n", merged.Purpose)
	}
	if len(merged.Conventions) > 0 {
		b.WriteString("conventions:\n")
		for k, v := range merged.Conventions {
			fmt.Fprintf(&b, "  %s: %s\n", k, v)
		}
	}
	if len(merged.Invariants) > 0 {
		b.WriteString("invariants:\n")
		for _, inv := range merged.Invariants {
			fmt.Fprintf(&b, "  - %s\n", inv)
		}
	}
	fmt.Fprintf(&b, "state: %s\n", merged.State)
	return b.String(), nil
}

func parseNote(jsonStr string) (*notes.Note, error) {
	var n notes.Note
	if err := json.Unmarshal([]byte(jsonStr), &n); err != nil {
		return nil, fmt.Errorf("invalid note JSON: %w", err)
	}
	return &n, nil
}

// Callers lists files that reference the given symbol across packages.
func (a *App) Callers(name string) (string, error) {
	idx, err := a.Indexer.Store().LoadIndex()
	if err != nil {
		return "", err
	}
	callers, err := a.Graph.CallersOfSymbol(idx, name)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if len(callers) == 0 {
		return "no callers found\n", nil
	}
	for _, c := range callers {
		fmt.Fprintf(&b, "%s: uses %s (via %s)\n", c.File, c.Sym, c.Pkg)
	}
	return b.String(), nil
}

// Affected lists test files impacted by a set of changed files.
func (a *App) Affected(changed []string) (string, error) {
	idx, err := a.Indexer.Store().LoadIndex()
	if err != nil {
		return "", err
	}
	tests, err := a.Graph.AffectedTests(idx, changed)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if len(tests) == 0 {
		return "no tests affected\n", nil
	}
	for _, t := range tests {
		fmt.Fprintf(&b, "%s\n", t)
	}
	return b.String(), nil
}
