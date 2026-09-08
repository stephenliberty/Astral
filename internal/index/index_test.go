package index

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInitAndLocate(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "services/auth.py", "def login(user):\n    return user\n")
	writeFile(t, root, "services/token.py", "class Token:\n    pass\n")
	writeFile(t, root, "README.md", "not code\n")

	ix := New(root)
	idx, err := ix.Init()
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Files) != 2 {
		t.Errorf("indexed %d files, want 2", len(idx.Files))
	}

	entry, sym, err := ix.Locate(idx, "login")
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil || sym == nil {
		t.Fatal("login not found")
	}
	if sym.Line != 1 {
		t.Errorf("login line = %d, want 1", sym.Line)
	}

	// Lazy re-parse: change the file, locate should pick up the new hash.
	writeFile(t, root, "services/auth.py", "def login(user):\n    return user\n\ndef logout(user):\n    pass\n")
	entry, sym, err = ix.Locate(idx, "logout")
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil || sym == nil {
		t.Fatal("logout not found after lazy re-parse")
	}
}

func TestRemoveFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.py", "def foo():\n    pass\n")
	ix := New(root)
	idx, err := ix.Init()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "a.py")); err != nil {
		t.Fatal(err)
	}
	if err := ix.RemoveFile(idx, filepath.Join(root, "a.py")); err != nil {
		t.Fatal(err)
	}
	if _, ok := idx.Files["a.py"]; ok {
		t.Error("removed file should be dropped from index")
	}
}

func TestDirFingerprintChanges(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.py", "def foo():\n    pass\n")
	ix := New(root)
	idx, err := ix.Init()
	if err != nil {
		t.Fatal(err)
	}
	fp1 := ix.DirFingerprintFor(idx, ".")

	writeFile(t, root, "a.py", "def foo():\n    return 1\n")
	if err := ix.ReindexFile(idx, filepath.Join(root, "a.py")); err != nil {
		t.Fatal(err)
	}
	fp2 := ix.DirFingerprintFor(idx, ".")
	if fp1 == fp2 {
		t.Error("directory fingerprint should change when a file changes")
	}
}
