package notes

import (
	"path/filepath"
	"testing"

	"astral/internal/store"
)

func newManager(t *testing.T) *Manager {
	t.Helper()
	s := store.New(filepath.Join(t.TempDir(), ".astral"))
	return New(s)
}

func TestLifecycle(t *testing.T) {
	m := newManager(t)
	dir := "services/auth"

	// No note yet.
	n, state, differs, err := m.Evaluate(dir, "fp1")
	if err != nil {
		t.Fatal(err)
	}
	if n != nil || state != StateDraft || differs {
		t.Errorf("fresh dir: got (%v, %s, %v), want (nil, draft, false)", n, state, differs)
	}

	// Set as draft.
	draft := Draft(dir, "fp1")
	draft.Purpose = "auth"
	if err := m.Set(dir, draft); err != nil {
		t.Fatal(err)
	}
	n, state, differs, err = m.Evaluate(dir, "fp1")
	if err != nil {
		t.Fatal(err)
	}
	if state != StateDraft {
		t.Errorf("draft state = %s, want draft", state)
	}

	// Approve.
	if err := m.Approve(dir, n, "fp1"); err != nil {
		t.Fatal(err)
	}
	n, state, differs, err = m.Evaluate(dir, "fp1")
	if err != nil {
		t.Fatal(err)
	}
	if state != StateReviewed || differs {
		t.Errorf("approved: got (%s, %v), want (reviewed, false)", state, differs)
	}

	// Drift: fingerprint changes, invariants unchanged -> stale.
	n, state, differs, err = m.Evaluate(dir, "fp2")
	if err != nil {
		t.Fatal(err)
	}
	if state != StateStale || !differs {
		t.Errorf("drift: got (%s, %v), want (stale, true)", state, differs)
	}

	// Drift with invariant change -> conflict.
	n.Invariants = []string{"new invariant"}
	if err := m.Save(dir, n); err != nil {
		t.Fatal(err)
	}
	_, state, differs, err = m.Evaluate(dir, "fp3")
	if err != nil {
		t.Fatal(err)
	}
	if state != StateConflict || !differs {
		t.Errorf("conflict: got (%s, %v), want (conflict, true)", state, differs)
	}
}

func TestMergeAdditive(t *testing.T) {
	m := newManager(t)
	root := &Note{State: StateReviewed, Purpose: "backend", Conventions: map[string]string{"naming": "snake_case"}, Invariants: []string{"no panics"}}
	leaf := &Note{State: StateReviewed, Purpose: "auth", Conventions: map[string]string{"naming": "camelCase"}, Invariants: []string{"reject with SomeSpecialError"}}
	if err := m.Save("services", root); err != nil {
		t.Fatal(err)
	}
	if err := m.Save("services/auth", leaf); err != nil {
		t.Fatal(err)
	}

	chain, err := m.Chain("services/auth")
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 2 {
		t.Fatalf("chain len = %d, want 2", len(chain))
	}
	merged := Merge(chain, nil)
	if merged.Purpose != "backend / auth" {
		t.Errorf("purpose = %q, want breadcrumb", merged.Purpose)
	}
	if merged.Conventions["naming"] != "camelCase" {
		t.Errorf("naming = %q, want most-specific (camelCase)", merged.Conventions["naming"])
	}
	if len(merged.Invariants) != 2 {
		t.Errorf("invariants = %v, want additive union of both", merged.Invariants)
	}
	if merged.State != StateReviewed {
		t.Errorf("state = %s, want reviewed", merged.State)
	}
}

func TestMergeWeakestLink(t *testing.T) {
	m := newManager(t)
	root := &Note{State: StateReviewed, Purpose: "backend"}
	leaf := &Note{State: StateDraft, Purpose: "auth"}
	if err := m.Save("services", root); err != nil {
		t.Fatal(err)
	}
	if err := m.Save("services/auth", leaf); err != nil {
		t.Fatal(err)
	}
	chain, err := m.Chain("services/auth")
	if err != nil {
		t.Fatal(err)
	}
	merged := Merge(chain, nil)
	if merged.State != StateDraft {
		t.Errorf("state = %s, want draft (weakest link)", merged.State)
	}
}

func TestMergeStaleParent(t *testing.T) {
	m := newManager(t)
	root := &Note{State: StateReviewed, Purpose: "backend", Fingerprint: "old"}
	leaf := &Note{State: StateReviewed, Purpose: "auth", Fingerprint: "fp"}
	if err := m.Save("services", root); err != nil {
		t.Fatal(err)
	}
	if err := m.Save("services/auth", leaf); err != nil {
		t.Fatal(err)
	}
	chain, err := m.Chain("services/auth")
	if err != nil {
		t.Fatal(err)
	}
	fps := map[string]string{"services": "new", "services/auth": "fp"}
	merged := Merge(chain, fps)
	if merged.State != StateStale {
		t.Errorf("state = %s, want stale (parent drifted)", merged.State)
	}
}

func TestDiff(t *testing.T) {
	reviewed := &Note{Purpose: "auth", Invariants: []string{"a"}}
	draft := &Note{Purpose: "auth", Invariants: []string{"a", "b"}}
	d := Diff(reviewed, draft)
	if d == "no relevant change" {
		t.Error("expected a diff")
	}
	if Diff(reviewed, reviewed) != "no relevant change" {
		t.Error("identical notes should show no change")
	}
}
