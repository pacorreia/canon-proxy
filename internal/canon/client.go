package canon

// client.go — public API surface and Client struct definition.
// Owns the domain types (Image, CameraFolder) and the exported methods that
// orchestrate session management and object operations.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pacorreia/canon-proxy/internal/logger"
)

// clientGUID is the GUID advertised by this initiator to the camera.
// Protected by clientGUIDMu; use SetClientGUID to write, readClientGUID to read.
var (
	clientGUIDMu sync.RWMutex
	clientGUID   = [16]byte{
		0xca, 0xfe, 0xba, 0xbe, 0xde, 0xad, 0xbe, 0xef,
		0x00, 0x01, 0x63, 0x61, 0x6e, 0x6f, 0x6e, 0x78,
	}
)

// SetClientGUID overrides the GUID advertised to the camera during PTP/IP
// initialisation. Call this before creating any Client or calling DiscoverLAN.
func SetClientGUID(g [16]byte) {
	clientGUIDMu.Lock()
	clientGUID = g
	clientGUIDMu.Unlock()
}

// readClientGUID returns a copy of the current client GUID under the read lock.
func readClientGUID() [16]byte {
	clientGUIDMu.RLock()
	g := clientGUID
	clientGUIDMu.RUnlock()
	return g
}

// Image represents a camera image identified by a PTP object handle.
// URL encodes the handle as ptpip://<host>:<port>/<handle>.
type Image struct {
	Handle       uint32
	URL          string
	Filename     string
	CapturedAt   *time.Time // nil when the camera did not report a capture date
	IsVideo      bool       // true for MOV, MP4, etc.
	CameraFolder string     // DCIM subfolder name (e.g. "100CANON"); empty when not known
}

// CameraFolder is a named folder on the camera storage (e.g. a DCIM subfolder).
type CameraFolder struct {
	Name   string `json:"name"`
	Handle uint32 `json:"handle"`
}

// Client is a stateful PTP/IP client for Canon EOS cameras.
// It maintains a persistent TCP command+event socket pair and
// reconnects automatically after transient failures.
//
// Two connection modes are supported:
//   - Dial mode (default): proxy connects to camera:port. Used when the camera
//     is its own WiFi access point ("Camera Access Point" mode).
//   - Server mode: proxy listens on listenAddr; the camera connects to us.
//     This is the correct mode for Canon EOS "Computer" WiFi in infrastructure
//     networks, where the camera initiates the TCP connection to a registered
//     computer IP.
type Client struct {
	host string
	port int

	// server mode
	listenMode bool
	listenAddr string
	listener   net.Listener // created once, reused across reconnects

	mu      sync.Mutex
	cmdConn net.Conn
	evtConn net.Conn
	txID    uint32
}

// NewClient creates a PTP/IP client that dials host:port (camera access point mode).
func NewClient(host string, port int) *Client {
	return &Client{host: host, port: port}
}

// NewServerClient creates a PTP/IP client that listens on listenAddr and waits
// for the camera to connect (infrastructure / "Computer" WiFi mode).
// host is used only for image URL generation and logging.
func NewServerClient(listenAddr string, host string) *Client {
	return &Client{
		host:       host,
		port:       15740,
		listenMode: true,
		listenAddr: listenAddr,
	}
}

// handleURL encodes an object handle as a ptpip:// URL.
func (c *Client) handleURL(handle uint32) string {
	return fmt.Sprintf("ptpip://%s:%d/%d", c.host, c.port, handle)
}

// parseHandle extracts the uint32 object handle from a ptpip:// URL.
func parseHandle(rawURL string) (uint32, error) {
	i := strings.LastIndex(rawURL, "/")
	if i < 0 {
		return 0, fmt.Errorf("invalid PTP/IP URL: %q", rawURL)
	}
	h, err := strconv.ParseUint(rawURL[i+1:], 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid handle in PTP/IP URL %q: %w", rawURL, err)
	}
	return uint32(h), nil
}

// ParseHandle extracts the uint32 object handle from a ptpip:// URL.
func ParseHandle(rawURL string) (uint32, error) { return parseHandle(rawURL) }

// ---- Public API ----

// ListFolders returns the DCIM subfolders on the camera (e.g. "100CANON", "101EOS").
// It first locates the DCIM root folder, then enumerates only its direct children so
// that top-level storage objects (the DCIM folder itself, etc.) are not included.
func (c *Client) ListFolders(ctx context.Context) ([]CameraFolder, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnected(ctx); err != nil {
		return nil, fmt.Errorf("list camera folders: %w", err)
	}

	storageIDs, err := c.getStorageIDs(ctx)
	if err != nil {
		storageIDs = []uint32{0xFFFFFFFF}
	}

	seen := make(map[string]bool)
	var folders []CameraFolder
	for _, sid := range storageIDs {
		fs, err := c.discoverDCIMFolders(ctx, sid)
		if err != nil {
			logger.Warn("component=canon msg=\"ListFolders: discoverDCIMFolders failed\" storageID=0x%08X err=%q", sid, err)
			continue
		}
		for _, f := range fs {
			if !seen[f.Name] {
				seen[f.Name] = true
				folders = append(folders, f)
			}
		}
	}
	return folders, nil
}

// ListImages returns all image objects currently on the camera storage.
func (c *Client) ListImages(ctx context.Context) ([]Image, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnected(ctx); err != nil {
		return nil, fmt.Errorf("list camera images: %w", err)
	}

	images, err := c.listImages(ctx, nil, nil)
	if err != nil {
		c.closeConns()
		return nil, fmt.Errorf("list camera images: %w", err)
	}
	return images, nil
}

// ListImagesDelta is like ListImages but skips GetObjectInfo for handles already
// known to be images (knownImageHandles) or folders (knownFolderHandles). Pass
// both maps after the first full scan to dramatically reduce PTP round-trips.
// On return, newFolderHandles contains any folder handles discovered this call
// that were not in knownFolderHandles.
func (c *Client) ListImagesDelta(ctx context.Context, knownImageHandles, knownFolderHandles map[uint32]struct{}) (images []Image, newFolderHandles map[uint32]struct{}, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnected(ctx); err != nil {
		return nil, nil, fmt.Errorf("list camera images (delta): %w", err)
	}

	newFolders := make(map[uint32]struct{})
	imgs, err := c.listImages(ctx, knownImageHandles, newFolders)
	if err != nil {
		c.closeConns()
		return nil, nil, fmt.Errorf("list camera images (delta): %w", err)
	}
	// Remove already-known folders so the caller only gets newly discovered folder handles.
	for h := range knownFolderHandles {
		delete(newFolders, h)
	}
	return imgs, newFolders, nil
}

// DownloadImage downloads the full image data from the camera.
func (c *Client) DownloadImage(ctx context.Context, image Image) (io.ReadCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnected(ctx); err != nil {
		return nil, fmt.Errorf("download image %q: %w", image.Filename, err)
	}

	handle, err := parseHandle(image.URL)
	if err != nil {
		return nil, fmt.Errorf("download image %q: %w", image.Filename, err)
	}

	data, err := c.getObjectData(ctx, ptpOCGetObject, handle)
	if err != nil {
		c.closeConns()
		return nil, fmt.Errorf("download image %q: %w", image.Filename, err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// DeleteObject removes the object from the camera's storage.
// This is called after a successful upload when delete-after-upload is enabled.
func (c *Client) DeleteObject(ctx context.Context, image Image) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnected(ctx); err != nil {
		return fmt.Errorf("delete %q: %w", image.Filename, err)
	}

	handle, err := parseHandle(image.URL)
	if err != nil {
		return fmt.Errorf("delete %q: %w", image.Filename, err)
	}

	txID := c.nextTxID()
	// DeleteObject(ObjectHandle, ObjectFormatCode=0x0000 meaning "all formats")
	if err := c.sendCmdRequest(ptpOCDeleteObject, txID, false, handle, uint32(0)); err != nil {
		c.closeConns()
		return fmt.Errorf("delete %q: %w", image.Filename, err)
	}
	if _, err := c.recvResponse(ctx, txID); err != nil {
		c.closeConns()
		return fmt.Errorf("delete %q: %w", image.Filename, err)
	}
	logger.Info("component=canon msg=\"deleted from camera\" file=%q handle=0x%08X", image.Filename, handle)
	return nil
}

// GetThumb downloads the embedded JPEG thumbnail for the image at imageURL.
func (c *Client) GetThumb(ctx context.Context, imageURL string) (io.ReadCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	handle, err := parseHandle(imageURL)
	if err != nil {
		return nil, fmt.Errorf("get thumb: %w", err)
	}

	if err := c.ensureConnected(ctx); err != nil {
		return nil, fmt.Errorf("get thumb: %w", err)
	}

	data, err := c.getObjectData(ctx, ptpOCGetThumb, handle)
	if err != nil {
		c.closeConns()
		return nil, fmt.Errorf("get thumb: %w", err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
