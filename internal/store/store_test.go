package store

import (
	"testing"

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

	added := s.Add("IMG_001.JPG", "http://cam/img/IMG_001.JPG", nil, false)
	if !added {
		t.Fatal("expected Add to return true for new entry")
	}

	added = s.Add("IMG_001.JPG", "http://cam/img/IMG_001.JPG", nil, false)
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
	s.Add("IMG_002.JPG", "http://cam/img/IMG_002.JPG", nil, false)
	s.SetStatus("http://cam/img/IMG_002.JPG", StatusDone, "")
	e := s.GetByFilename("IMG_002.JPG")
	if e.Status != StatusDone {
		t.Fatalf("expected status done, got %s", e.Status)
	}
}

func TestList(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	s.Add("A.JPG", "http://cam/A.JPG", nil, false)
	s.Add("B.JPG", "http://cam/B.JPG", nil, false)
	list := s.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(list))
	}
}

func TestGetByFilename(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	s.Add("C.JPG", "http://cam/C.JPG", nil, false)
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
	s.Add("D.JPG", "http://cam/D.JPG", nil, false)
	s.Add("E.JPG", "http://cam/E.JPG", nil, false)
	s.SetStatus("http://cam/D.JPG", StatusDone, "")
	discovered := s.DiscoveredByFilenames([]string{"D.JPG", "E.JPG"})
	if len(discovered) != 1 || discovered[0].Filename != "E.JPG" {
		t.Fatalf("expected only E.JPG discovered, got %+v", discovered)
	}
}

func TestAllQueued(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	s.Add("F.JPG", "http://cam/F.JPG", nil, false)
	s.Add("G.JPG", "http://cam/G.JPG", nil, false)
	s.MarkQueued([]string{"http://cam/F.JPG", "http://cam/G.JPG"})
	s.SetStatus("http://cam/F.JPG", StatusUploading, "")
	queued := s.AllQueued()
	if len(queued) != 1 || queued[0].Filename != "G.JPG" {
		t.Fatalf("expected only G.JPG queued, got %+v", queued)
	}
}
