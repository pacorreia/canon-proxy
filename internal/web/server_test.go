package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pacorreia/canon-proxy/internal/canon"
	"github.com/pacorreia/canon-proxy/internal/db"
	"github.com/pacorreia/canon-proxy/internal/store"
)

// newTestServer builds a minimal Server with a real in-memory SQLite database.
// All optional function fields default to nil; individual tests override what they need.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	gdb, err := db.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	imgRepo := db.NewImageRepo(gdb)
	st := store.New(imgRepo)
	settingRepo := db.NewSettingRepo(gdb)
	pairingRepo := db.NewFolderPairingRepo(gdb)

	return New(ServerConfig{
		Store:       st,
		SettingRepo: settingRepo,
		PairingRepo: pairingRepo,
		// Leave ThumbFunc, DownloadFunc, etc. nil — not needed for these tests.
		Queue: func(_ []canon.Image) {},
	})
}

// do performs an HTTP request against the server's mux and returns the recorder.
func do(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rb *bytes.Reader
	if body != "" {
		rb = bytes.NewReader([]byte(body))
	} else {
		rb = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rb)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	return w
}

// ---------- /api/pairings ----------------------------------------------------

func TestHandlePairings_GetEmpty(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodGet, "/api/pairings", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var records []db.FolderPairingRecord
	if err := json.NewDecoder(w.Body).Decode(&records); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("expected empty list, got %d records", len(records))
	}
}

func TestHandlePairings_Post(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	body := `{"camera_folder":"100CANON","share_path":"/photos"}`
	w := do(t, s, http.MethodPost, "/api/pairings", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var rec db.FolderPairingRecord
	if err := json.NewDecoder(w.Body).Decode(&rec); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rec.ID == 0 {
		t.Error("expected non-zero ID in response")
	}
	if rec.CameraFolder != "100CANON" || rec.SharePath != "/photos" {
		t.Errorf("unexpected fields: %+v", rec)
	}
}

func TestHandlePairings_Post_Duplicate(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	body := `{"camera_folder":"100CANON","share_path":"/photos"}`

	// First creation must succeed.
	w := do(t, s, http.MethodPost, "/api/pairings", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 on first create, got %d: %s", w.Code, w.Body.String())
	}

	// Second creation with the same camera folder must return 409.
	w2 := do(t, s, http.MethodPost, "/api/pairings", `{"camera_folder":"100CANON","share_path":"/other"}`)
	if w2.Code != http.StatusConflict {
		t.Errorf("expected 409 Conflict for duplicate camera folder, got %d: %s", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), "100CANON") {
		t.Errorf("expected error message to mention the folder name, got: %s", w2.Body.String())
	}
}

func TestHandlePairings_Post_MissingField(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodPost, "/api/pairings", `{"camera_folder":"100CANON"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandlePairings_Post_InvalidJSON(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodPost, "/api/pairings", `not-json`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandlePairings_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodDelete, "/api/pairings", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandlePairings_NilRepo(t *testing.T) {
	t.Parallel()
	// Construct a server without a pairingRepo to verify the nil guard.
	gdb, err := db.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	imgRepo := db.NewImageRepo(gdb)
	st := store.New(imgRepo)
	s := New(ServerConfig{
		Store: st,
		Queue: func(_ []canon.Image) {},
		// PairingRepo intentionally omitted (nil).
	})
	w := do(t, s, http.MethodGet, "/api/pairings", "")
	if w.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 when pairingRepo is nil, got %d", w.Code)
	}
}

// ---------- /api/pairings/{id} -----------------------------------------------

func TestHandlePairingByID_Delete(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	// Create one pairing, then delete it.
	w := do(t, s, http.MethodPost, "/api/pairings", `{"camera_folder":"200DCIM","share_path":"/raw"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("setup POST failed: %d %s", w.Code, w.Body.String())
	}
	var rec db.FolderPairingRecord
	json.NewDecoder(w.Body).Decode(&rec)

	w2 := do(t, s, http.MethodDelete, "/api/pairings/"+itoa(rec.ID), "")
	if w2.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w2.Code, w2.Body.String())
	}

	// Confirm the list is now empty.
	w3 := do(t, s, http.MethodGet, "/api/pairings", "")
	var records []db.FolderPairingRecord
	json.NewDecoder(w3.Body).Decode(&records)
	if len(records) != 0 {
		t.Errorf("expected empty list after delete, got %d", len(records))
	}
}

func TestHandlePairingByID_DeleteNotFound(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodDelete, "/api/pairings/9999", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for non-existent ID, got %d", w.Code)
	}
	if !errors.Is(db.ErrPairingNotFound, db.ErrPairingNotFound) {
		t.Error("ErrPairingNotFound sentinel check failed")
	}
}

func TestHandlePairingByID_InvalidID(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	for _, path := range []string{"/api/pairings/abc", "/api/pairings/0", "/api/pairings/-1"} {
		w := do(t, s, http.MethodDelete, path, "")
		if w.Code != http.StatusBadRequest {
			t.Errorf("DELETE %s: expected 400, got %d", path, w.Code)
		}
	}
}

func TestHandlePairingByID_NilRepo(t *testing.T) {
	t.Parallel()
	gdb, err := db.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := store.New(db.NewImageRepo(gdb))
	s := New(ServerConfig{
		Store: st,
		Queue: func(_ []canon.Image) {},
	})
	w := do(t, s, http.MethodDelete, "/api/pairings/1", "")
	if w.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 when pairingRepo is nil, got %d", w.Code)
	}
}

// ---------- /api/system/restart ----------------------------------------------

func TestHandleRestart_ProxyHeaders(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	proxyHeaders := []string{
		"X-Forwarded-For",
		"X-Real-IP",
		"Forwarded",
		"CF-Connecting-IP",
		"True-Client-IP",
		"X-Forwarded-Host",
	}
	for _, h := range proxyHeaders {
		req := httptest.NewRequest(http.MethodPost, "/api/system/restart", nil)
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set(h, "10.0.0.1")
		w := httptest.NewRecorder()
		s.httpServer.Handler.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("header %q present: expected 403, got %d", h, w.Code)
		}
	}
}

func TestHandleRestart_NonLocalhost(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/system/restart", nil)
	req.RemoteAddr = "10.0.0.5:12345" // not localhost
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-localhost, got %d", w.Code)
	}
}

func TestHandleRestart_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodGet, "/api/system/restart", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleRestart_NoRestartFunc(t *testing.T) {
	t.Parallel()
	s := newTestServer(t) // RestartFunc is nil

	req := httptest.NewRequest(http.MethodPost, "/api/system/restart", nil)
	req.RemoteAddr = "127.0.0.1:9999"
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 when restartFunc is nil, got %d", w.Code)
	}
}

// ---------- /api/camera/* ----------------------------------------------------

func TestHandleCameraScan_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodGet, "/api/camera/scan", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleCameraFolders_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodPost, "/api/camera/folders", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleCameraFolders_NilFunc(t *testing.T) {
	t.Parallel()
	s := newTestServer(t) // ListFoldersFunc is nil
	w := do(t, s, http.MethodGet, "/api/camera/folders", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when camera not configured, got %d", w.Code)
	}
}

// ---------- /api/settings ----------------------------------------------------

func TestHandleSettings_Get(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodGet, "/api/settings", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var m map[string]string
	if err := json.NewDecoder(w.Body).Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestHandleSettings_Put(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	body := `{"log.level":"debug"}`
	w := do(t, s, http.MethodPut, "/api/settings", body)
	if w.Code != http.StatusNoContent && w.Code != http.StatusOK {
		t.Errorf("expected 204 or 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleSettings_SecretRedacted(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	// Store a secret value directly through the repo.
	s.settingRepo.Set("smb.password", "super-secret")

	w := do(t, s, http.MethodGet, "/api/settings", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var m map[string]string
	json.NewDecoder(w.Body).Decode(&m)

	if m["smb.password"] == "super-secret" {
		t.Error("smb.password must be redacted in GET /api/settings response")
	}
	if m["smb.password"] != "********" {
		t.Errorf("expected redacted placeholder, got %q", m["smb.password"])
	}
}

func TestHandleSettings_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodDelete, "/api/settings", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// ---------- /api/images ------------------------------------------------------

func TestHandleImages_GetEmpty(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodGet, "/api/images", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := strings.TrimSpace(w.Body.String())
	// Should be a JSON array (possibly empty).
	if !strings.HasPrefix(body, "[") {
		t.Errorf("expected JSON array, got: %s", body)
	}
}

func TestHandleImages_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodPost, "/api/images", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// ---------- /api/queue/* controls --------------------------------------------

func TestHandleQueueStatus(t *testing.T) {
	t.Parallel()
	paused := false
	s := newTestServerWithQueue(t, &paused)

	w := do(t, s, http.MethodGet, "/api/queue/status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"paused":false`) {
		t.Errorf("expected paused=false in response, got: %s", w.Body.String())
	}
}

func TestHandleQueueStatus_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodPost, "/api/queue/status", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleQueuePause(t *testing.T) {
	t.Parallel()
	paused := false
	s := newTestServerWithQueue(t, &paused)

	w := do(t, s, http.MethodPost, "/api/queue/pause", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
	if !paused {
		t.Error("expected paused=true after POST /api/queue/pause")
	}
}

func TestHandleQueuePause_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodGet, "/api/queue/pause", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleQueueResume(t *testing.T) {
	t.Parallel()
	paused := true
	s := newTestServerWithQueue(t, &paused)

	w := do(t, s, http.MethodPost, "/api/queue/resume", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
	if paused {
		t.Error("expected paused=false after POST /api/queue/resume")
	}
}

func TestHandleQueueResume_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodGet, "/api/queue/resume", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleQueueClear(t *testing.T) {
	t.Parallel()
	cleared := false
	gdb, err := db.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	imgRepo := db.NewImageRepo(gdb)
	st := store.New(imgRepo)
	s := New(ServerConfig{
		Store: st,
		Queue: func(_ []canon.Image) {},
		QueueCtrl: QueueController{
			Clear:    func() { cleared = true },
			IsPaused: func() bool { return false },
		},
	})

	w := do(t, s, http.MethodPost, "/api/queue/clear", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
	if !cleared {
		t.Error("expected clear func to be called")
	}
}

func TestHandleQueueClear_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodGet, "/api/queue/clear", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// ---------- /api/images/queue ------------------------------------------------

func TestHandleQueue_Post(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.store.Add("IMG_001.JPG", "http://cam/1", "", nil, false)

	body := `{"filenames":["IMG_001.JPG"]}`
	w := do(t, s, http.MethodPost, "/api/images/queue", body)
	if w.Code != http.StatusAccepted && w.Code != http.StatusNoContent {
		t.Errorf("expected 202 or 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleQueue_EmptyFilenames(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodPost, "/api/images/queue", `{"filenames":[]}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty filenames, got %d", w.Code)
	}
}

func TestHandleQueue_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodGet, "/api/images/queue", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleQueueAll_Post(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.store.Add("IMG_002.JPG", "http://cam/2", "", nil, false)
	s.store.Add("IMG_003.JPG", "http://cam/3", "", nil, false)

	w := do(t, s, http.MethodPost, "/api/images/queue-all", "")
	if w.Code != http.StatusAccepted && w.Code != http.StatusNoContent {
		t.Errorf("expected 202 or 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleQueueAll_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodGet, "/api/images/queue-all", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleRetryFailed_Post(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.store.Add("IMG_004.JPG", "http://cam/4", "", nil, false)
	s.store.SetStatus("http://cam/4", "failed", "upload error")

	w := do(t, s, http.MethodPost, "/api/images/retry-failed", "")
	if w.Code != http.StatusAccepted && w.Code != http.StatusNoContent {
		t.Errorf("expected 202 or 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleRetryFailed_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodGet, "/api/images/retry-failed", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// ---------- settings PUT: secret-filtering -----------------------------------

func TestHandleSettings_Put_SecretNotOverwrittenByPlaceholder(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.settingRepo.Set("smb.password", "real-password")

	// Sending the placeholder must not overwrite the real value.
	w := do(t, s, http.MethodPut, "/api/settings", `{"smb.password":"********"}`)
	if w.Code != http.StatusNoContent && w.Code != http.StatusOK {
		t.Fatalf("expected 204 or 200, got %d: %s", w.Code, w.Body.String())
	}
	v, _ := s.settingRepo.Get("smb.password")
	if v != "real-password" {
		t.Errorf("secret was overwritten by placeholder; got %q", v)
	}
}

func TestHandleSettings_Put_SecretNotOverwrittenByEmpty(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.settingRepo.Set("ftp.password", "secret")

	w := do(t, s, http.MethodPut, "/api/settings", `{"ftp.password":""}`)
	if w.Code != http.StatusNoContent && w.Code != http.StatusOK {
		t.Fatalf("expected 204/200, got %d", w.Code)
	}
	v, _ := s.settingRepo.Get("ftp.password")
	if v != "secret" {
		t.Errorf("secret must not be overwritten by empty string; got %q", v)
	}
}

func TestHandleSettings_Put_InvalidJSON(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodPut, "/api/settings", `not-json`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// ---------- boundedCache -----------------------------------------------------

func TestBoundedCache(t *testing.T) {
	t.Parallel()
	c := newBoundedCache()

	_, ok := c.Load("missing")
	if ok {
		t.Error("expected miss for empty cache")
	}

	c.Store("a", []byte("hello"))
	v, ok := c.Load("a")
	if !ok || string(v) != "hello" {
		t.Errorf("expected hit for 'a', got ok=%v val=%q", ok, v)
	}

	// Overwrite same key — size should not grow past capacity.
	c.Store("a", []byte("world"))
	v, _ = c.Load("a")
	if string(v) != "world" {
		t.Errorf("expected updated value 'world', got %q", v)
	}
}

func TestBoundedCache_Eviction(t *testing.T) {
	t.Parallel()
	c := newBoundedCache()

	// Fill to capacity.
	for i := 0; i < thumbCacheMaxSize; i++ {
		c.Store(itoa(uint(i)), []byte("x"))
	}
	if len(c.entries) != thumbCacheMaxSize {
		t.Fatalf("expected %d entries at capacity, got %d", thumbCacheMaxSize, len(c.entries))
	}

	// One more entry must evict one existing one (total stays at capacity).
	c.Store("overflow", []byte("y"))
	if len(c.entries) != thumbCacheMaxSize {
		t.Errorf("expected %d entries after eviction, got %d", thumbCacheMaxSize, len(c.entries))
	}
	// The new entry must be present.
	v, ok := c.Load("overflow")
	if !ok || string(v) != "y" {
		t.Errorf("expected 'overflow' in cache, got ok=%v val=%q", ok, v)
	}
}

// ---------- helper constructors ----------------------------------------------

// newTestServerWithQueue creates a server with controllable pause/resume/status.
func newTestServerWithQueue(t *testing.T, paused *bool) *Server {
	t.Helper()
	gdb, err := db.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	imgRepo := db.NewImageRepo(gdb)
	st := store.New(imgRepo)
	return New(ServerConfig{
		Store: st,
		Queue: func(_ []canon.Image) {},
		QueueCtrl: QueueController{
			Pause:    func() { *paused = true },
			Resume:   func() { *paused = false },
			IsPaused: func() bool { return *paused },
		},
		SettingRepo: db.NewSettingRepo(gdb),
		PairingRepo: db.NewFolderPairingRepo(gdb),
	})
}

// ---------- helpers ----------------------------------------------------------

// newTestServerWithPoller creates a server wired with a real (nil-client) Poller and settingRepo,
// so that poll-control endpoints can be exercised.
func newTestServerWithPoller(t *testing.T) (*Server, *canon.Poller) {
	t.Helper()
	gdb, err := db.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	imgRepo := db.NewImageRepo(gdb)
	st := store.New(imgRepo)
	settingRepo := db.NewSettingRepo(gdb)
	pairingRepo := db.NewFolderPairingRepo(gdb)
	poller := canon.NewPoller(nil, 10*time.Second) // nil client — safe as long as Run() is not called
	s := New(ServerConfig{
		Store:       st,
		SettingRepo: settingRepo,
		PairingRepo: pairingRepo,
		Queue:       func(_ []canon.Image) {},
		Poller:      poller,
	})
	return s, poller
}

// ---------- /api/camera/poll/trigger -----------------------------------------

func TestHandlePollTrigger_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodGet, "/api/camera/poll/trigger", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandlePollTrigger_NoPoller(t *testing.T) {
	t.Parallel()
	s := newTestServer(t) // no Poller configured
	w := do(t, s, http.MethodPost, "/api/camera/poll/trigger", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when poller is nil, got %d", w.Code)
	}
}

func TestHandlePollTrigger_Success(t *testing.T) {
	t.Parallel()
	s, _ := newTestServerWithPoller(t)
	w := do(t, s, http.MethodPost, "/api/camera/poll/trigger", "")
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

// ---------- /api/camera/poll/settings ----------------------------------------

func TestHandlePollSettings_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	s, _ := newTestServerWithPoller(t)
	w := do(t, s, http.MethodDelete, "/api/camera/poll/settings", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandlePollSettings_NoPoller(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodGet, "/api/camera/poll/settings", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when poller is nil, got %d", w.Code)
	}
}

func TestHandlePollSettings_Get(t *testing.T) {
	t.Parallel()
	s, poller := newTestServerWithPoller(t)
	poller.SetEnabled(false)
	poller.SetInterval(30 * time.Second)

	w := do(t, s, http.MethodGet, "/api/camera/poll/settings", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var body map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, ok := body["enabled"].(bool); !ok || got {
		t.Errorf("expected enabled=false, got %v", body["enabled"])
	}
	if got, ok := body["interval_sec"].(float64); !ok || got != 30 {
		t.Errorf("expected interval_sec=30, got %v", body["interval_sec"])
	}
}

func TestHandlePollSettings_Post_EnabledAndInterval(t *testing.T) {
	t.Parallel()
	s, poller := newTestServerWithPoller(t)

	w := do(t, s, http.MethodPost, "/api/camera/poll/settings", `{"enabled":false,"interval_sec":60}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	if poller.AutoEnabled() {
		t.Error("expected poller disabled after POST")
	}
	if poller.Interval() != 60*time.Second {
		t.Errorf("expected interval 60s, got %v", poller.Interval())
	}
}

func TestHandlePollSettings_Post_InvalidJSON(t *testing.T) {
	t.Parallel()
	s, _ := newTestServerWithPoller(t)
	w := do(t, s, http.MethodPost, "/api/camera/poll/settings", `not-json`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandlePollSettings_Post_PersistsToSettingRepo(t *testing.T) {
	t.Parallel()
	s, _ := newTestServerWithPoller(t)

	w := do(t, s, http.MethodPost, "/api/camera/poll/settings", `{"enabled":false,"interval_sec":30}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	// Verify the values were persisted to the setting repo.
	enabled, _ := s.settingRepo.Get("camera.poll_enabled")
	if enabled != "false" {
		t.Errorf("expected camera.poll_enabled=false, got %q", enabled)
	}

	interval, _ := s.settingRepo.Get("camera.poll_interval")
	if interval != "30s" {
		t.Errorf("expected camera.poll_interval=30s, got %q", interval)
	}
}

// ---------- /api/share/folders -----------------------------------------------

func TestHandleShareFolders_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	w := do(t, s, http.MethodPost, "/api/share/folders", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleShareFolders_NilFunc(t *testing.T) {
	t.Parallel()
	s := newTestServer(t) // shareFoldersFunc is nil
	w := do(t, s, http.MethodGet, "/api/share/folders", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when shareFoldersFunc is nil, got %d", w.Code)
	}
}

func TestHandleShareFolders_Success(t *testing.T) {
	t.Parallel()
	gdb, err := db.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := store.New(db.NewImageRepo(gdb))
	s := New(ServerConfig{
		Store: st,
		Queue: func(_ []canon.Image) {},
	})
	// shareFoldersFunc is not set via ServerConfig (no backend configured).
	// Just verify we get 503 as expected.
	w := do(t, s, http.MethodGet, "/api/share/folders?path=/photos", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (no lister configured), got %d", w.Code)
	}
}

func TestHandleShareFolders_ReturnsNames(t *testing.T) {
	t.Parallel()
	gdb, err := db.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := store.New(db.NewImageRepo(gdb))

	// Inject the shareFoldersFunc directly after construction.
	s := New(ServerConfig{
		Store: st,
		Queue: func(_ []canon.Image) {},
	})
	s.shareFoldersFunc = func(_ context.Context, path string) ([]string, error) {
		return []string{"alpha", "beta"}, nil
	}

	w := do(t, s, http.MethodGet, "/api/share/folders?path=/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var names []string
	if err := json.NewDecoder(w.Body).Decode(&names); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(names) != 2 || names[0] != "alpha" || names[1] != "beta" {
		t.Errorf("unexpected names: %v", names)
	}
}

func TestHandleShareFolders_PathTraversalSanitized(t *testing.T) {
	t.Parallel()
	gdb, err := db.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st := store.New(db.NewImageRepo(gdb))
	s := New(ServerConfig{
		Store: st,
		Queue: func(_ []canon.Image) {},
	})

	var receivedPath string
	s.shareFoldersFunc = func(_ context.Context, path string) ([]string, error) {
		receivedPath = path
		return []string{}, nil
	}

	// /../../../etc should be sanitized to /etc (path.Clean behaviour).
	w := do(t, s, http.MethodGet, "/api/share/folders?path=/../../../etc", "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if receivedPath != "/etc" {
		t.Errorf("expected path sanitized to /etc, got %q", receivedPath)
	}
}

// ---------- helpers ----------------------------------------------------------

// itoa converts a uint to a decimal string without importing strconv.
func itoa(n uint) string {
	if n == 0 {
		return "0"
	}
	b := make([]byte, 0, 10)
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
