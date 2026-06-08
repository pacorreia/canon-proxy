package store

import (
	"testing"
	"time"

	"github.com/pacorreia/canon-proxy/internal/db"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	gdb, err := db.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	repo := db.NewImageRepo(gdb)
	return New(repo)
}

func TestAddAndGet(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	added := s.Add("IMG_001.JPG", "http://cam/img/IMG_001.JPG", "", nil, false)
	if !added {
		t.Fatal("expected Add to return true for new entry")
	}

	added = s.Add("IMG_001.JPG", "http://cam/img/IMG_001.JPG", "", nil, false)
	if added {
		t.Fatal("expected Add to return false for duplicate URL")
	}

	e := s.GetByFilename("IMG_001.JPG")
	if e == nil {
		t.Fatal("expected entry to be present")
	}
	if e.Filename != "IMG_001.JPG" {
		t.Fatalf("expected filename IMG_001.JPG, got %s", e.Filename)
	}
	if e.Status != StatusDiscovered {
		t.Fatalf("expected status discovered, got %s", e.Status)
	}
}

func TestSetStatus(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	s.Add("IMG_002.JPG", "http://cam/img/IMG_002.JPG", "", nil, false)
	s.SetStatus("http://cam/img/IMG_002.JPG", StatusDone, "")
	e := s.GetByFilename("IMG_002.JPG")
	if e.Status != StatusDone {
		t.Fatalf("expected status done, got %s", e.Status)
	}
}

func TestList(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	s.Add("A.JPG", "http://cam/A.JPG", "", nil, false)
	s.Add("B.JPG", "http://cam/B.JPG", "", nil, false)
	list := s.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(list))
	}
}

func TestGetByFilename(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	s.Add("C.JPG", "http://cam/C.JPG", "", nil, false)
	e := s.GetByFilename("C.JPG")
	if e == nil {
		t.Fatal("expected entry by filename")
	}
	if e.URL != "http://cam/C.JPG" {
		t.Fatalf("unexpected URL %s", e.URL)
	}
}

func TestDiscoveredByFilenames(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	s.Add("D.JPG", "http://cam/D.JPG", "", nil, false)
	s.Add("E.JPG", "http://cam/E.JPG", "", nil, false)
	s.SetStatus("http://cam/D.JPG", StatusDone, "")
	discovered := s.DiscoveredByFilenames([]string{"D.JPG", "E.JPG"})
	if len(discovered) != 1 || discovered[0].Filename != "E.JPG" {
		t.Fatalf("expected only E.JPG discovered, got %+v", discovered)
	}
}

func TestAllQueued(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	s.Add("F.JPG", "http://cam/F.JPG", "", nil, false)
	s.Add("G.JPG", "http://cam/G.JPG", "", nil, false)
	s.MarkQueued([]string{"http://cam/F.JPG", "http://cam/G.JPG"})
	s.SetStatus("http://cam/F.JPG", StatusUploading, "")
	queued := s.AllQueued()
	if len(queued) != 1 || queued[0].Filename != "G.JPG" {
		t.Fatalf("expected only G.JPG queued, got %+v", queued)
	}
}

func TestAllFailed_And_ResetOnlyFailed(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	s.Add("H.JPG", "http://cam/H.JPG", "", nil, false)
	s.Add("I.JPG", "http://cam/I.JPG", "", nil, false)
	s.SetStatus("http://cam/H.JPG", StatusFailed, "network error")
	s.SetStatus("http://cam/I.JPG", StatusDone, "")

	failed := s.AllFailed()
	if len(failed) != 1 || failed[0].Filename != "H.JPG" {
		t.Fatalf("AllFailed: expected [H.JPG], got %+v", failed)
	}

	n := s.ResetOnlyFailed()
	if n != 1 {
		t.Errorf("ResetOnlyFailed: expected 1, got %d", n)
	}
	e := s.GetByFilename("H.JPG")
	if e.Status != StatusQueued {
		t.Errorf("after ResetOnlyFailed: expected queued, got %q", e.Status)
	}
}

func TestMarkAllDiscoveredQueued(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	s.Add("J.JPG", "http://cam/J.JPG", "", nil, false)
	s.Add("K.JPG", "http://cam/K.JPG", "", nil, false)
	s.SetStatus("http://cam/K.JPG", StatusDone, "")

	n := s.MarkAllDiscoveredQueued()
	if n != 1 {
		t.Errorf("MarkAllDiscoveredQueued: expected 1, got %d", n)
	}
	e := s.GetByFilename("J.JPG")
	if e.Status != StatusQueued {
		t.Errorf("expected J.JPG queued, got %q", e.Status)
	}
}

func TestAllFreshQueued(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	s.Add("L.JPG", "http://cam/L.JPG", "", nil, false)
	s.Add("M.JPG", "http://cam/M.JPG", "", nil, false)
	s.MarkQueued([]string{"http://cam/L.JPG", "http://cam/M.JPG"})
	// Simulate a retry on M: bump retry_count.
	s.SetRetryQueued("http://cam/M.JPG", 1, time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), "retry")

	fresh := s.AllFreshQueued()
	if len(fresh) != 1 || fresh[0].Filename != "L.JPG" {
		t.Errorf("AllFreshQueued: expected only L.JPG, got %+v", fresh)
	}
}

func TestListReadyToRetry(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	s.Add("N.JPG", "http://cam/N.JPG", "", nil, false)
	s.Add("O.JPG", "http://cam/O.JPG", "", nil, false)

	// N is ready to retry (next_retry_at in the past).
	s.SetRetryQueued("http://cam/N.JPG", 1, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), "err")
	// O's retry is in the future.
	s.SetRetryQueued("http://cam/O.JPG", 1, time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), "err")

	ready := s.ListReadyToRetry()
	if len(ready) != 1 || ready[0].Filename != "N.JPG" {
		t.Errorf("ListReadyToRetry: expected [N.JPG], got %+v", ready)
	}
}

func TestResetQueued(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	s.Add("P.JPG", "http://cam/P.JPG", "", nil, false)
	s.Add("Q.JPG", "http://cam/Q.JPG", "", nil, false)
	s.MarkQueued([]string{"http://cam/P.JPG", "http://cam/Q.JPG"})

	n := s.ResetQueued()
	if n != 2 {
		t.Errorf("ResetQueued: expected 2, got %d", n)
	}
	for _, f := range []string{"P.JPG", "Q.JPG"} {
		e := s.GetByFilename(f)
		if e.Status != StatusDiscovered {
			t.Errorf("%s: expected discovered after ResetQueued, got %q", f, e.Status)
		}
	}
}

func TestResetStuckUploading(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	s.Add("R.JPG", "http://cam/R.JPG", "", nil, false)
	s.SetStatus("http://cam/R.JPG", StatusUploading, "")

	s.ResetStuckUploading()
	e := s.GetByFilename("R.JPG")
	if e.Status != StatusQueued {
		t.Errorf("expected queued after ResetStuckUploading, got %q", e.Status)
	}
}

func TestSetOnChange(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	called := 0
	s.SetOnChange(func() { called++ })

	s.Add("S.JPG", "http://cam/S.JPG", "", nil, false)
	if called != 1 {
		t.Errorf("expected onChange called 1 time after Add, got %d", called)
	}
	s.SetStatus("http://cam/S.JPG", StatusDone, "")
	if called != 2 {
		t.Errorf("expected onChange called 2 times after SetStatus, got %d", called)
	}
}

func TestGetByFilename_Missing(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	e := s.GetByFilename("NOTEXIST.JPG")
	if e != nil {
		t.Errorf("expected nil for missing filename, got %+v", e)
	}
}
