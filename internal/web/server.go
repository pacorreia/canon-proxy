package web

import (
	"context"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/pacorreia/canon-proxy/internal/canon"
	"github.com/pacorreia/canon-proxy/internal/store"
)

//go:embed static
var staticFiles embed.FS

// PushFunc is called when the user requests images to be uploaded.
// Implementations should enqueue the given images for processing.
type PushFunc func(images []canon.Image)

// Server is the web UI HTTP server.
type Server struct {
	store      *store.Store
	cameraBase string // e.g. "http://192.168.1.100:8080"
	push       PushFunc
	httpServer *http.Server
	thumbClient *http.Client
}

// New creates a Server.
//
//   - st        — shared image store
//   - cameraBase — base URL of the camera (used to proxy thumbnails)
//   - push       — callback invoked when the user requests a push
//   - listen     — TCP address to bind, e.g. ":9090"
func New(st *store.Store, cameraBase string, push PushFunc, listen string) *Server {
	s := &Server{
		store:      st,
		cameraBase: strings.TrimRight(cameraBase, "/"),
		push:       push,
		thumbClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}

	mux := http.NewServeMux()

	// Static UI
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic("web: failed to sub static embed: " + err.Error())
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	// API
	mux.HandleFunc("/api/images", s.handleImages)
	mux.HandleFunc("/api/images/push", s.handlePush)
	mux.HandleFunc("/api/images/push-all", s.handlePushAll)
	// Thumbnail proxy: /api/images/{filename}/thumb
	mux.HandleFunc("/api/images/", s.handleThumb)

	s.httpServer = &http.Server{
		Addr:         listen,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
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

	log.Printf("level=info component=web msg=\"web UI listening\" addr=%q", s.httpServer.Addr)
	if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("level=error component=web msg=\"server error\" err=%q", err)
	}
}

// handleImages — GET /api/images
func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	entries := s.store.List()
	// Return an empty JSON array rather than null when there are no images.
	if entries == nil {
		entries = []store.Entry{}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(entries); err != nil {
		log.Printf("level=error component=web msg=\"encode images\" err=%q", err)
	}
}

// handlePush — POST /api/images/push
func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
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

	entries := s.store.PendingByFilenames(req.Filenames)
	if len(entries) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	images := entriesToImages(entries)
	s.push(images)
	w.WriteHeader(http.StatusAccepted)
}

// handlePushAll — POST /api/images/push-all
func (s *Server) handlePushAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	entries := s.store.AllPending()
	if len(entries) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	images := entriesToImages(entries)
	s.push(images)
	w.WriteHeader(http.StatusAccepted)
}

// handleThumb — GET /api/images/{filename}/thumb
// Proxies the thumbnail from the camera CCAPI endpoint.
func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Path: /api/images/{filename}/thumb
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/images/")
	if !strings.HasSuffix(trimmed, "/thumb") {
		http.NotFound(w, r)
		return
	}
	filename := strings.TrimSuffix(trimmed, "/thumb")
	if filename == "" {
		http.NotFound(w, r)
		return
	}

	entry := s.store.GetByFilename(filename)
	if entry == nil {
		http.NotFound(w, r)
		return
	}

	// Build thumbnail URL: original URL + ?kind=thumbnail
	thumbURL := entry.URL + "?kind=thumbnail"
	proxyReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, thumbURL, nil)
	if err != nil {
		http.Error(w, "failed to build thumb request", http.StatusInternalServerError)
		return
	}

	resp, err := s.thumbClient.Do(proxyReq)
	if err != nil {
		http.Error(w, "failed to fetch thumbnail from camera", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "image/jpeg")
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("level=warn component=web msg=\"thumb copy error\" err=%q", err)
	}
}

func entriesToImages(entries []store.Entry) []canon.Image {
	imgs := make([]canon.Image, len(entries))
	for i, e := range entries {
		imgs[i] = canon.Image{Filename: e.Filename, URL: e.URL}
	}
	return imgs
}
