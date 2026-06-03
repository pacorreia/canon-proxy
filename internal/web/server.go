package web

import (
	"archive/zip"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pacorreia/canon-proxy/internal/canon"
	"github.com/pacorreia/canon-proxy/internal/db"
	"github.com/pacorreia/canon-proxy/internal/store"
)

//go:embed static
var staticFiles embed.FS

// QueueFunc is called when the user requests images to be queued for upload.
type QueueFunc func(images []canon.Image)

// ThumbFunc fetches the thumbnail for the image identified by imageURL.
type ThumbFunc func(ctx context.Context, imageURL string) (io.ReadCloser, error)

// DownloadFunc fetches the full-resolution image data from the camera.
type DownloadFunc func(ctx context.Context, image canon.Image) (io.ReadCloser, error)

// QueueController exposes pipeline pause/resume/clear controls to the web layer.
type QueueController struct {
	Pause    func()
	Resume   func()
	Clear    func()
	IsPaused func() bool
}

// sseClient is a single SSE subscriber.
type sseClient struct {
	ch chan struct{}
}

// logClient is a subscriber to the live log stream.
type logClient struct {
	ch chan string
}

// LogBroadcaster is an io.Writer that fans log lines out to all connected /api/logs SSE clients.
// Wire it as a tee on log.SetOutput so every log.Printf line reaches the browser.
type LogBroadcaster struct {
	mu      sync.Mutex
	clients map[*logClient]struct{}
	buf     []string // ring buffer of recent lines
}

// NewLogBroadcaster returns an initialised LogBroadcaster.
func NewLogBroadcaster() *LogBroadcaster { return &LogBroadcaster{clients: make(map[*logClient]struct{})} }

const logRingSize = 200

// Write implements io.Writer. Each call is treated as one log line.
func (lb *LogBroadcaster) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n")
	lb.mu.Lock()
	if len(lb.buf) >= logRingSize {
		lb.buf = lb.buf[1:]
	}
	lb.buf = append(lb.buf, line)
	for c := range lb.clients {
		select {
		case c.ch <- line:
		default: // slow client: drop the line rather than block
		}
	}
	lb.mu.Unlock()
	return len(p), nil
}

func (lb *LogBroadcaster) subscribe() (*logClient, []string) {
	c := &logClient{ch: make(chan string, 64)}
	lb.mu.Lock()
	snap := make([]string, len(lb.buf))
	copy(snap, lb.buf)
	lb.clients[c] = struct{}{}
	lb.mu.Unlock()
	return c, snap
}

func (lb *LogBroadcaster) unsubscribe(c *logClient) {
	lb.mu.Lock()
	delete(lb.clients, c)
	lb.mu.Unlock()
}

// boundedCache is a simple thread-safe cache with a fixed maximum number of
// entries. When the cache is full, one entry is evicted at random before the
// new entry is inserted. This prevents unbounded memory growth in long-running
// deployments with large SD cards.
const thumbCacheMaxSize = 512

type boundedCache struct {
	mu      sync.Mutex
	entries map[string][]byte
}

func newBoundedCache() *boundedCache {
	return &boundedCache{entries: make(map[string][]byte)}
}

func (c *boundedCache) Load(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.entries[key]
	return v, ok
}

func (c *boundedCache) Store(key string, value []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists && len(c.entries) >= thumbCacheMaxSize {
		// Evict an arbitrary entry to stay within the size limit.
		for k := range c.entries {
			delete(c.entries, k)
			break
		}
	}
	c.entries[key] = value
}

// Server is the web UI HTTP server.
type Server struct {
	store        *store.Store
	thumbFunc    ThumbFunc
	thumbCache   *boundedCache // filename -> thumbnail bytes; evicts an arbitrary entry when capacity is reached
	downloadFunc DownloadFunc
	queue        QueueFunc
	queueCtrl    QueueController
	settingRepo  *db.SettingRepo
	restartFunc  func() // called to restart the process
	logBcast     *LogBroadcaster
	httpServer   *http.Server

	sseMu   sync.Mutex
	sseSubs map[*sseClient]struct{}
}

// New creates a Server and registers all routes.
func New(st *store.Store, thumbFunc ThumbFunc, downloadFunc DownloadFunc, queue QueueFunc, listen string, qc QueueController, settingRepo *db.SettingRepo, restartFunc func(), logBcast *LogBroadcaster) *Server {
	s := &Server{
		store:        st,
		thumbFunc:    thumbFunc,
		thumbCache:   newBoundedCache(),
		downloadFunc: downloadFunc,
		queue:        queue,
		queueCtrl:    qc,
		settingRepo:  settingRepo,
		restartFunc:  restartFunc,
		logBcast:     logBcast,
		sseSubs:      make(map[*sseClient]struct{}),
	}

	st.SetOnChange(s.broadcast)

	mux := http.NewServeMux()

	// Static UI
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic("web: failed to sub static embed: " + err.Error())
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	// Image API
	mux.HandleFunc("/api/images", s.handleImages)
	mux.HandleFunc("/api/images/queue", s.handleQueue)
	mux.HandleFunc("/api/images/queue-all", s.handleQueueAll)
	mux.HandleFunc("/api/images/retry-failed", s.handleRetryFailed)
	mux.HandleFunc("/api/images/download", s.handleDownloadZip)
	mux.HandleFunc("/api/events", s.handleSSE)
	// Per-image routes: /api/images/{filename}/thumb and /api/images/{filename}/download
	mux.HandleFunc("/api/images/", s.handleImageFile)

	// Queue controls
	mux.HandleFunc("/api/queue/status", s.handleQueueStatus)
	mux.HandleFunc("/api/queue/pause", s.handleQueuePause)
	mux.HandleFunc("/api/queue/resume", s.handleQueueResume)
	mux.HandleFunc("/api/queue/clear", s.handleQueueClear)

	// Settings
	mux.HandleFunc("/api/settings", s.handleSettings)

	// Camera discovery
	mux.HandleFunc("/api/camera/scan", s.handleCameraScan)

	// Live log stream
	mux.HandleFunc("/api/logs", s.handleLogStream)

	// System
	mux.HandleFunc("/api/system/restart", s.handleRestart)

	s.httpServer = &http.Server{
		Addr:         listen,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	return s
}

// Start begins serving and blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) {
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.httpServer.Shutdown(shutCtx); err != nil {
			log.Printf("level=warn component=web msg=\"shutdown error\" err=%q", err)
		}
	}()
	log.Printf("level=info component=web msg=\"listening\" addr=%q", s.httpServer.Addr)
	if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("level=error component=web msg=\"server error\" err=%q", err)
	}
}

// broadcast sends a ping to all connected SSE clients.
func (s *Server) broadcast() {
	s.sseMu.Lock()
	defer s.sseMu.Unlock()
	for c := range s.sseSubs {
		select {
		case c.ch <- struct{}{}:
		default:
		}
	}
}

// ---------- SSE ---------------------------------------------------------------

// handleSSE — GET /api/events
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	// SSE connections are long-lived; clear the per-connection write deadline so
	// the server-wide WriteTimeout does not prematurely close the stream.
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		log.Printf("level=warn component=web msg=\"could not clear SSE write deadline\" err=%q", err)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	client := &sseClient{ch: make(chan struct{}, 4)}
	s.sseMu.Lock()
	s.sseSubs[client] = struct{}{}
	s.sseMu.Unlock()
	defer func() {
		s.sseMu.Lock()
		delete(s.sseSubs, client)
		s.sseMu.Unlock()
	}()

	fmt.Fprintf(w, "event: update\ndata: {}\n\n")
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-client.ch:
			fmt.Fprintf(w, "event: update\ndata: {}\n\n")
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// ---------- Image API ---------------------------------------------------------

// handleImages — GET /api/images
func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	entries := s.store.List()
	if entries == nil {
		entries = []store.Entry{}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(entries); err != nil {
		log.Printf("level=error component=web msg=\"encode images\" err=%q", err)
	}
}

// handleQueue — POST /api/images/queue
// Body: {"filenames":["IMG_0001.JPG",...]}
// Marks selected discovered images as queued and starts uploading them.
func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Filenames []string `json:"filenames"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if len(req.Filenames) == 0 {
		http.Error(w, "filenames must not be empty", http.StatusBadRequest)
		return
	}
	entries := s.store.DiscoveredByFilenames(req.Filenames)
	if len(entries) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	urls := make([]string, len(entries))
	for i, e := range entries {
		urls[i] = e.URL
	}
	s.store.MarkQueued(urls)
	s.queue(entriesToImages(entries))
	w.WriteHeader(http.StatusAccepted)
}

// handleQueueAll — POST /api/images/queue-all
// Queues every image that is currently in "discovered" status.
func (s *Server) handleQueueAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	n := s.store.MarkAllDiscoveredQueued()
	if n == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	entries := s.store.AllFreshQueued()
	if len(entries) > 0 {
		s.queue(entriesToImages(entries))
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleRetryFailed — POST /api/images/retry-failed
// Resets all failed images to queued and re-enqueues them.
func (s *Server) handleRetryFailed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	n := s.store.ResetOnlyFailed()
	if n == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	entries := s.store.AllFreshQueued()
	if len(entries) > 0 {
		s.queue(entriesToImages(entries))
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleImageFile dispatches /api/images/{filename}/thumb and /api/images/{filename}/download.
func (s *Server) handleImageFile(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/images/")
	switch {
	case strings.HasSuffix(trimmed, "/thumb"):
		s.serveThumb(w, r, strings.TrimSuffix(trimmed, "/thumb"))
	case strings.HasSuffix(trimmed, "/download"):
		s.serveDownloadSingle(w, r, strings.TrimSuffix(trimmed, "/download"))
	default:
		http.NotFound(w, r)
	}
}

// serveThumb — GET /api/images/{filename}/thumb
func (s *Server) serveThumb(w http.ResponseWriter, r *http.Request, filename string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if filename == "" {
		http.NotFound(w, r)
		return
	}

	if cached, ok := s.thumbCache.Load(filename); ok {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Write(cached)
		return
	}

	entry := s.store.GetByFilename(filename)
	if entry == nil {
		http.NotFound(w, r)
		return
	}
	if s.thumbFunc == nil {
		http.Error(w, "thumbnails not available", http.StatusNotImplemented)
		return
	}

	rc, err := s.thumbFunc(r.Context(), entry.URL)
	if err != nil {
		log.Printf("level=warn component=web msg=\"thumbnail fetch failed\" file=%q err=%q", filename, err)
		http.Error(w, "failed to fetch thumbnail", http.StatusBadGateway)
		return
	}
	if rc == nil {
		http.Error(w, "thumbnails not available", http.StatusNotImplemented)
		return
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		log.Printf("level=warn component=web msg=\"thumbnail read failed\" file=%q err=%q", filename, err)
		http.Error(w, "failed to read thumbnail", http.StatusBadGateway)
		return
	}
	s.thumbCache.Store(filename, data)

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	io.Copy(w, bytes.NewReader(data))
}

// serveDownloadSingle — GET /api/images/{filename}/download
// Streams the original full-resolution image from the camera.
func (s *Server) serveDownloadSingle(w http.ResponseWriter, r *http.Request, filename string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if filename == "" {
		http.NotFound(w, r)
		return
	}
	if s.downloadFunc == nil {
		http.Error(w, "download not available", http.StatusNotImplemented)
		return
	}
	entry := s.store.GetByFilename(filename)
	if entry == nil {
		http.NotFound(w, r)
		return
	}
	log.Printf("level=info component=web msg=\"single download\" file=%q", filename)
	rc, err := s.downloadFunc(r.Context(), canon.Image{Filename: entry.Filename, URL: entry.URL})
	if err != nil {
		log.Printf("level=warn component=web msg=\"download failed\" file=%q err=%q", filename, err)
		http.Error(w, "failed to download image", http.StatusBadGateway)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.Header().Set("Cache-Control", "no-store")
	if _, err := io.Copy(w, rc); err != nil {
		log.Printf("level=warn component=web msg=\"download copy error\" file=%q err=%q", filename, err)
	}
}

// handleDownloadZip — POST /api/images/download
// Body: {"filenames":["IMG_0001.JPG",...]}
// Streams a ZIP archive containing all requested images.
func (s *Server) handleDownloadZip(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.downloadFunc == nil {
		http.Error(w, "download not available", http.StatusNotImplemented)
		return
	}
	var req struct {
		Filenames []string `json:"filenames"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Filenames) == 0 {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Resolve entries from the store, preserving request order.
	// Use GetByFilename for each so images in any status (discovered, queued, done, etc.) are included.
	var entries []store.Entry
	for _, fn := range req.Filenames {
		if e := s.store.GetByFilename(fn); e != nil {
			entries = append(entries, *e)
		} else {
			log.Printf("level=warn component=web msg=\"zip: file not found\" file=%q", fn)
		}
	}
	if len(entries) == 0 {
		http.Error(w, "no matching images found", http.StatusNotFound)
		return
	}

	log.Printf("level=info component=web msg=\"zip download started\" count=%d", len(entries))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=\"canon-images.zip\"")
	w.Header().Set("Cache-Control", "no-store")

	zw := zip.NewWriter(w)
	defer zw.Close()

	for _, entry := range entries {
		rc, err := s.downloadFunc(r.Context(), canon.Image{Filename: entry.Filename, URL: entry.URL})
		if err != nil {
			log.Printf("level=warn component=web msg=\"zip: skip file\" file=%q err=%q", entry.Filename, err)
			continue
		}
		fw, err := zw.Create(entry.Filename)
		if err != nil {
			rc.Close()
			log.Printf("level=warn component=web msg=\"zip: create entry\" file=%q err=%q", entry.Filename, err)
			continue
		}
		if _, err := io.Copy(fw, rc); err != nil {
			log.Printf("level=warn component=web msg=\"zip: copy error\" file=%q err=%q", entry.Filename, err)
		}
		rc.Close()
	}
	log.Printf("level=info component=web msg=\"zip download done\" count=%d", len(entries))
}

// ---------- Queue controls ----------------------------------------------------

// handleQueueStatus — GET /api/queue/status
func (s *Server) handleQueueStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	paused := s.queueCtrl.IsPaused != nil && s.queueCtrl.IsPaused()
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"paused":%v}`+"\n", paused)
}

// handleQueuePause — POST /api/queue/pause
func (s *Server) handleQueuePause(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.queueCtrl.Pause != nil {
		s.queueCtrl.Pause()
	}
	s.broadcast()
	w.WriteHeader(http.StatusNoContent)
}

// handleQueueResume — POST /api/queue/resume
func (s *Server) handleQueueResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.queueCtrl.Resume != nil {
		s.queueCtrl.Resume()
	}
	s.broadcast()
	w.WriteHeader(http.StatusNoContent)
}

// handleQueueClear — POST /api/queue/clear
func (s *Server) handleQueueClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.queueCtrl.Clear != nil {
		s.queueCtrl.Clear()
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- Settings ----------------------------------------------------------

// handleSettings — GET /api/settings | PUT /api/settings
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getSettings(w, r)
	case http.MethodPut:
		s.putSettings(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	if s.settingRepo == nil {
		http.Error(w, "settings not available", http.StatusNotImplemented)
		return
	}
	m, err := s.settingRepo.All()
	if err != nil {
		http.Error(w, "failed to load settings", http.StatusInternalServerError)
		return
	}
	// Redact secret credential fields so they are never exposed via the API.
	// Users can update these keys via PUT /api/settings (write-only).
	secretKeys := map[string]bool{
		"smb.password":   true,
		"ftp.password":   true,
		"s3.secret_key":  true,
		"azure.sas_token": true,
	}
	for k := range m {
		if secretKeys[k] {
			if m[k] != "" {
				m[k] = "********"
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(m); err != nil {
		log.Printf("level=error component=web msg=\"encode settings\" err=%q", err)
	}
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	if s.settingRepo == nil {
		http.Error(w, "settings not available", http.StatusNotImplemented)
		return
	}
	var kv map[string]string
	if err := json.NewDecoder(r.Body).Decode(&kv); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if err := s.settingRepo.SetMany(kv); err != nil {
		log.Printf("level=error component=web msg=\"save settings\" err=%q", err)
		http.Error(w, "failed to save settings", http.StatusInternalServerError)
		return
	}
	log.Printf("level=info component=web msg=\"settings updated\" keys=%d", len(kv))
	// Return the full updated settings map.
	s.getSettings(w, r)
}

// ---------- Camera discovery -------------------------------------------------

// handleCameraScan — POST /api/camera/scan
// Scans all local LAN subnets for PTP/IP cameras (port 15740).
// The scan runs with a 20-second deadline so the HTTP response always arrives.
func (s *Server) handleCameraScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	log.Printf("level=info component=web msg=\"camera scan started\"")
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	cameras, err := canon.DiscoverLAN(ctx)
	if err != nil {
		log.Printf("level=warn component=web msg=\"camera scan error\" err=%q", err)
		http.Error(w, "scan failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if cameras == nil {
		cameras = []canon.DiscoveredCamera{}
	}
	log.Printf("level=info component=web msg=\"camera scan done\" found=%d", len(cameras))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cameras)
}

// ---------- System ------------------------------------------------------------

// handleLogStream — GET /api/logs
// Streams server log lines as SSE events. Sends the last 200 lines on connect,
// then pushes new lines as they are written to the logger.
func (s *Server) handleLogStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	if s.logBcast == nil {
		http.Error(w, "log stream not configured", http.StatusNotImplemented)
		return
	}
	// Log stream connections are long-lived; clear the per-connection write
	// deadline so the server-wide WriteTimeout does not close the stream.
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		log.Printf("level=warn component=web msg=\"could not clear log stream write deadline\" err=%q", err)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	client, history := s.logBcast.subscribe()
	defer s.logBcast.unsubscribe(client)

	// Replay recent history so the viewer is immediately useful.
	for _, line := range history {
		safe := strings.NewReplacer("\n", "\\n", "\r", "\\r").Replace(line)
		fmt.Fprintf(w, "data: %s\n\n", safe)
	}
	flusher.Flush()
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case line := <-client.ch:
			fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		}
	}
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Minimal hardening: only allow restarts from localhost.
	if !strings.HasPrefix(r.RemoteAddr, "127.0.0.1:") && !strings.HasPrefix(r.RemoteAddr, "[::1]:") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if s.restartFunc == nil {
		http.Error(w, "restart not configured", http.StatusNotImplemented)
		return
	}
	log.Printf("level=info component=web msg=\"restart requested\"")
	w.WriteHeader(http.StatusAccepted)
	go func() {
		time.Sleep(150 * time.Millisecond) // let the HTTP response flush
		s.restartFunc()
	}()
}

// ---------- helpers -----------------------------------------------------------

func entriesToImages(entries []store.Entry) []canon.Image {
	imgs := make([]canon.Image, len(entries))
	for i, e := range entries {
		imgs[i] = canon.Image{Filename: e.Filename, URL: e.URL}
	}
	return imgs
}
