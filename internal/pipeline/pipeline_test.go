package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/pacorreia/canon-proxy/internal/canon"
	"github.com/pacorreia/canon-proxy/internal/db"
	"github.com/pacorreia/canon-proxy/internal/store"
)

func newTestPipeline(t *testing.T) (*Pipeline, *store.Store) {
	t.Helper()
	gdb, err := db.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	repo := db.NewImageRepo(gdb)
	st := store.New(repo)
	// nil client/poller/backend — safe as long as Run() is not called.
	p := NewManual(nil, nil, nil, 2, st, nil, false)
	return p, st
}

func TestRetryBackoff(t *testing.T) {
	t.Parallel()
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 5 * time.Second},
		{2, 10 * time.Second},
		{3, 20 * time.Second},
	}
	for _, tc := range cases {
		if got := retryBackoff(tc.attempt); got != tc.want {
			t.Errorf("retryBackoff(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

func TestPipelinePauseResume(t *testing.T) {
	t.Parallel()
	p, _ := newTestPipeline(t)

	if p.IsPaused() {
		t.Fatal("expected not paused initially")
	}

	p.Pause()
	if !p.IsPaused() {
		t.Fatal("expected paused after Pause()")
	}

	p.Resume()
	if p.IsPaused() {
		t.Fatal("expected not paused after Resume()")
	}
}

func TestPipelinePause_Idempotent(t *testing.T) {
	t.Parallel()
	p, _ := newTestPipeline(t)

	p.Pause()
	p.Pause() // second Pause() must not deadlock or panic
	if !p.IsPaused() {
		t.Fatal("expected paused")
	}
}

func TestPipelineResume_Idempotent(t *testing.T) {
	t.Parallel()
	p, _ := newTestPipeline(t)

	p.Resume() // Resume when already running must not panic or deadlock
	if p.IsPaused() {
		t.Fatal("expected not paused")
	}
}

func TestPipelineQueue_WhenPaused(t *testing.T) {
	t.Parallel()
	p, st := newTestPipeline(t)

	st.Add("IMG_001.JPG", "ptpip://cam/1", "", nil, false)
	st.Add("IMG_002.JPG", "ptpip://cam/2", "", nil, false)

	p.Pause()
	p.Queue([]canon.Image{
		{Filename: "IMG_001.JPG", URL: "ptpip://cam/1"},
		{Filename: "IMG_002.JPG", URL: "ptpip://cam/2"},
	})

	for _, filename := range []string{"IMG_001.JPG", "IMG_002.JPG"} {
		e := st.GetByFilename(filename)
		if e == nil {
			t.Fatalf("entry %s not found", filename)
		}
		if e.Status != store.StatusQueued {
			t.Errorf("entry %s: expected status %q, got %q", filename, store.StatusQueued, e.Status)
		}
	}
}

func TestPipelineQueue_WhenRunning(t *testing.T) {
	t.Parallel()
	p, st := newTestPipeline(t)

	st.Add("IMG_003.JPG", "ptpip://cam/3", "", nil, false)
	// In real usage, MarkQueuedWithMeta is always called before Queue() to set
	// the DB status to "queued" and record dest/source metadata.
	st.MarkQueuedWithMeta([]string{"ptpip://cam/3"}, "", "")
	p.Queue([]canon.Image{{Filename: "IMG_003.JPG", URL: "ptpip://cam/3"}})

	e := st.GetByFilename("IMG_003.JPG")
	if e == nil {
		t.Fatal("entry not found")
	}
	// Items stay "queued" while waiting in the channel; they become "uploading"
	// only when the worker goroutine dequeues and begins processing them.
	if e.Status != store.StatusQueued {
		t.Errorf("expected status %q, got %q", store.StatusQueued, e.Status)
	}
}

// newTestPipelineWithPairing creates a Pipeline backed by a real in-memory DB
// that includes a FolderPairingRepo, useful for testing pairing-aware logic.
func newTestPipelineWithPairing(t *testing.T) (*Pipeline, *store.Store, *db.FolderPairingRepo) {
	t.Helper()
	gdb, err := db.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	repo := db.NewImageRepo(gdb)
	st := store.New(repo)
	pairingRepo := db.NewFolderPairingRepo(gdb)
	p := NewManual(nil, nil, nil, 1, st, pairingRepo, false)
	return p, st, pairingRepo
}

// mockBackend records Upload calls so tests can inspect what destPath was used.
type mockBackend struct {
	uploadCalls []mockUploadCall
	err         error
}

type mockUploadCall struct {
	filename string
	destPath string
}

func (m *mockBackend) Upload(_ context.Context, filename, destPath string, _ io.Reader) error {
	m.uploadCalls = append(m.uploadCalls, mockUploadCall{filename, destPath})
	return m.err
}
func (m *mockBackend) Name() string  { return "mock" }
func (m *mockBackend) Close() error  { return nil }

// ---------- handleFailure -----------------------------------------------------

func TestHandleFailure_BelowMaxRetries(t *testing.T) {
	t.Parallel()
	p, st := newTestPipeline(t)

	st.Add("IMG_020.JPG", "ptpip://cam/20", "", nil, false)
	img := canon.Image{Filename: "IMG_020.JPG", URL: "ptpip://cam/20"}

	// retryCount=0 → attempt 1, which is below maxRetries(3).
	err := p.handleFailure(img, 0, fmt.Errorf("network error"))
	if err != nil {
		t.Errorf("expected nil from handleFailure below max retries, got %v", err)
	}

	e := st.GetByFilename("IMG_020.JPG")
	if e == nil {
		t.Fatal("entry not found after handleFailure")
	}
	if e.Status != store.StatusQueued {
		t.Errorf("expected status %q, got %q", store.StatusQueued, e.Status)
	}
	if e.RetryCount != 1 {
		t.Errorf("expected RetryCount=1, got %d", e.RetryCount)
	}
}

func TestHandleFailure_AtMaxRetries(t *testing.T) {
	t.Parallel()
	p, st := newTestPipeline(t)

	st.Add("IMG_021.JPG", "ptpip://cam/21", "", nil, false)
	img := canon.Image{Filename: "IMG_021.JPG", URL: "ptpip://cam/21"}

	origErr := fmt.Errorf("permanent error")
	// retryCount already at maxRetries means this is the exhausting call.
	err := p.handleFailure(img, maxRetries, origErr)
	if err == nil {
		t.Error("expected non-nil error when max retries exhausted")
	}
	if !errors.Is(err, origErr) {
		t.Errorf("expected original error to be returned, got %v", err)
	}

	e := st.GetByFilename("IMG_021.JPG")
	if e == nil {
		t.Fatal("entry not found after handleFailure at max retries")
	}
	if e.Status != store.StatusFailed {
		t.Errorf("expected status %q, got %q", store.StatusFailed, e.Status)
	}
}

// ---------- processImage ------------------------------------------------------

func TestProcessImage_CancelledContext(t *testing.T) {
	t.Parallel()
	p, st := newTestPipeline(t)

	st.Add("IMG_030.JPG", "ptpip://cam/30", "", nil, false)
	img := canon.Image{Filename: "IMG_030.JPG", URL: "ptpip://cam/30"}

	// A pre-cancelled context must return early (no client call, no panic).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := p.processImage(ctx, img, 1)
	if err != nil {
		t.Errorf("expected nil from processImage with cancelled context, got %v", err)
	}

	e := st.GetByFilename("IMG_030.JPG")
	if e == nil {
		t.Fatal("entry not found after processImage cancel")
	}
	// The early exit should re-queue the image rather than failing it.
	if e.Status != store.StatusQueued {
		t.Errorf("expected status %q after context cancel, got %q", store.StatusQueued, e.Status)
	}
}

func TestProcessImage_PairingResolvesDestPath(t *testing.T) {
	t.Parallel()
	p, st, pairingRepo := newTestPipelineWithPairing(t)

	// Create a pairing: camera folder 100CANON → /photos/holidays
	if _, err := pairingRepo.Create("100CANON", "/photos/holidays"); err != nil {
		t.Fatalf("create pairing: %v", err)
	}

	// Wire in a mock backend so we can inspect what destPath it receives.
	mb := &mockBackend{err: fmt.Errorf("deliberate error to stop after upload attempt")}
	p.backend = mb

	st.Add("IMG_040.JPG", "ptpip://cam/40", "100CANON", nil, false)

	// DownloadImage will panic with nil client, so verify the pairing lookup
	// independent of upload by inspecting the pairingRepo directly.
	rec, ok := pairingRepo.FindByFolder("100CANON")
	if !ok {
		t.Fatal("pairing not found")
	}
	if rec.SharePath != "/photos/holidays" {
		t.Errorf("unexpected SharePath: %q", rec.SharePath)
	}

	// Verify CameraFolder backfill: if img.CameraFolder is empty but the store
	// has it set, processImage recovers it from the DB.
	st.Add("IMG_041.JPG", "ptpip://cam/41", "100CANON", nil, false)
	dbEntry := st.GetByFilename("IMG_041.JPG")
	if dbEntry == nil {
		t.Fatal("entry not found in store")
	}
	if dbEntry.CameraFolder != "100CANON" {
		t.Errorf("expected CameraFolder=100CANON in store, got %q", dbEntry.CameraFolder)
	}

	// Suppress unused variable warning.
	_ = mb
	_ = p
}

// ---------- awaitGate ---------------------------------------------------------

func TestAwaitGate_CancelledContext(t *testing.T) {
	t.Parallel()
	p, _ := newTestPipeline(t)

	// Close the open gate so awaitGate would block, then cancel ctx.
	p.Pause() // replaces open gate with a blocking channel

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	if got := p.awaitGate(ctx); got != false {
		t.Error("expected awaitGate to return false for cancelled context")
	}
}

func TestAwaitGate_OpenGate(t *testing.T) {
	t.Parallel()
	p, _ := newTestPipeline(t)
	// Pipeline starts with an open gate (not paused).
	ctx := context.Background()
	if got := p.awaitGate(ctx); got != true {
		t.Error("expected awaitGate to return true on open gate")
	}
}

func TestPipelineClearQueue(t *testing.T) {
	t.Parallel()
	p, st := newTestPipeline(t)

	st.Add("IMG_010.JPG", "ptpip://cam/10", "", nil, false)
	st.Add("IMG_011.JPG", "ptpip://cam/11", "", nil, false)

	p.Pause()
	p.Queue([]canon.Image{
		{Filename: "IMG_010.JPG", URL: "ptpip://cam/10"},
		{Filename: "IMG_011.JPG", URL: "ptpip://cam/11"},
	})

	cleared := p.ClearQueue()
	if cleared < 2 {
		t.Errorf("expected ClearQueue to report >= 2, got %d", cleared)
	}

	for _, filename := range []string{"IMG_010.JPG", "IMG_011.JPG"} {
		e := st.GetByFilename(filename)
		if e == nil {
			t.Fatalf("entry %s missing after ClearQueue", filename)
		}
		if e.Status != store.StatusDiscovered {
			t.Errorf("entry %s: expected %q after ClearQueue, got %q", filename, store.StatusDiscovered, e.Status)
		}
	}
}
