package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHashAndFingerprint(t *testing.T) {
	if Hash([]byte("a")) == Hash([]byte("b")) {
		t.Error("different content should hash differently")
	}
	if Hash([]byte("a")) != Hash([]byte("a")) {
		t.Error("same content should hash identically")
	}
	fp1 := DirFingerprint([]string{"b", "a"})
	fp2 := DirFingerprint([]string{"a", "b"})
	if fp1 != fp2 {
		t.Error("directory fingerprint should be order-independent")
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, ".advisor"))

	idx, err := s.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Files) != 0 {
		t.Error("fresh index should be empty")
	}

	idx.Files["a.py"] = &FileEntry{Path: "a.py", FileHash: "h1", IndexRef: "h1"}
	if err := s.SaveIndex(idx); err != nil {
		t.Fatal(err)
	}

	got, err := s.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	if got.Files["a.py"].FileHash != "h1" {
		t.Error("round-trip failed")
	}

	type payload struct{ N int }
	if err := s.PutFile("h1", payload{N: 42}); err != nil {
		t.Fatal(err)
	}
	var p payload
	if err := s.GetFile("h1", &p); err != nil {
		t.Fatal(err)
	}
	if p.N != 42 {
		t.Error("file artifact round-trip failed")
	}
}

func TestNoteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, ".advisor"))

	type note struct{ Purpose string }
	if err := s.PutNote("internal/parser", note{Purpose: "auth"}); err != nil {
		t.Fatal(err)
	}
	if !s.NoteExists("internal/parser") {
		t.Error("note should exist")
	}
	var n note
	if err := s.GetNote("internal/parser", &n); err != nil {
		t.Fatal(err)
	}
	if n.Purpose != "auth" {
		t.Error("note round-trip failed")
	}

	dirs, err := s.ListNotes()
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 1 || dirs[0] != "internal/parser" {
		t.Errorf("ListNotes = %v, want [internal/parser]", dirs)
	}
}

func TestGC(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, ".advisor"))

	idx, _ := s.LoadIndex()
	idx.Files["a.py"] = &FileEntry{Path: "a.py", FileHash: "keep", IndexRef: "keep"}
	if err := s.PutFile("keep", map[string]string{"x": "y"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutFile("orphan", map[string]string{"x": "y"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveIndex(idx); err != nil {
		t.Fatal(err)
	}
	if err := s.GC(idx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".advisor", "files", "orphan.json")); !os.IsNotExist(err) {
		t.Error("orphan should be removed")
	}
	if _, err := os.Stat(filepath.Join(dir, ".advisor", "files", "keep.json")); err != nil {
		t.Error("referenced artifact should be kept")
	}
}
