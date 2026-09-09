package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FileEntry records what the index knows about a single source file.
type FileEntry struct {
	Path     string `json:"path"`
	FileHash string `json:"file_hash"`
	IndexRef string `json:"index_ref"`
	NoteRef  string `json:"note_ref,omitempty"`
	State    string `json:"state,omitempty"`
}

// DirEntry caches a directory fingerprint and when it was computed.
type DirEntry struct {
	Fingerprint string    `json:"fingerprint"`
	ComputedAt  time.Time `json:"computed_at"`
}

// Index is the root document of the store.
type Index struct {
	Files map[string]*FileEntry `json:"files"`
	Dirs  map[string]*DirEntry  `json:"dirs"`
}

// Store is a content-addressed store rooted at a directory.
type Store struct {
	root string
}

// New creates a store rooted at root (e.g. <project>/.astral).
func New(root string) *Store {
	return &Store{root: root}
}

// Root returns the store root path.
func (s *Store) Root() string { return s.root }

// Hash computes the SHA-256 of content, hex-encoded.
func Hash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// HashFile computes the SHA-256 of a file on disk.
func HashFile(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return Hash(content), nil
}

// DirFingerprint computes the directory fingerprint from the sorted file hashes.
func DirFingerprint(fileHashes []string) string {
	sorted := append([]string(nil), fileHashes...)
	sort.Strings(sorted)
	return Hash([]byte(strings.Join(sorted, "\n")))
}

// LoadIndex reads index.json, returning an empty index if absent.
func (s *Store) LoadIndex() (*Index, error) {
	idx := &Index{Files: map[string]*FileEntry{}, Dirs: map[string]*DirEntry{}}
	content, err := os.ReadFile(s.indexPath())
	if err != nil {
		if os.IsNotExist(err) {
			return idx, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(content, idx); err != nil {
		return nil, fmt.Errorf("corrupt index.json: %w", err)
	}
	if idx.Files == nil {
		idx.Files = map[string]*FileEntry{}
	}
	if idx.Dirs == nil {
		idx.Dirs = map[string]*DirEntry{}
	}
	return idx, nil
}

// SaveIndex writes index.json atomically (temp + rename).
func (s *Store) SaveIndex(idx *Index) error {
	content, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return s.writeAtomic(s.indexPath(), content)
}

// PutFile stores a derived artifact under files/<hash>.json.
func (s *Store) PutFile(hash string, data any) error {
	content, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return s.writeAtomic(s.filePath(hash), content)
}

// GetFile reads a derived artifact from files/<hash>.json.
func (s *Store) GetFile(hash string, out any) error {
	content, err := os.ReadFile(s.filePath(hash))
	if err != nil {
		return err
	}
	return json.Unmarshal(content, out)
}

// PutNote stores a human-authored note at notes/<dir_path>.json.
func (s *Store) PutNote(dirPath string, data any) error {
	content, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return s.writeAtomic(s.notePath(dirPath), content)
}

// GetNote reads a human-authored note from notes/<dir_path>.json.
func (s *Store) GetNote(dirPath string, out any) error {
	content, err := os.ReadFile(s.notePath(dirPath))
	if err != nil {
		return err
	}
	return json.Unmarshal(content, out)
}

// NoteExists reports whether a note file exists for the directory.
func (s *Store) NoteExists(dirPath string) bool {
	_, err := os.Stat(s.notePath(dirPath))
	return err == nil
}

// ListNotes returns the directory paths that have notes, recursively.
func (s *Store) ListNotes() ([]string, error) {
	notesDir := filepath.Join(s.root, "notes")
	var dirs []string
	err := filepath.Walk(notesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".json") {
			return nil
		}
		rel, err := filepath.Rel(notesDir, path)
		if err != nil {
			return err
		}
		dirs = append(dirs, strings.TrimSuffix(filepath.ToSlash(rel), ".json"))
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	sort.Strings(dirs)
	return dirs, nil
}

// GC removes derived artifacts not referenced by the index.
func (s *Store) GC(idx *Index) error {
	referenced := map[string]bool{}
	for _, f := range idx.Files {
		referenced[f.FileHash] = true
		referenced[f.IndexRef] = true
	}
	filesDir := filepath.Join(s.root, "files")
	entries, err := os.ReadDir(filesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		hash := strings.TrimSuffix(name, ".json")
		if !referenced[hash] {
			if err := os.Remove(filepath.Join(filesDir, name)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) indexPath() string  { return filepath.Join(s.root, "index.json") }
func (s *Store) filePath(h string) string {
	return filepath.Join(s.root, "files", h+".json")
}
func (s *Store) notePath(dir string) string {
	return filepath.Join(s.root, "notes", dir+".json")
}

func (s *Store) writeAtomic(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
