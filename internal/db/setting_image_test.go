package db

import (
	"testing"
	"time"
)

func newTestSettingRepo(t *testing.T) *SettingRepo {
	t.Helper()
	gdb, err := Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return NewSettingRepo(gdb)
}

func TestSettingRepo_GetSet(t *testing.T) {
	t.Parallel()
	repo := newTestSettingRepo(t)

	_, ok := repo.Get("missing.key")
	if ok {
		t.Error("expected Get to return false for missing key")
	}

	if err := repo.Set("log.level", "debug"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	v, ok := repo.Get("log.level")
	if !ok {
		t.Fatal("expected Get to return true for existing key")
	}
	if v != "debug" {
		t.Errorf("Get = %q, want %q", v, "debug")
	}
}

func TestSettingRepo_Set_Upsert(t *testing.T) {
	t.Parallel()
	repo := newTestSettingRepo(t)

	if err := repo.Set("k", "v1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := repo.Set("k", "v2"); err != nil {
		t.Fatalf("Set upsert: %v", err)
	}

	v, _ := repo.Get("k")
	if v != "v2" {
		t.Errorf("expected upserted value %q, got %q", "v2", v)
	}
}

func TestSettingRepo_All(t *testing.T) {
	t.Parallel()
	repo := newTestSettingRepo(t)

	m, err := repo.All()
	if err != nil {
		t.Fatalf("All (empty): %v", err)
	}
	if len(m) != 0 {
		t.Errorf("expected empty map, got %d entries", len(m))
	}

	repo.Set("a", "1")
	repo.Set("b", "2")

	m, err = repo.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(m) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(m))
	}
	if m["a"] != "1" || m["b"] != "2" {
		t.Errorf("unexpected map: %v", m)
	}
}

func TestSettingRepo_SetMany(t *testing.T) {
	t.Parallel()
	repo := newTestSettingRepo(t)

	err := repo.SetMany(map[string]string{"x": "10", "y": "20"})
	if err != nil {
		t.Fatalf("SetMany: %v", err)
	}
	v, _ := repo.Get("x")
	if v != "10" {
		t.Errorf("x = %q, want %q", v, "10")
	}
	v, _ = repo.Get("y")
	if v != "20" {
		t.Errorf("y = %q, want %q", v, "20")
	}
}

func TestSettingRepo_SetMany_Empty(t *testing.T) {
	t.Parallel()
	repo := newTestSettingRepo(t)
	// Should be a no-op, not an error.
	if err := repo.SetMany(nil); err != nil {
		t.Errorf("SetMany(nil): %v", err)
	}
	if err := repo.SetMany(map[string]string{}); err != nil {
		t.Errorf("SetMany({}): %v", err)
	}
}

func TestSettingRepo_SetMany_Upsert(t *testing.T) {
	t.Parallel()
	repo := newTestSettingRepo(t)

	repo.Set("shared", "old")
	if err := repo.SetMany(map[string]string{"shared": "new", "fresh": "yes"}); err != nil {
		t.Fatalf("SetMany upsert: %v", err)
	}
	v, _ := repo.Get("shared")
	if v != "new" {
		t.Errorf("expected %q after upsert, got %q", "new", v)
	}
}

func TestSettingRepo_SeedDefaults(t *testing.T) {
	t.Parallel()
	repo := newTestSettingRepo(t)

	defaults := map[string]string{"log.level": "info", "upload.workers": "2"}
	if err := repo.SeedDefaults(defaults); err != nil {
		t.Fatalf("SeedDefaults: %v", err)
	}
	v, _ := repo.Get("log.level")
	if v != "info" {
		t.Errorf("expected %q, got %q", "info", v)
	}

	// SeedDefaults must not overwrite an existing value.
	repo.Set("log.level", "debug")
	if err := repo.SeedDefaults(defaults); err != nil {
		t.Fatalf("SeedDefaults second call: %v", err)
	}
	v, _ = repo.Get("log.level")
	if v != "debug" {
		t.Errorf("SeedDefaults must not overwrite existing key; got %q, want %q", v, "debug")
	}
}

// ---------- ImageRepo ---------------------------------------------------------

func newTestImageRepo(t *testing.T) *ImageRepo {
	t.Helper()
	gdb, err := Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return NewImageRepo(gdb)
}

func TestImageRepo_FindOrCreate(t *testing.T) {
	t.Parallel()
	repo := newTestImageRepo(t)

	rec, created, _, err := repo.FindOrCreate("IMG_001.JPG", "ptpip://cam/1", "", nil, false)
	if err != nil {
		t.Fatalf("FindOrCreate: %v", err)
	}
	if !created {
		t.Error("expected created=true for new record")
	}
	if rec.Filename != "IMG_001.JPG" || rec.Status != StatusDiscovered {
		t.Errorf("unexpected record: %+v", rec)
	}

	// Second call with same URL → not created.
	_, created, _, err = repo.FindOrCreate("IMG_001.JPG", "ptpip://cam/1", "", nil, false)
	if err != nil {
		t.Fatalf("FindOrCreate duplicate: %v", err)
	}
	if created {
		t.Error("expected created=false for duplicate URL")
	}
}

func TestImageRepo_FindOrCreate_ReuseHandle(t *testing.T) {
	t.Parallel()
	repo := newTestImageRepo(t)

	// Create a "done" image.
	repo.FindOrCreate("OLD.JPG", "ptpip://cam/99", "", nil, false)
	repo.SetStatus("ptpip://cam/99", StatusDone, "")

	// Camera reuses the same URL for a different filename → should reset.
	rec, created, _, err := repo.FindOrCreate("NEW.JPG", "ptpip://cam/99", "", nil, false)
	if err != nil {
		t.Fatalf("FindOrCreate reuse: %v", err)
	}
	if !created {
		t.Error("expected created=true when camera reuses handle with new filename")
	}
	if rec.Status != StatusDiscovered {
		t.Errorf("expected status discovered after reuse, got %q", rec.Status)
	}
	if rec.Filename != "NEW.JPG" {
		t.Errorf("expected filename NEW.JPG, got %q", rec.Filename)
	}
}

func TestImageRepo_FindOrCreate_BackfillCapturedAt(t *testing.T) {
	t.Parallel()
	repo := newTestImageRepo(t)

	// First insert without capturedAt.
	repo.FindOrCreate("IMG_002.JPG", "ptpip://cam/2", "", nil, false)

	// Second call with capturedAt → should backfill.
	ts := time.Date(2024, 3, 15, 14, 25, 30, 0, time.UTC)
	rec, created, _, err := repo.FindOrCreate("IMG_002.JPG", "ptpip://cam/2", "", &ts, false)
	if err != nil {
		t.Fatalf("FindOrCreate backfill: %v", err)
	}
	if created {
		t.Error("expected created=false for backfill")
	}
	if rec.CapturedAt == nil || !rec.CapturedAt.Equal(ts) {
		t.Errorf("expected CapturedAt %v, got %v", ts, rec.CapturedAt)
	}
}

func TestImageRepo_List(t *testing.T) {
	t.Parallel()
	repo := newTestImageRepo(t)

	repo.FindOrCreate("A.JPG", "ptpip://cam/1", "", nil, false)
	repo.FindOrCreate("B.JPG", "ptpip://cam/2", "", nil, false)

	list, err := repo.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2, got %d", len(list))
	}
}

func TestImageRepo_GetByURL(t *testing.T) {
	t.Parallel()
	repo := newTestImageRepo(t)

	repo.FindOrCreate("C.JPG", "ptpip://cam/3", "", nil, false)

	rec, err := repo.GetByURL("ptpip://cam/3")
	if err != nil || rec == nil {
		t.Fatalf("GetByURL: err=%v rec=%v", err, rec)
	}
	if rec.Filename != "C.JPG" {
		t.Errorf("expected C.JPG, got %q", rec.Filename)
	}

	missing, err := repo.GetByURL("ptpip://cam/9999")
	if err != nil {
		t.Fatalf("GetByURL missing: %v", err)
	}
	if missing != nil {
		t.Error("expected nil for missing URL")
	}
}

func TestImageRepo_GetByFilename(t *testing.T) {
	t.Parallel()
	repo := newTestImageRepo(t)

	repo.FindOrCreate("D.JPG", "ptpip://cam/4", "", nil, false)

	rec, err := repo.GetByFilename("D.JPG")
	if err != nil || rec == nil {
		t.Fatalf("GetByFilename: err=%v rec=%v", err, rec)
	}

	missing, err := repo.GetByFilename("NOTEXIST.JPG")
	if err != nil {
		t.Fatalf("GetByFilename missing: %v", err)
	}
	if missing != nil {
		t.Error("expected nil for missing filename")
	}
}

func TestImageRepo_SetStatus(t *testing.T) {
	t.Parallel()
	repo := newTestImageRepo(t)

	repo.FindOrCreate("E.JPG", "ptpip://cam/5", "", nil, false)
	if err := repo.SetStatus("ptpip://cam/5", StatusUploading, ""); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	rec, _ := repo.GetByURL("ptpip://cam/5")
	if rec.Status != StatusUploading {
		t.Errorf("expected %q, got %q", StatusUploading, rec.Status)
	}
}

func TestImageRepo_SetRetryQueued(t *testing.T) {
	t.Parallel()
	repo := newTestImageRepo(t)

	repo.FindOrCreate("F.JPG", "ptpip://cam/6", "", nil, false)
	next := time.Now().Add(10 * time.Second)
	if err := repo.SetRetryQueued("ptpip://cam/6", 1, next, "download failed"); err != nil {
		t.Fatalf("SetRetryQueued: %v", err)
	}
	rec, _ := repo.GetByURL("ptpip://cam/6")
	if rec.Status != StatusQueued {
		t.Errorf("expected queued, got %q", rec.Status)
	}
	if rec.RetryCount != 1 {
		t.Errorf("expected RetryCount=1, got %d", rec.RetryCount)
	}
	if rec.LastError != "download failed" {
		t.Errorf("expected error msg, got %q", rec.LastError)
	}
}

func TestImageRepo_MarkQueued(t *testing.T) {
	t.Parallel()
	repo := newTestImageRepo(t)

	repo.FindOrCreate("G.JPG", "ptpip://cam/7", "", nil, false)
	repo.FindOrCreate("H.JPG", "ptpip://cam/8", "", nil, false)
	if err := repo.MarkQueued([]string{"ptpip://cam/7", "ptpip://cam/8"}); err != nil {
		t.Fatalf("MarkQueued: %v", err)
	}
	rec, _ := repo.GetByURL("ptpip://cam/7")
	if rec.Status != StatusQueued {
		t.Errorf("expected queued, got %q", rec.Status)
	}
}

func TestImageRepo_MarkAllDiscoveredQueued(t *testing.T) {
	t.Parallel()
	repo := newTestImageRepo(t)

	repo.FindOrCreate("I.JPG", "ptpip://cam/9", "", nil, false)
	repo.FindOrCreate("J.JPG", "ptpip://cam/10", "", nil, false)
	repo.SetStatus("ptpip://cam/9", StatusDone, "") // should not be touched

	n, err := repo.MarkAllDiscoveredQueued()
	if err != nil {
		t.Fatalf("MarkAllDiscoveredQueued: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 queued, got %d", n)
	}
	rec, _ := repo.GetByURL("ptpip://cam/10")
	if rec.Status != StatusQueued {
		t.Errorf("expected queued, got %q", rec.Status)
	}
}

func TestImageRepo_ResetQueued(t *testing.T) {
	t.Parallel()
	repo := newTestImageRepo(t)

	repo.FindOrCreate("K.JPG", "ptpip://cam/11", "", nil, false)
	repo.MarkQueued([]string{"ptpip://cam/11"})

	n, err := repo.ResetQueued()
	if err != nil {
		t.Fatalf("ResetQueued: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 reset, got %d", n)
	}
	rec, _ := repo.GetByURL("ptpip://cam/11")
	if rec.Status != StatusDiscovered {
		t.Errorf("expected discovered, got %q", rec.Status)
	}
}

func TestImageRepo_ResetStuckUploading(t *testing.T) {
	t.Parallel()
	repo := newTestImageRepo(t)

	repo.FindOrCreate("L.JPG", "ptpip://cam/12", "", nil, false)
	repo.SetStatus("ptpip://cam/12", StatusUploading, "")

	if err := repo.ResetStuckUploading(); err != nil {
		t.Fatalf("ResetStuckUploading: %v", err)
	}
	rec, _ := repo.GetByURL("ptpip://cam/12")
	if rec.Status != StatusQueued {
		t.Errorf("expected queued, got %q", rec.Status)
	}
}

func TestImageRepo_ResetFailed(t *testing.T) {
	t.Parallel()
	repo := newTestImageRepo(t)

	repo.FindOrCreate("M.JPG", "ptpip://cam/13", "", nil, false)
	repo.SetStatus("ptpip://cam/13", StatusFailed, "all retries exhausted")

	n, err := repo.ResetFailed()
	if err != nil {
		t.Fatalf("ResetFailed: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 reset, got %d", n)
	}
	rec, _ := repo.GetByURL("ptpip://cam/13")
	if rec.Status != StatusQueued {
		t.Errorf("expected queued, got %q", rec.Status)
	}
	if rec.RetryCount != 0 {
		t.Errorf("expected RetryCount reset to 0, got %d", rec.RetryCount)
	}
}

func TestImageRepo_ListReadyToRetry(t *testing.T) {
	t.Parallel()
	repo := newTestImageRepo(t)

	repo.FindOrCreate("N.JPG", "ptpip://cam/14", "", nil, false)
	past := time.Now().Add(-1 * time.Second)
	repo.SetRetryQueued("ptpip://cam/14", 1, past, "err")

	repo.FindOrCreate("O.JPG", "ptpip://cam/15", "", nil, false)
	future := time.Now().Add(60 * time.Second)
	repo.SetRetryQueued("ptpip://cam/15", 1, future, "err")

	ready, err := repo.ListReadyToRetry()
	if err != nil {
		t.Fatalf("ListReadyToRetry: %v", err)
	}
	if len(ready) != 1 {
		t.Fatalf("expected 1 ready, got %d", len(ready))
	}
	if ready[0].URL != "ptpip://cam/14" {
		t.Errorf("expected cam/14 ready, got %q", ready[0].URL)
	}
}

func TestImageRepo_ListFreshQueued(t *testing.T) {
	t.Parallel()
	repo := newTestImageRepo(t)

	repo.FindOrCreate("P.JPG", "ptpip://cam/16", "", nil, false)
	repo.MarkQueued([]string{"ptpip://cam/16"}) // fresh: retry_count=0

	repo.FindOrCreate("Q.JPG", "ptpip://cam/17", "", nil, false)
	repo.SetRetryQueued("ptpip://cam/17", 1, time.Now().Add(time.Second), "err") // not fresh

	fresh, err := repo.ListFreshQueued()
	if err != nil {
		t.Fatalf("ListFreshQueued: %v", err)
	}
	if len(fresh) != 1 || fresh[0].URL != "ptpip://cam/16" {
		t.Errorf("expected only cam/16, got %+v", fresh)
	}
}

func TestImageRepo_Counts(t *testing.T) {
	t.Parallel()
	repo := newTestImageRepo(t)

	repo.FindOrCreate("R.JPG", "ptpip://cam/18", "", nil, false)
	repo.FindOrCreate("S.JPG", "ptpip://cam/19", "", nil, false)
	repo.SetStatus("ptpip://cam/18", StatusDone, "")

	counts, err := repo.Counts()
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if counts[StatusDiscovered] != 1 {
		t.Errorf("discovered count = %d, want 1", counts[StatusDiscovered])
	}
	if counts[StatusDone] != 1 {
		t.Errorf("done count = %d, want 1", counts[StatusDone])
	}
}

func TestImageRepo_IsVideo(t *testing.T) {
	t.Parallel()
	repo := newTestImageRepo(t)

	repo.FindOrCreate("MVI_001.MOV", "ptpip://cam/20", "", nil, true)
	rec, _ := repo.GetByURL("ptpip://cam/20")
	if !rec.IsVideo {
		t.Error("expected IsVideo=true")
	}
}
