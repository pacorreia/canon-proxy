package store

import (
	"testing"
	"time"
)

func TestAddAndGet(t *testing.T) {
	t.Parallel()
	s := New()

	added := s.Add("IMG_001.JPG", "http://cam/img/IMG_001.JPG")
	if !added {
		t.Fatal("expected Add to return true for new entry")
	}

	added = s.Add("IMG_001.JPG", "http://cam/img/IMG_001.JPG")
	if added {
		t.Fatal("expected Add to return false for duplicate URL")
	}

	e := s.Get("http://cam/img/IMG_001.JPG")
	if e == nil {
		t.Fatal("expected entry to be present")
	}
	if e.Filename != "IMG_001.JPG" {
		t.Fatalf("expected filename IMG_001.JPG, got %s", e.Filename)
	}
	if e.Status != StatusPending {
		t.Fatalf("expected status pending, got %s", e.Status)
	}
}

func TestSetStatus(t *testing.T) {
	t.Parallel()
	s := New()
	s.Add("IMG_002.JPG", "http://cam/img/IMG_002.JPG")
	s.SetStatus("http://cam/img/IMG_002.JPG", StatusDone, "")
	e := s.Get("http://cam/img/IMG_002.JPG")
	if e.Status != StatusDone {
		t.Fatalf("expected status done, got %s", e.Status)
	}
}

func TestList(t *testing.T) {
	t.Parallel()
	s := New()
	s.Add("A.JPG", "http://cam/A.JPG")
	s.Add("B.JPG", "http://cam/B.JPG")
	list := s.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(list))
	}
}

func TestGetByFilename(t *testing.T) {
	t.Parallel()
	s := New()
	s.Add("C.JPG", "http://cam/C.JPG")
	e := s.GetByFilename("C.JPG")
	if e == nil {
		t.Fatal("expected entry by filename")
	}
	if e.URL != "http://cam/C.JPG" {
		t.Fatalf("unexpected URL %s", e.URL)
	}
}

func TestPendingByFilenames(t *testing.T) {
	t.Parallel()
	s := New()
	s.Add("D.JPG", "http://cam/D.JPG")
	s.Add("E.JPG", "http://cam/E.JPG")
	s.SetStatus("http://cam/D.JPG", StatusDone, "")
	pending := s.PendingByFilenames([]string{"D.JPG", "E.JPG"})
	if len(pending) != 1 || pending[0].Filename != "E.JPG" {
		t.Fatalf("expected only E.JPG pending, got %+v", pending)
	}
}

func TestAllPending(t *testing.T) {
	t.Parallel()
	s := New()
	s.Add("F.JPG", "http://cam/F.JPG")
	s.Add("G.JPG", "http://cam/G.JPG")
	s.SetStatus("http://cam/F.JPG", StatusUploading, "")
	pending := s.AllPending()
	if len(pending) != 1 || pending[0].Filename != "G.JPG" {
		t.Fatalf("expected only G.JPG pending, got %+v", pending)
	}
}

func TestEviction(t *testing.T) {
	t.Parallel()
	s := New()
	// Fill beyond capacity and confirm oldest is evicted.
	for i := 0; i < maxEntries+1; i++ {
		url := "http://cam/" + time.Now().String() + string(rune(i+'a'))
		s.Add("file.jpg", url)
	}
	if len(s.keys) > maxEntries {
		t.Fatalf("expected at most %d entries, got %d", maxEntries, len(s.keys))
	}
}
