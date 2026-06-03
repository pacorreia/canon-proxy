package store

import (
	"log"
	"sync"
	"time"

	"github.com/pacorreia/canon-proxy/internal/db"
)

// Status mirrors db constants, re-exported for pipeline/web compatibility.
type Status = string

const (
	StatusDiscovered Status = db.StatusDiscovered
	StatusQueued     Status = db.StatusQueued
	StatusUploading  Status = db.StatusUploading
	StatusDone       Status = db.StatusDone
	StatusFailed     Status = db.StatusFailed
)

// Entry is the public representation of an image record.
type Entry struct {
	Filename    string     `json:"filename"`
	URL         string     `json:"url"`
	Status      Status     `json:"status"`
	RetryCount  int        `json:"retry_count"`
	Error       string     `json:"error,omitempty"`
	DetectedAt  time.Time  `json:"detected_at"`
	CapturedAt  *time.Time `json:"captured_at,omitempty"`
	NextRetryAt *time.Time `json:"next_retry_at,omitempty"`
	IsVideo     bool       `json:"is_video,omitempty"`
}

// Store is the public façade over the DB image repository.
type Store struct {
	repo     *db.ImageRepo
	mu       sync.RWMutex
	onChange func()
}

// New returns a Store backed by the given ImageRepo.
func New(repo *db.ImageRepo) *Store {
	return &Store{repo: repo}
}

// SetOnChange registers fn to be called (without any lock held) after every mutation.
func (s *Store) SetOnChange(fn func()) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

func (s *Store) notify() {
	s.mu.RLock()
	fn := s.onChange
	s.mu.RUnlock()
	if fn != nil {
		fn()
	}
}

// Add inserts a new image (status=discovered) if it doesn't already exist.
// Returns true if the entry was newly created.
func (s *Store) Add(filename, url string, capturedAt *time.Time, isVideo bool) bool {
	_, created, err := s.repo.FindOrCreate(filename, url, capturedAt, isVideo)
	if err != nil {
		return false
	}
	if created {
		s.notify()
	}
	return created
}

// List returns every image in insertion order.
func (s *Store) List() []Entry {
	recs, err := s.repo.List()
	if err != nil {
		return nil
	}
	return recsToEntries(recs)
}

// GetByFilename returns the entry with the given filename, or nil if not found.
func (s *Store) GetByFilename(filename string) *Entry {
	rec, err := s.repo.GetByFilename(filename)
	if err != nil || rec == nil {
		return nil
	}
	e := recToEntry(*rec)
	return &e
}

// SetStatus updates the status and error message for the image identified by URL.
func (s *Store) SetStatus(url string, status Status, errMsg string) {
	if err := s.repo.SetStatus(url, status, errMsg); err != nil {
		log.Printf("level=error component=store msg=\"SetStatus failed\" url=%q err=%q", url, err)
		return
	}
	s.notify()
}

// SetRetryQueued sets an image back to "queued" with retry metadata.
func (s *Store) SetRetryQueued(url string, retryCount int, nextRetryAt time.Time, errMsg string) {
	_ = s.repo.SetRetryQueued(url, retryCount, nextRetryAt, errMsg)
	s.notify()
}

// MarkQueued transitions discovered images (identified by URL list) to queued.
func (s *Store) MarkQueued(urls []string) {
	if len(urls) == 0 {
		return
	}
	_ = s.repo.MarkQueued(urls)
	s.notify()
}

// MarkAllDiscoveredQueued queues every currently-discovered image.
// Returns the number of images queued.
func (s *Store) MarkAllDiscoveredQueued() int64 {
	n, _ := s.repo.MarkAllDiscoveredQueued()
	if n > 0 {
		s.notify()
	}
	return n
}

// AllQueued returns all images in "queued" status.
func (s *Store) AllQueued() []Entry {
	recs, err := s.repo.ListByStatus(StatusQueued)
	if err != nil {
		return nil
	}
	return recsToEntries(recs)
}

// AllFreshQueued returns queued images that have never been attempted.
func (s *Store) AllFreshQueued() []Entry {
	recs, err := s.repo.ListFreshQueued()
	if err != nil {
		return nil
	}
	return recsToEntries(recs)
}

// DiscoveredByFilenames returns discovered entries matching the given filenames.
func (s *Store) DiscoveredByFilenames(filenames []string) []Entry {
	set := make(map[string]struct{}, len(filenames))
	for _, f := range filenames {
		set[f] = struct{}{}
	}
	recs, err := s.repo.ListByStatus(StatusDiscovered)
	if err != nil {
		return nil
	}
	var out []Entry
	for _, rec := range recs {
		if _, ok := set[rec.Filename]; ok {
			out = append(out, recToEntry(rec))
		}
	}
	return out
}

// AllFailed returns all images in "failed" status.
func (s *Store) AllFailed() []Entry {
	recs, err := s.repo.ListByStatus(StatusFailed)
	if err != nil {
		return nil
	}
	return recsToEntries(recs)
}

// ResetOnlyFailed resets only failed images to queued for retry.
func (s *Store) ResetOnlyFailed() int64 {
	n, _ := s.repo.ResetFailed()
	if n > 0 {
		s.notify()
	}
	return n
}

// ListReadyToRetry returns queued images that have passed their back-off delay.
func (s *Store) ListReadyToRetry() []Entry {
	recs, err := s.repo.ListReadyToRetry()
	if err != nil {
		return nil
	}
	return recsToEntries(recs)
}

// ResetStuckUploading resets images interrupted mid-upload (e.g. by a crash) to queued.
func (s *Store) ResetStuckUploading() {
	_ = s.repo.ResetStuckUploading()
}

// --- helpers -----------------------------------------------------------------

func recsToEntries(recs []db.ImageRecord) []Entry {
	out := make([]Entry, len(recs))
	for i, r := range recs {
		out[i] = recToEntry(r)
	}
	return out
}

func recToEntry(r db.ImageRecord) Entry {
	return Entry{
		Filename:    r.Filename,
		URL:         r.URL,
		Status:      r.Status,
		RetryCount:  r.RetryCount,
		Error:       r.LastError,
		DetectedAt:  r.CreatedAt,
		CapturedAt:  r.CapturedAt,
		NextRetryAt: r.NextRetryAt,
		IsVideo:     r.IsVideo,
	}
}
