package notes

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stephen/advisor/internal/store"
)

// State is the lifecycle state of a note.
type State string

const (
	StateDraft    State = "draft"
	StateReviewed State = "reviewed"
	StateStale    State = "stale"
	StateConflict State = "conflict"
)

// Note is a human-authored claim about a directory.
type Note struct {
	Fingerprint string            `json:"fingerprint"`
	State       State             `json:"state"`
	Purpose     string            `json:"purpose"`
	Conventions map[string]string `json:"conventions"`
	Invariants  []string          `json:"invariants"`
}

// Manager handles note lifecycle, merge, and drift detection.
type Manager struct {
	store *store.Store
}

// New creates a note manager over a store.
func New(s *store.Store) *Manager {
	return &Manager{store: s}
}

// Load reads a note for a directory, or nil if none exists.
func (m *Manager) Load(dir string) (*Note, error) {
	var n Note
	if err := m.store.GetNote(dir, &n); err != nil {
		return nil, nil
	}
	return &n, nil
}

// Save writes a note for a directory.
func (m *Manager) Save(dir string, n *Note) error {
	return m.store.PutNote(dir, n)
}

// Set stores a note as draft (used by `advisor note --set`).
func (m *Manager) Set(dir string, n *Note) error {
	n.State = StateDraft
	return m.Save(dir, n)
}

// Approve promotes a note to reviewed, stamping the current fingerprint.
func (m *Manager) Approve(dir string, n *Note, fingerprint string) error {
	n.State = StateReviewed
	n.Fingerprint = fingerprint
	return m.Save(dir, n)
}

// Edit stores a note as reviewed (used by `advisor note --edit`).
func (m *Manager) Edit(dir string, n *Note, fingerprint string) error {
	n.State = StateReviewed
	n.Fingerprint = fingerprint
	return m.Save(dir, n)
}

// Evaluate determines the current state of a note against the directory
// fingerprint, and whether a re-draft differs from the reviewed note.
func (m *Manager) Evaluate(dir string, fingerprint string) (*Note, State, bool, error) {
	n, err := m.Load(dir)
	if err != nil {
		return nil, "", false, err
	}
	if n == nil {
		return nil, StateDraft, false, nil
	}
	if n.State == StateReviewed && n.Fingerprint == fingerprint {
		return n, StateReviewed, false, nil
	}
	if n.State == StateDraft {
		return n, StateDraft, false, nil
	}
	// Reviewed but drifted: re-draft and diff against the reviewed note.
	draft := Draft(dir, fingerprint)
	differs := !sameInvariants(n, draft)
	if differs {
		return n, StateConflict, true, nil
	}
	return n, StateStale, true, nil
}

// Draft builds a draft note for a directory. In v1 this is a structural
// placeholder; the LLM-handoff flow fills in real content.
func Draft(dir, fingerprint string) *Note {
	return &Note{
		Fingerprint: fingerprint,
		State:       StateDraft,
		Purpose:     "",
		Conventions: map[string]string{},
		Invariants:  []string{},
	}
}

// Merge combines notes from root to leaf. Invariants are additive; the most
// specific purpose and conventions win; state is the weakest link. Staleness
// is evaluated per-note against the current directory fingerprints.
func Merge(chain []ChainItem, dirFingerprints map[string]string) *Note {
	if len(chain) == 0 {
		return nil
	}
	merged := &Note{
		State:       StateReviewed,
		Conventions: map[string]string{},
		Invariants:  []string{},
	}
	// Purpose: most specific (last in chain) wins, breadcrumbed.
	var purposes []string
	for _, item := range chain {
		if item.Note.Purpose != "" {
			purposes = append(purposes, item.Note.Purpose)
		}
	}
	merged.Purpose = strings.Join(purposes, " / ")

	// Conventions: most specific wins.
	for _, item := range chain {
		for k, v := range item.Note.Conventions {
			merged.Conventions[k] = v
		}
	}
	// Invariants: additive union, deduped, order preserved.
	seen := map[string]bool{}
	for _, item := range chain {
		for _, inv := range item.Note.Invariants {
			if !seen[inv] {
				seen[inv] = true
				merged.Invariants = append(merged.Invariants, inv)
			}
		}
	}
	// State: weakest link, annotated with source. A reviewed note whose
	// fingerprint no longer matches the current directory fingerprint is stale.
	weakest := StateReviewed
	var weakSource string
	for _, item := range chain {
		st := item.Note.State
		if st == StateReviewed && dirFingerprints != nil {
			if fp, ok := dirFingerprints[item.Dir]; ok && fp != "" && item.Note.Fingerprint != fp {
				st = StateStale
			}
		}
		if rank(st) < rank(weakest) {
			weakest = st
			weakSource = item.Dir
		}
	}
	merged.State = weakest
	if weakSource != "" && weakest != StateReviewed {
		merged.Purpose = fmt.Sprintf("%s (state from: %s)", merged.Purpose, weakSource)
	}
	return merged
}

// ChainItem is a note plus the directory it governs.
type ChainItem struct {
	Dir  string
	Note *Note
}

// Chain returns the note chain for a directory path, root to leaf.
func (m *Manager) Chain(dir string) ([]ChainItem, error) {
	dir = filepath.ToSlash(dir)
	var parts []string
	for _, p := range strings.Split(dir, "/") {
		if p != "" && p != "." {
			parts = append(parts, p)
		}
	}
	var chain []ChainItem
	for i := 0; i <= len(parts); i++ {
		prefix := strings.Join(parts[:i], "/")
		if prefix == "" {
			continue
		}
		n, err := m.Load(prefix)
		if err != nil {
			return nil, err
		}
		if n != nil {
			chain = append(chain, ChainItem{Dir: prefix, Note: n})
		}
	}
	return chain, nil
}

// Review returns all notes with their current state, for the review command.
func (m *Manager) Review(dirFingerprints map[string]string) ([]ReviewItem, error) {
	dirs, err := m.store.ListNotes()
	if err != nil {
		return nil, err
	}
	var items []ReviewItem
	for _, dir := range dirs {
		n, err := m.Load(dir)
		if err != nil {
			return nil, err
		}
		if n == nil {
			continue
		}
		fp := dirFingerprints[dir]
		state := n.State
		if n.State == StateReviewed && n.Fingerprint != fp {
			state = StateStale
		}
		items = append(items, ReviewItem{Dir: dir, Note: n, State: state})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Dir < items[j].Dir })
	return items, nil
}

// ReviewItem is a note plus its evaluated state.
type ReviewItem struct {
	Dir   string
	Note  *Note
	State State
}

// Diff describes how a draft differs from a reviewed note.
func Diff(reviewed, draft *Note) string {
	var lines []string
	if reviewed.Purpose != draft.Purpose {
		lines = append(lines, fmt.Sprintf("purpose: %q -> %q", reviewed.Purpose, draft.Purpose))
	}
	for k, v := range draft.Conventions {
		if reviewed.Conventions[k] != v {
			lines = append(lines, fmt.Sprintf("convention %s: %q -> %q", k, reviewed.Conventions[k], v))
		}
	}
	for _, inv := range draft.Invariants {
		if !contains(reviewed.Invariants, inv) {
			lines = append(lines, fmt.Sprintf("invariant added: %q", inv))
		}
	}
	for _, inv := range reviewed.Invariants {
		if !contains(draft.Invariants, inv) {
			lines = append(lines, fmt.Sprintf("invariant removed: %q", inv))
		}
	}
	if len(lines) == 0 {
		return "no relevant change"
	}
	return strings.Join(lines, "\n")
}

func sameInvariants(a, b *Note) bool {
	if len(a.Invariants) != len(b.Invariants) {
		return false
	}
	for _, inv := range a.Invariants {
		if !contains(b.Invariants, inv) {
			return false
		}
	}
	return true
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func rank(s State) int {
	switch s {
	case StateReviewed:
		return 3
	case StateDraft:
		return 2
	case StateStale:
		return 1
	case StateConflict:
		return 0
	}
	return 3
}

// MarshalJSON ensures empty maps serialize as {} not null.
func (n *Note) MarshalJSON() ([]byte, error) {
	type alias Note
	if n.Conventions == nil {
		n.Conventions = map[string]string{}
	}
	if n.Invariants == nil {
		n.Invariants = []string{}
	}
	return json.Marshal((*alias)(n))
}
