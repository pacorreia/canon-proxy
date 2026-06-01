package store

import (
	"sync"
	"time"
)

// Status represents the processing state of an image.
type Status string

const (
	StatusPending   Status = "pending"
	StatusUploading Status = "uploading"
	StatusDone      Status = "done"
	StatusFailed    Status = "failed"
)

// Entry holds metadata for a detected image.
type Entry struct {
	Filename    string    `json:"filename"`
	URL         string    `json:"url"`
	Status      Status    `json:"status"`
	DetectedAt  time.Time `json:"detected_at"`
	Error       string    `json:"error,omitempty"`
}

const maxEntries = 50_000

// Store is a thread-safe registry of detected images.
type Store struct {
	mu      sync.RWMutex
	entries map[string]*Entry // keyed by URL
	keys    []string          // insertion-ordered URLs for eviction
}

// New returns an initialised Store.
func New() *Store {
	return &Store{
		entries: make(map[string]*Entry),
	}
}

// Add inserts an image entry if not already present.
// Returns true if the entry was newly added.
func (s *Store) Add(filename, url string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.entries[url]; ok {
		return false
	}

	e := &Entry{
		Filename:   filename,
		URL:        url,
		Status:     StatusPending,
		DetectedAt: time.Now().UTC(),
	}
	s.entries[url] = e
	s.keys = append(s.keys, url)

	// Evict oldest if over capacity.
	if len(s.keys) > maxEntries {
		oldest := s.keys[0]
		s.keys = s.keys[1:]
		delete(s.entries, oldest)
	}

	return true
}

// Get returns the entry for the given URL, or nil if not found.
func (s *Store) Get(url string) *Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.entries[url]
	if e == nil {
		return nil
	}
	cp := *e
	return &cp
}

// GetByFilename returns the entry with the given filename, or nil if not found.
func (s *Store) GetByFilename(filename string) *Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.entries {
		if e.Filename == filename {
			cp := *e
			return &cp
		}
	}
	return nil
}

// SetStatus updates the status (and optional error message) for the entry
// identified by URL.
func (s *Store) SetStatus(url string, status Status, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[url]; ok {
		e.Status = status
		e.Error = errMsg
	}
}

// List returns a snapshot of all entries in insertion order.
func (s *Store) List() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entry, 0, len(s.keys))
	for _, k := range s.keys {
		if e, ok := s.entries[k]; ok {
			out = append(out, *e)
		}
	}
	return out
}

// PendingByFilenames returns entries matching the given filenames that are
// still in StatusPending.
func (s *Store) PendingByFilenames(filenames []string) []Entry {
	set := make(map[string]struct{}, len(filenames))
	for _, f := range filenames {
		set[f] = struct{}{}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Entry
	for _, e := range s.entries {
		if _, ok := set[e.Filename]; ok && e.Status == StatusPending {
			out = append(out, *e)
		}
	}
	return out
}

// AllPending returns all entries in StatusPending.
func (s *Store) AllPending() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Entry
	for _, e := range s.entries {
		if e.Status == StatusPending {
			out = append(out, *e)
		}
	}
	return out
}
