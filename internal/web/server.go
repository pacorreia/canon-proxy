package web

import (
	"archive/zip"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	gopath "path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pacorreia/canon-proxy/internal/canon"
	"github.com/pacorreia/canon-proxy/internal/db"
	"github.com/pacorreia/canon-proxy/internal/logger"
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

// ListFoldersFunc returns the list of DCIM subfolders currently on the camera.
type ListFoldersFunc func(ctx context.Context) ([]canon.CameraFolder, error)

// ShareFoldersFunc lists subdirectory names on the file share backend at the given path.
// path is slash-separated and relative to the share root (e.g. "/" or "/photos").
type ShareFoldersFunc func(ctx context.Context, path string) ([]string, error)

// QueueController exposes pipeline pause/resume/clear controls to the web layer.
type QueueController struct {
	Pause    func()
	Resume   func()
	Clear    func()
	IsPaused func() bool
}

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 16384,
	// Allow all origins for local LAN use.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// wsClient is a single WebSocket subscriber.
type wsClient struct {
	send chan []byte
}

// Server is the web UI HTTP server.
type Server struct {
	store             *store.Store
	thumbFunc         ThumbFunc
	thumbCache        *boundedCache // filename -> thumbnail bytes; evicts an arbitrary entry when capacity is reached
	downloadFunc      DownloadFunc
	listFoldersFunc   ListFoldersFunc
	shareFoldersFunc  ShareFoldersFunc
	queue             QueueFunc
	queueCtrl         QueueController
	settingRepo       *db.SettingRepo
	pairingRepo       *db.FolderPairingRepo
	restartFunc       func() // called to restart the process
	onSettingsChanged func(map[string]string) // called after settings are saved; may be nil
	logBcast          *LogBroadcaster
	httpServer        *http.Server
	poller            *canon.Poller

	wsMu   sync.Mutex
	wsSubs map[*wsClient]struct{}
}

// ServerConfig holds all dependencies and configuration for the web server.
// Using a config struct makes it easy to add new dependencies without breaking
// existing call sites.
type ServerConfig struct {
	Store             *store.Store
	ThumbFunc         ThumbFunc
	DownloadFunc      DownloadFunc
	ListFoldersFunc   ListFoldersFunc
	ShareFoldersFunc  ShareFoldersFunc
	Queue             QueueFunc
	Listen            string
	QueueCtrl         QueueController
	SettingRepo       *db.SettingRepo
	PairingRepo       *db.FolderPairingRepo
	RestartFunc       func()
	LogBcast          *LogBroadcaster
	OnSettingsChanged func(map[string]string)
	Poller            *canon.Poller
}

// New creates a Server and registers all routes.
func New(cfg ServerConfig) *Server {
	s := &Server{
		store:             cfg.Store,
		thumbFunc:         cfg.ThumbFunc,
		thumbCache:        newBoundedCache(),
		downloadFunc:      cfg.DownloadFunc,
		listFoldersFunc:   cfg.ListFoldersFunc,
		shareFoldersFunc:  cfg.ShareFoldersFunc,
		queue:             cfg.Queue,
		queueCtrl:         cfg.QueueCtrl,
		settingRepo:       cfg.SettingRepo,
		pairingRepo:       cfg.PairingRepo,
		restartFunc:       cfg.RestartFunc,
		onSettingsChanged: cfg.OnSettingsChanged,
		logBcast:          cfg.LogBcast,
		poller:            cfg.Poller,
		wsSubs:            make(map[*wsClient]struct{}),
	}

	cfg.Store.SetOnChange(s.broadcast)

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
	// Events
	mux.HandleFunc("/api/events", s.handleWS)
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
	mux.HandleFunc("/api/camera/folders", s.handleCameraFolders)
	mux.HandleFunc("/api/camera/poll/trigger", s.handlePollTrigger)
	mux.HandleFunc("/api/camera/poll/settings", s.handlePollSettings)

	// Share folder browser
	mux.HandleFunc("/api/share/folders", s.handleShareFolders)

	// Folder pairings
	mux.HandleFunc("/api/pairings", s.handlePairings)
	mux.HandleFunc("/api/pairings/", s.handlePairingByID)

	// Live log stream
	mux.HandleFunc("/api/logs", s.handleLogStream)

	// System
	mux.HandleFunc("/api/system/restart", s.handleRestart)

	s.httpServer = &http.Server{
		Addr:         cfg.Listen,
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
			logger.Warn("component=web msg=\"shutdown error\" err=%q", err)
		}
	}()
	logger.Info("component=web msg=\"listening\" addr=%q", s.httpServer.Addr)
	if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("component=web msg=\"server error\" err=%q", err)
	}
}

// broadcast encodes the current image list and pushes it to all connected WebSocket clients.
func (s *Server) broadcast() {
	entries := s.store.List()
	if entries == nil {
		entries = []store.Entry{}
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return
	}
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	for c := range s.wsSubs {
		select {
		case c.send <- data:
		default: // client too slow, drop frame
		}
	}
}

// ---------- WebSocket ---------------------------------------------------------

// handleWS — GET /api/events (WebSocket upgrade)
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Warn("component=web msg=\"WS upgrade failed\" err=%q", err)
		return
	}

	client := &wsClient{send: make(chan []byte, 16)}
	s.wsMu.Lock()
	s.wsSubs[client] = struct{}{}
	s.wsMu.Unlock()

	// Send initial data immediately on connect.
	entries := s.store.List()
	if entries == nil {
		entries = []store.Entry{}
	}
	if data, err := json.Marshal(entries); err == nil {
		client.send <- data
	}

	// Write goroutine: drains the send channel and sends ping frames.
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		ping := time.NewTicker(25 * time.Second)
		defer ping.Stop()
		for {
			select {
			case msg, ok := <-client.send:
				if !ok {
					conn.Close()
					return
				}
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					conn.Close()
					return
				}
			case <-ping.C:
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					conn.Close()
					return
				}
			}
		}
	}()

	// Read loop: reset pong deadline; exit on disconnect.
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}

	// Cleanup.
	s.wsMu.Lock()
	delete(s.wsSubs, client)
	s.wsMu.Unlock()
	close(client.send)
	<-writeDone
}

// handlePollTrigger — POST /api/camera/poll/trigger
func (s *Server) handlePollTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.poller == nil {
		http.Error(w, "no poller configured", http.StatusServiceUnavailable)
		return
	}
	s.poller.TriggerNow()
	w.WriteHeader(http.StatusNoContent)
}

// handlePollSettings — GET/POST /api/camera/poll/settings
func (s *Server) handlePollSettings(w http.ResponseWriter, r *http.Request) {
	if s.poller == nil {
		http.Error(w, "no poller configured", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"enabled":      s.poller.AutoEnabled(),
			"interval_sec": int(s.poller.Interval().Seconds()),
		})
	case http.MethodPost:
		var body struct {
			Enabled     bool `json:"enabled"`
			IntervalSec int  `json:"interval_sec"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		s.poller.SetEnabled(body.Enabled)
		if body.IntervalSec > 0 {
			s.poller.SetInterval(time.Duration(body.IntervalSec) * time.Second)
		}
		// Persist so settings survive restarts.
		if s.settingRepo != nil {
			kv := map[string]string{
				"camera.poll_enabled": strconv.FormatBool(body.Enabled),
			}
			if body.IntervalSec > 0 {
				kv["camera.poll_interval"] = (time.Duration(body.IntervalSec) * time.Second).String()
			}
			if err := s.settingRepo.SetMany(kv); err != nil {
				logger.Warn("component=web msg=\"persist poll settings\" err=%q", err)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
		logger.Error("component=web msg=\"encode images\" err=%q", err)
	}
}

// handleQueue — POST /api/images/queue
// Body: {"filenames":["IMG_0001.JPG",...], "dest_path":"/optional/path", "upload_source":"manual"}
// Marks selected discovered images as queued and starts uploading them.
// dest_path, when provided, is stored on each record so it overrides any pairing destination.
// upload_source defaults to "manual" when not provided.
func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Filenames    []string `json:"filenames"`
		DestPath     string   `json:"dest_path"`
		UploadSource string   `json:"upload_source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if len(req.Filenames) == 0 {
		http.Error(w, "filenames must not be empty", http.StatusBadRequest)
		return
	}
	if req.UploadSource == "" {
		req.UploadSource = "manual"
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
	if req.DestPath != "" {
		s.store.MarkQueuedWithMeta(urls, req.DestPath, req.UploadSource)
	} else {
		s.store.MarkQueuedWithMeta(urls, "", req.UploadSource)
	}
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
		logger.Warn("component=web msg=\"thumbnail fetch failed\" file=%q err=%q", filename, err)
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
		logger.Warn("component=web msg=\"thumbnail read failed\" file=%q err=%q", filename, err)
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
	logger.Info("component=web msg=\"single download\" file=%q", filename)
	rc, err := s.downloadFunc(r.Context(), canon.Image{Filename: entry.Filename, URL: entry.URL})
	if err != nil {
		logger.Warn("component=web msg=\"download failed\" file=%q err=%q", filename, err)
		http.Error(w, "failed to download image", http.StatusBadGateway)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	safeName := strings.NewReplacer("\\", "_", "/", "_", "\"", "_", "\n", "", "\r", "").Replace(filename)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+safeName+"\"")
	w.Header().Set("Cache-Control", "no-store")
	if _, err := io.Copy(w, rc); err != nil {
		logger.Warn("component=web msg=\"download copy error\" file=%q err=%q", filename, err)
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
			logger.Warn("component=web msg=\"zip: file not found\" file=%q", fn)
		}
	}
	if len(entries) == 0 {
		http.Error(w, "no matching images found", http.StatusNotFound)
		return
	}

	logger.Info("component=web msg=\"zip download started\" count=%d", len(entries))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=\"canon-images.zip\"")
	w.Header().Set("Cache-Control", "no-store")

	zw := zip.NewWriter(w)
	defer zw.Close()

	for _, entry := range entries {
		rc, err := s.downloadFunc(r.Context(), canon.Image{Filename: entry.Filename, URL: entry.URL})
		if err != nil {
			logger.Warn("component=web msg=\"zip: skip file\" file=%q err=%q", entry.Filename, err)
			continue
		}
		safeName := strings.NewReplacer("/", "_", "\\", "_").Replace(entry.Filename)
		fw, err := zw.Create(safeName)
		if err != nil {
			rc.Close()
			logger.Warn("component=web msg=\"zip: create entry\" file=%q err=%q", entry.Filename, err)
			continue
		}
		if _, err := io.Copy(fw, rc); err != nil {
			logger.Warn("component=web msg=\"zip: copy error\" file=%q err=%q", entry.Filename, err)
		}
		rc.Close()
	}
	logger.Info("component=web msg=\"zip download done\" count=%d", len(entries))
}

// ---------- Queue controls ----------------------------------------------------

// handleQueueStatus — GET /api/queue/status
func (s *Server) handleQueueStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	paused := s.queueCtrl.IsPaused != nil && s.queueCtrl.IsPaused()
	cameraConnected := s.poller != nil && s.poller.CameraConnected()
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"paused":%v,"camera_connected":%v}`+"\n", paused, cameraConnected)
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
		logger.Error("component=web msg=\"encode settings\" err=%q", err)
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
	// Prevent redacted placeholders (or empty values) from clobbering stored secrets.
	secretKeys := map[string]bool{
		"smb.password":    true,
		"ftp.password":    true,
		"s3.secret_key":   true,
		"azure.sas_token": true,
	}
	for k, v := range kv {
		if secretKeys[k] && (v == "" || v == "********") {
			delete(kv, k)
		}
	}
	if err := s.settingRepo.SetMany(kv); err != nil {
		logger.Error("component=web msg=\"save settings\" err=%q", err)
		http.Error(w, "failed to save settings", http.StatusInternalServerError)
		return
	}
	logger.Info("component=web msg=\"settings updated\" keys=%d", len(kv))
	if s.onSettingsChanged != nil {
		s.onSettingsChanged(kv)
	}
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
	logger.Info("component=web msg=\"camera scan started\"")
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	cameras, err := canon.DiscoverLAN(ctx, canon.DiscoverOptions{})
	if err != nil {
		logger.Warn("component=web msg=\"camera scan error\" err=%q", err)
		http.Error(w, "camera scan failed", http.StatusInternalServerError)
		return
	}
	if cameras == nil {
		cameras = []canon.DiscoveredCamera{}
	}
	logger.Info("component=web msg=\"camera scan done\" found=%d", len(cameras))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cameras)
}

// handleCameraFolders — GET /api/camera/folders
// Returns the list of DCIM subfolders currently on the camera.
// Requires the camera to be connected; returns 503 when not available.
func (s *Server) handleCameraFolders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.listFoldersFunc == nil {
		http.Error(w, "camera not configured", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	folders, err := s.listFoldersFunc(ctx)
	if err != nil {
		logger.Warn("component=web msg=\"list camera folders error\" err=%q", err)
		http.Error(w, "failed to list camera folders", http.StatusInternalServerError)
		return
	}
	if folders == nil {
		folders = []canon.CameraFolder{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(folders)
}

// handleShareFolders — GET /api/share/folders?path=/
// Returns subdirectory names on the file share backend at the given path.
// path is slash-separated and relative to the share root.
func (s *Server) handleShareFolders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.shareFoldersFunc == nil {
		http.Error(w, "share browsing not available", http.StatusServiceUnavailable)
		return
	}
	p := r.URL.Query().Get("path")
	if p == "" {
		p = "/"
	}
	// Sanitize: path.Clean resolves .. so the result stays within the share root.
	p = gopath.Clean("/" + p)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	names, err := s.shareFoldersFunc(ctx, p)
	if err != nil {
		logger.Warn("component=web msg=\"list share folders error\" path=%q err=%q", p, err)
		http.Error(w, "failed to list share folders", http.StatusInternalServerError)
		return
	}
	if names == nil {
		names = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(names)
}

// ---------- Folder pairings --------------------------------------------------

// handlePairings — GET /api/pairings  or  POST /api/pairings
func (s *Server) handlePairings(w http.ResponseWriter, r *http.Request) {
	if s.pairingRepo == nil {
		http.Error(w, "pairings not available", http.StatusNotImplemented)
		return
	}
	switch r.Method {
	case http.MethodGet:
		records, err := s.pairingRepo.List()
		if err != nil {
			logger.Error("component=web msg=\"list pairings\" err=%q", err)
			http.Error(w, "failed to list pairings", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(records)

	case http.MethodPost:
		var req struct {
			CameraFolder string `json:"camera_folder"`
			SharePath    string `json:"share_path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if req.CameraFolder == "" || req.SharePath == "" {
			http.Error(w, "camera_folder and share_path are required", http.StatusBadRequest)
			return
		}
		rec, err := s.pairingRepo.Create(req.CameraFolder, req.SharePath)
		if err != nil {
			if errors.Is(err, db.ErrPairingAlreadyExists) {
				http.Error(w, fmt.Sprintf("camera folder %q is already paired — delete the existing pairing first", req.CameraFolder), http.StatusConflict)
				return
			}
			logger.Error("component=web msg=\"create pairing\" err=%q", err)
			http.Error(w, "failed to create pairing", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(rec)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePairingByID — DELETE or PATCH /api/pairings/{id}
func (s *Server) handlePairingByID(w http.ResponseWriter, r *http.Request) {
	if s.pairingRepo == nil {
		http.Error(w, "pairings not available", http.StatusNotImplemented)
		return
	}
	// Extract ID from path: /api/pairings/{id}
	idStr := strings.TrimPrefix(r.URL.Path, "/api/pairings/")
	if idStr == "" {
		http.Error(w, "missing pairing ID", http.StatusBadRequest)
		return
	}
	var id uint
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil || id == 0 {
		http.Error(w, "invalid pairing ID", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodDelete:
		if err := s.pairingRepo.Delete(id); err != nil {
			if errors.Is(err, db.ErrPairingNotFound) {
				http.Error(w, "pairing not found", http.StatusNotFound)
				return
			}
			logger.Error("component=web msg=\"delete pairing\" id=%d err=%q", id, err)
			http.Error(w, "failed to delete pairing", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case http.MethodPatch:
		var req struct {
			AutoUpload bool `json:"auto_upload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := s.pairingRepo.SetAutoUpload(id, req.AutoUpload); err != nil {
			if errors.Is(err, db.ErrPairingNotFound) {
				http.Error(w, "pairing not found", http.StatusNotFound)
				return
			}
			logger.Error("component=web msg=\"patch pairing auto_upload\" id=%d err=%q", id, err)
			http.Error(w, "failed to update pairing", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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
		logger.Warn("component=web msg=\"could not clear log stream write deadline\" err=%q", err)
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
			safe := strings.NewReplacer("\n", "\\n", "\r", "\\r").Replace(line)
			fmt.Fprintf(w, "data: %s\n\n", safe)
			flusher.Flush()
		}
	}
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Hardening: refuse requests that carry any proxy or forwarding header.
	// This prevents an attacker behind a reverse proxy from triggering a restart
	// by spoofing RemoteAddr.
	for _, h := range []string{
		"Forwarded", "X-Forwarded-For", "X-Real-IP",
		"CF-Connecting-IP", "True-Client-IP", "X-Forwarded-Host",
	} {
		if r.Header.Get(h) != "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}
	if !strings.HasPrefix(r.RemoteAddr, "127.0.0.1:") && !strings.HasPrefix(r.RemoteAddr, "[::1]:") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if s.restartFunc == nil {
		http.Error(w, "restart not configured", http.StatusNotImplemented)
		return
	}
	logger.Info("component=web msg=\"restart requested\"")
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
		imgs[i] = canon.Image{Filename: e.Filename, URL: e.URL, CameraFolder: e.CameraFolder}
	}
	return imgs
}
