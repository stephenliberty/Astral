package watcher

import (
	"log"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"astral/internal/index"
	"astral/internal/parser"
)

// Watch runs the fsnotify warm-up loop. It is an optimization, not a
// correctness requirement — locate() lazily re-parses stale files.
func Watch(ix *index.Indexer, debounce time.Duration) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()

	root := ix.Store().Root()
	projectRoot := filepath.Dir(root)
	if err := w.Add(projectRoot); err != nil {
		return err
	}

	idx, err := ix.Store().LoadIndex()
	if err != nil {
		return err
	}

	// Debounce: collect events, process after a quiet period.
	var pending []string
	timer := time.NewTimer(debounce)
	timer.Stop()
	flush := func() {
		if len(pending) == 0 {
			return
		}
		seen := map[string]bool{}
		for _, p := range pending {
			if seen[p] {
				continue
			}
			seen[p] = true
			rel, err := filepath.Rel(projectRoot, p)
			if err != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			if _, ok := parser.DetectLanguage(rel); !ok {
				continue
			}
			if err := ix.ReindexFile(idx, rel); err != nil {
				log.Printf("reindex %s: %v", rel, err)
			}
		}
		pending = nil
	}

	for {
		select {
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
				pending = append(pending, ev.Name)
				timer.Reset(debounce)
			}
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			log.Printf("watcher error: %v", err)
		case <-timer.C:
			flush()
		}
	}
}
