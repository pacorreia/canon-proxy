package canon

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

// PTP/IP packet types (PTP/IP specification, Annex A).
const (
	ptpipInitCommandRequest uint32 = 0x01
	ptpipInitCommandAck     uint32 = 0x02
	ptpipInitEventRequest   uint32 = 0x03
	ptpipInitEventAck       uint32 = 0x04
	ptpipInitFail           uint32 = 0x05
	ptpipCmdRequest         uint32 = 0x06
	ptpipCmdResponse        uint32 = 0x07
	ptpipStartDataPacket    uint32 = 0x09
	ptpipDataPacket         uint32 = 0x0A
	ptpipEndDataPacket      uint32 = 0x0C
	ptpipPing               uint32 = 0x0D
	ptpipPong               uint32 = 0x0E
)

// PTP standard operation codes (ISO 15740).
const (
	ptpOCOpenSession      uint16 = 0x1002
	ptpOCGetStorageIDs    uint16 = 0x1004
	ptpOCGetObjectHandles uint16 = 0x1007
	ptpOCGetObjectInfo    uint16 = 0x1008
	ptpOCGetObject        uint16 = 0x1009
	ptpOCGetThumb         uint16 = 0x100A
	ptpOCDeleteObject     uint16 = 0x100B
)

// Canon EOS vendor-specific operation codes.
const (
	ptpOCCanonEOSSetRemoteMode uint16 = 0x9114
	ptpOCCanonEOSSetEventMode  uint16 = 0x9115
)

// ptpRCOK is the PTP response code for "OK".
const ptpRCOK uint16 = 0x2001

// clientGUID is the GUID advertised by this initiator to the camera.
var clientGUID = [16]byte{
	0xca, 0xfe, 0xba, 0xbe, 0xde, 0xad, 0xbe, 0xef,
	0x00, 0x01, 0x63, 0x61, 0x6e, 0x6f, 0x6e, 0x78,
}

// Image represents a camera image identified by a PTP object handle.
// URL encodes the handle as ptpip://<host>:<port>/<handle>.
type Image struct {
	Handle     uint32
	URL        string
	Filename   string
	CapturedAt *time.Time // nil when the camera did not report a capture date
	IsVideo    bool       // true for MOV, MP4, etc.
}

// isVideoFilename reports whether the filename looks like a video based on its extension.
func isVideoFilename(name string) bool {
	n := strings.ToUpper(name)
	return strings.HasSuffix(n, ".MOV") || strings.HasSuffix(n, ".MP4") ||
		strings.HasSuffix(n, ".AVI") || strings.HasSuffix(n, ".MTS")
}

// ParseHandle extracts the uint32 object handle from a ptpip:// URL.
func ParseHandle(rawURL string) (uint32, error) { return parseHandle(rawURL) }

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

// isImageFormat reports whether the PTP object format code represents an image.
func isImageFormat(format uint16) bool {
	switch {
	case format == 0x3001: // Association (directory)
		return false
	case format == 0x3000: // Undefined
		return false
	case format >= 0x3800 && format < 0x4000: // Standard still-image formats (JPEG, TIFF, …)
		return true
	case format >= 0xB000: // Vendor-defined (Canon CRW, CR2, CR3, MOV, …)
		return true
	}
	return false
}

// ---- Public API ----

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
	// merge newly-found folders with the known set so caller can cache them
	for h := range knownFolderHandles {
		newFolders[h] = struct{}{}
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
	log.Printf("level=info component=canon msg=\"deleted from camera\" file=%q handle=0x%08X", image.Filename, handle)
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

// ---- Connection management ----

func (c *Client) ensureConnected(ctx context.Context) error {
	if c.cmdConn != nil {
		return nil
	}
	// Retry with exponential backoff: camera needs a few seconds to recover
	// after dropping a connection following a GetObject download.
	backoff := time.Second
	const maxAttempts = 6
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		lastErr = c.connect(ctx)
		if lastErr == nil {
			return nil
		}
		if attempt == maxAttempts {
			break
		}
		log.Printf("level=warn component=canon msg=\"reconnect failed, retrying\" attempt=%d backoff=%s err=%q", attempt, backoff, lastErr)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		if backoff < 16*time.Second {
			backoff *= 2
		}
	}
	return lastErr
}

func (c *Client) connect(ctx context.Context) error {
	if c.listenMode {
		return c.connectServer(ctx)
	}
	return c.connectClient(ctx)
}

// connectClient dials the camera directly (access-point / dial mode).
func (c *Client) connectClient(ctx context.Context) error {
	addr := net.JoinHostPort(c.host, strconv.Itoa(c.port))
	dialer := net.Dialer{Timeout: 10 * time.Second}

	cmdConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial command channel %s: %w", addr, err)
	}
	if err := c.sendInitCommandRequest(cmdConn); err != nil {
		cmdConn.Close()
		return err
	}
	connNum, err := c.recvInitCommandAck(cmdConn)
	if err != nil {
		cmdConn.Close()
		return err
	}

	evtConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		cmdConn.Close()
		return fmt.Errorf("dial event channel %s: %w", addr, err)
	}
	if err := c.sendInitEventRequest(evtConn, connNum); err != nil {
		cmdConn.Close()
		evtConn.Close()
		return err
	}
	if err := c.recvInitEventAck(evtConn); err != nil {
		cmdConn.Close()
		evtConn.Close()
		return err
	}

	c.cmdConn = cmdConn
	c.evtConn = evtConn
	c.txID = 0

	if err := c.openSession(ctx); err != nil {
		c.closeConns()
		return fmt.Errorf("open PTP session: %w", err)
	}

	// The EOS 2000D floods dozens of property-change events after OpenSession
	// and is not ready to handle PTP commands for ~5 seconds. Without this
	// delay, the first GetObjectInfo call gets a connection reset.
	// Ref: https://github.com/gphoto/gphoto2/issues/382
	log.Printf("level=info component=canon msg=\"PTP session open, waiting for camera to settle\" remote=%q", cmdConn.RemoteAddr())
	select {
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
		c.closeConns()
		return ctx.Err()
	}
	log.Printf("level=info component=canon msg=\"camera ready\" remote=%q", cmdConn.RemoteAddr())

	// Drain the event channel in the background so its TCP buffer never fills
	// and causes backpressure on the command channel.
	go c.drainEvents(evtConn)
	return nil
}

func (c *Client) closeConns() {
	if c.cmdConn != nil {
		c.cmdConn.Close()
		c.cmdConn = nil
	}
	if c.evtConn != nil {
		c.evtConn.Close()
		c.evtConn = nil
	}
}

// drainEvents reads and discards all packets from the PTP/IP event channel.
// The EOS 2000D sends dozens of property-change events after OpenSession.
// If left unread they fill the TCP receive buffer, causing backpressure that
// stalls the camera's command-channel responses. This goroutine runs until
// the connection is closed.
func (c *Client) drainEvents(conn net.Conn) {
	for {
		_, _, err := recvPacket(conn)
		if err != nil {
			return // connection closed; goroutine exits
		}
	}
}

// connectServer listens for the camera to connect to us (infrastructure WiFi mode).
// The camera is the TCP client; we are the TCP server but still the PTP/IP Initiator.
func (c *Client) connectServer(ctx context.Context) error {
	if c.listener == nil {
		ln, err := net.Listen("tcp", c.listenAddr)
		if err != nil {
			return fmt.Errorf("listen on %s: %w", c.listenAddr, err)
		}
		c.listener = ln
		log.Printf("level=info component=canon msg=\"listening for camera\" addr=%q", c.listenAddr)
	}

	log.Printf("level=info component=canon msg=\"waiting for camera to connect\" addr=%q", c.listenAddr)

	// Accept command channel — camera dials us first.
	cmdConn, err := acceptWithContext(ctx, c.listener)
	if err != nil {
		return fmt.Errorf("accept command channel: %w", err)
	}
	log.Printf("level=info component=canon msg=\"camera connected (command channel)\" remote=%q", cmdConn.RemoteAddr())

	if err := c.sendInitCommandRequest(cmdConn); err != nil {
		cmdConn.Close()
		return err
	}
	connNum, err := c.recvInitCommandAck(cmdConn)
	if err != nil {
		cmdConn.Close()
		return err
	}

	// Accept event channel — camera dials us a second time.
	evtConn, err := acceptWithContext(ctx, c.listener)
	if err != nil {
		cmdConn.Close()
		return fmt.Errorf("accept event channel: %w", err)
	}
	log.Printf("level=info component=canon msg=\"camera connected (event channel)\" remote=%q", evtConn.RemoteAddr())

	if err := c.sendInitEventRequest(evtConn, connNum); err != nil {
		cmdConn.Close()
		evtConn.Close()
		return err
	}
	if err := c.recvInitEventAck(evtConn); err != nil {
		cmdConn.Close()
		evtConn.Close()
		return err
	}

	c.cmdConn = cmdConn
	c.evtConn = evtConn
	c.txID = 0

	if err := c.openSession(ctx); err != nil {
		c.closeConns()
		return fmt.Errorf("open PTP session: %w", err)
	}

	log.Printf("level=info component=canon msg=\"PTP session open, waiting for camera to settle\" remote=%q", cmdConn.RemoteAddr())
	select {
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
		c.closeConns()
		return ctx.Err()
	}
	log.Printf("level=info component=canon msg=\"camera ready\" remote=%q", cmdConn.RemoteAddr())
	go c.drainEvents(evtConn)
	return nil
}

// acceptWithContext calls Accept() with periodic deadline resets so that
// context cancellation is honoured without goroutine leaks.
func acceptWithContext(ctx context.Context, ln net.Listener) (net.Conn, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if tln, ok := ln.(*net.TCPListener); ok {
			_ = tln.SetDeadline(time.Now().Add(1 * time.Second))
		}
		conn, err := ln.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue // check ctx and retry
			}
			return nil, err
		}
		return conn, nil
	}
}

// ---- PTP session ----

func (c *Client) openSession(ctx context.Context) error {
	txID := c.nextTxID()
	if err := c.sendCmdRequest(ptpOCOpenSession, txID, false, 1); err != nil {
		return err
	}
	_, err := c.recvResponse(ctx, txID)
	return err
}

// canonEOSInit sets remote and event modes required for Canon EOS event polling.
func (c *Client) canonEOSInit(ctx context.Context) error {
	txID := c.nextTxID()
	if err := c.sendCmdRequest(ptpOCCanonEOSSetRemoteMode, txID, false, 1); err != nil {
		return err
	}
	if _, err := c.recvResponse(ctx, txID); err != nil {
		return err
	}

	txID = c.nextTxID()
	if err := c.sendCmdRequest(ptpOCCanonEOSSetEventMode, txID, false, 1); err != nil {
		return err
	}
	_, err := c.recvResponse(ctx, txID)
	return err
}

// ---- Image enumeration ----

// listImages enumerates all images across all storage IDs.
// knownImageHandles: handles already known to be images — skip GetObjectInfo (pass nil for full scan).
// newFolderHandles:  caller-supplied map that receives any newly-discovered folder handles (may be nil).
func (c *Client) listImages(ctx context.Context, knownImageHandles map[uint32]struct{}, newFolderHandles map[uint32]struct{}) ([]Image, error) {
	// Get storage IDs first (required before GetObjectHandles on many cameras).
	storageIDs, err := c.getStorageIDs(ctx)
	if err != nil {
		log.Printf("level=warn component=canon msg=\"GetStorageIDs failed, falling back to 0xFFFFFFFF\" err=%q", err)
		storageIDs = []uint32{0xFFFFFFFF}
	}
	log.Printf("level=debug component=canon msg=\"storage IDs\" ids=%v", storageIDs)

	var images []Image
	for _, sid := range storageIDs {
		imgs, err := c.enumerateObjects(ctx, sid, 0xFFFFFFFF, 0, knownImageHandles, newFolderHandles)
		if err != nil {
			log.Printf("level=warn component=canon msg=\"enumerate failed\" storageID=0x%08X err=%q", sid, err)
			return nil, err // propagate so caller closes and reconnects
		}
		images = append(images, imgs...)
	}
	return images, nil
}

// enumerateObjects recursively enumerates PTP objects under parent in the given storage.
// Canon EOS cameras in Smartphone mode expose virtual container handles with bit 31 set.
// There are two kinds:
//   - Container handles (e.g. 0x90000000, 0x91900000): GetObjectHandles returns children.
//   - Leaf image handles (e.g. 0x9190XXXX): GetObjectHandles returns count=0; these are
//     the actual images and must be accessed directly via GetObjectInfo/GetObject.
//
// knownImageHandles: skip GetObjectInfo (and emission) for these handles — already known.
// newFolderHandles:  if non-nil, newly-found folder handles are added to this map.
func (c *Client) enumerateObjects(ctx context.Context, storageID, parent uint32, depth int, knownImageHandles map[uint32]struct{}, newFolderHandles map[uint32]struct{}) ([]Image, error) {
	if depth > 8 {
		return nil, nil
	}

	handles, err := c.getObjectHandles(ctx, storageID, 0, parent)
	if err != nil {
		return nil, err
	}
	log.Printf("level=debug component=canon msg=\"got handles\" storageID=0x%08X parent=0x%08X depth=%d count=%d", storageID, parent, depth, len(handles))

	var images []Image
	for _, handle := range handles {
		if handle >= 0x80000000 {
			// Virtual handle: check if it has children (container) or is a leaf (image).
			// Avoid calling GetObjectInfo on virtual handles — the camera resets the
			// connection when GetObjectInfo is called on top-level virtual containers.

			// Known image: skip everything.
			if knownImageHandles != nil {
				if _, known := knownImageHandles[handle]; known {
					continue
				}
			}

			children, err := c.getObjectHandles(ctx, storageID, 0, handle)
			if err != nil {
				return nil, err
			}
			if len(children) > 0 {
				// Container: recurse using this handle as the new parent.
				log.Printf("level=debug component=canon msg=\"virtual container\" handle=0x%08X children=%d", handle, len(children))
				if newFolderHandles != nil {
					newFolderHandles[handle] = struct{}{}
				}
				imgs, err := c.enumerateObjects(ctx, storageID, handle, depth+1, knownImageHandles, newFolderHandles)
				if err != nil {
					return nil, err
				}
				images = append(images, imgs...)
			} else {
				// Leaf virtual handle — the actual image object. Try GetObjectInfo.
				log.Printf("level=debug component=canon msg=\"virtual leaf, trying GetObjectInfo\" handle=0x%08X", handle)
				info, err := c.getObjectInfo(ctx, handle)
				if err != nil {
					log.Printf("level=warn component=canon msg=\"virtual leaf GetObjectInfo failed\" handle=0x%08X err=%q", handle, err)
					return nil, err
				}
				if isImageFormat(info.format) {
					log.Printf("level=debug component=canon msg=\"found image\" handle=0x%08X filename=%q", handle, info.filename)
					img := Image{
						Handle:   handle,
						URL:      c.handleURL(handle),
						Filename: info.filename,
						IsVideo:  isVideoFilename(info.filename),
					}
					if !info.captureDate.IsZero() {
						t := info.captureDate
						img.CapturedAt = &t
					}
					images = append(images, img)
				}
			}
			continue
		}

		// Regular (non-virtual) handle.

		// Already known as an image: skip.
		if knownImageHandles != nil {
			if _, known := knownImageHandles[handle]; known {
				continue
			}
		}

		info, err := c.getObjectInfo(ctx, handle)
		if err != nil {
			log.Printf("level=warn component=canon msg=\"GetObjectInfo failed\" handle=0x%08X err=%q", handle, err)
			return nil, err
		}
		switch {
		case info.format == 0x3001: // Association/folder — recurse
			if newFolderHandles != nil {
				newFolderHandles[handle] = struct{}{}
			}
			imgs, err := c.enumerateObjects(ctx, storageID, handle, depth+1, knownImageHandles, newFolderHandles)
			if err != nil {
				return nil, err
			}
			images = append(images, imgs...)
		case isImageFormat(info.format):
			img := Image{
				Handle:   handle,
				URL:      c.handleURL(handle),
				Filename: info.filename,
				IsVideo:  isVideoFilename(info.filename),
			}
			if !info.captureDate.IsZero() {
				t := info.captureDate
				img.CapturedAt = &t
			}
			images = append(images, img)
		default:
			log.Printf("level=debug component=canon msg=\"skipping non-image\" handle=0x%08X format=0x%04X", handle, info.format)
		}
	}
	return images, nil
}

func (c *Client) getStorageIDs(ctx context.Context) ([]uint32, error) {
	txID := c.nextTxID()
	if err := c.sendCmdRequest(ptpOCGetStorageIDs, txID, true); err != nil {
		return nil, err
	}
	data, err := c.recvData(ctx, txID)
	if err != nil {
		return nil, err
	}
	if _, err := c.recvResponse(ctx, txID); err != nil {
		return nil, err
	}
	return parsePTPUint32Array(data), nil
}

type ptpObjectInfo struct {
	format      uint16
	filename    string
	captureDate time.Time // zero value if not present or unparseable
}

func (c *Client) getObjectHandles(ctx context.Context, storageID, format, parent uint32) ([]uint32, error) {
	txID := c.nextTxID()
	if err := c.sendCmdRequest(ptpOCGetObjectHandles, txID, true, storageID, format, parent); err != nil {
		return nil, err
	}
	data, err := c.recvData(ctx, txID)
	if err != nil {
		return nil, err
	}
	if _, err := c.recvResponse(ctx, txID); err != nil {
		return nil, err
	}
	return parsePTPUint32Array(data), nil
}

func (c *Client) getObjectInfo(ctx context.Context, handle uint32) (ptpObjectInfo, error) {
	txID := c.nextTxID()
	if err := c.sendCmdRequest(ptpOCGetObjectInfo, txID, true, handle); err != nil {
		return ptpObjectInfo{}, err
	}
	data, err := c.recvData(ctx, txID)
	if err != nil {
		return ptpObjectInfo{}, err
	}
	if _, err := c.recvResponse(ctx, txID); err != nil {
		return ptpObjectInfo{}, err
	}
	return parseObjectInfo(data), nil
}

func (c *Client) getObjectData(ctx context.Context, opcode uint16, handle uint32) ([]byte, error) {
	txID := c.nextTxID()
	if err := c.sendCmdRequest(opcode, txID, true, handle); err != nil {
		return nil, err
	}
	data, err := c.recvData(ctx, txID)
	if err != nil {
		return nil, err
	}
	if _, err := c.recvResponse(ctx, txID); err != nil {
		return nil, err
	}
	return data, nil
}

// ---- PTP/IP transport layer ----

func (c *Client) nextTxID() uint32 {
	c.txID++
	return c.txID
}

// sendCmdRequest sends a PTP/IP OperationRequest packet.
// dataPhase=true sets DataPhase=2 (data phase follows); false sets DataPhase=1 (no data).
func (c *Client) sendCmdRequest(opcode uint16, txID uint32, dataPhase bool, params ...uint32) error {
	dp := uint32(1)
	if dataPhase {
		dp = 2
	}
	payload := make([]byte, 10+4*len(params))
	binary.LittleEndian.PutUint32(payload[0:4], dp)
	binary.LittleEndian.PutUint16(payload[4:6], opcode)
	binary.LittleEndian.PutUint32(payload[6:10], txID)
	for i, p := range params {
		binary.LittleEndian.PutUint32(payload[10+4*i:], p)
	}
	_ = c.cmdConn.SetDeadline(time.Now().Add(30 * time.Second))
	return sendPacket(c.cmdConn, ptpipCmdRequest, payload)
}

// recvResponse reads packets from the command channel until the OperationResponse
// matching txID arrives. Ping packets are answered with Pong.
func (c *Client) recvResponse(ctx context.Context, txID uint32) ([]uint32, error) {
	for {
		pktType, payload, err := recvPacket(c.cmdConn)
		if err != nil {
			return nil, fmt.Errorf("recv response: %w", err)
		}
		log.Printf("level=debug component=canon msg=\"recvResponse pkt\" type=0x%02X len=%d txID=%d", pktType, len(payload), txID)
		switch pktType {
		case ptpipCmdResponse:
			if len(payload) < 6 {
				return nil, fmt.Errorf("response packet too short (%d bytes)", len(payload))
			}
			rc := binary.LittleEndian.Uint16(payload[0:2])
			rTxID := binary.LittleEndian.Uint32(payload[2:6])
			log.Printf("level=debug component=canon msg=\"response code\" rc=0x%04X rTxID=%d", rc, rTxID)
			if rTxID != txID {
				continue
			}
			if rc != ptpRCOK {
				return nil, fmt.Errorf("PTP error code 0x%04X", rc)
			}
			var params []uint32
			for off := 6; off+4 <= len(payload); off += 4 {
				params = append(params, binary.LittleEndian.Uint32(payload[off:off+4]))
			}
			return params, nil
		case ptpipPing:
			_ = sendPacket(c.cmdConn, ptpipPong, nil)
		default:
			log.Printf("level=debug component=canon msg=\"unexpected packet in recvResponse\" type=0x%02X len=%d", pktType, len(payload))
		}
	}
}

// recvData reads StartDataPacket + optional DataPackets + EndDataPacket and
// returns the assembled payload bytes.
func (c *Client) recvData(ctx context.Context, txID uint32) ([]byte, error) {
	var buf []byte

	for {
		pktType, payload, err := recvPacket(c.cmdConn)
		if err != nil {
			return nil, fmt.Errorf("recv data: %w", err)
		}
		switch pktType {
		case ptpipStartDataPacket:
			// payload: TxID(4) + TotalDataLength(8)
			if len(payload) >= 12 {
				totalLen := binary.LittleEndian.Uint64(payload[4:12])
				if totalLen > 0 {
					buf = make([]byte, 0, totalLen)
				}
			}
		case ptpipDataPacket:
			// payload: TxID(4) + Data
			if len(payload) > 4 {
				buf = append(buf, payload[4:]...)
			}
		case ptpipEndDataPacket:
			// payload: TxID(4) + Data (last chunk)
			if len(payload) > 4 {
				buf = append(buf, payload[4:]...)
			}
			return buf, nil
		case ptpipCmdResponse:
			if len(payload) >= 2 {
				rc := binary.LittleEndian.Uint16(payload[0:2])
				return nil, fmt.Errorf("received OperationResponse (rc=0x%04X) during data receive phase", rc)
			}
			return nil, fmt.Errorf("received OperationResponse during data receive phase")
		case ptpipPing:
			_ = sendPacket(c.cmdConn, ptpipPong, nil)
		default:
			log.Printf("level=debug component=canon msg=\"unexpected packet in recvData\" type=0x%02X len=%d", pktType, len(payload))
		}
	}
}

// ---- Low-level PTP/IP packet I/O ----

// sendPacket writes a complete PTP/IP packet: [Length(4)][Type(4)][payload].
func sendPacket(conn net.Conn, pktType uint32, payload []byte) error {
	total := 8 + len(payload)
	buf := make([]byte, total)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(total))
	binary.LittleEndian.PutUint32(buf[4:8], pktType)
	copy(buf[8:], payload)
	_, err := conn.Write(buf)
	return err
}

// recvPacket reads one PTP/IP packet and returns its type and payload bytes.
func recvPacket(conn net.Conn) (pktType uint32, payload []byte, err error) {
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	hdr := make([]byte, 8)
	if _, err = io.ReadFull(conn, hdr); err != nil {
		return 0, nil, fmt.Errorf("read packet header: %w", err)
	}
	length := binary.LittleEndian.Uint32(hdr[0:4])
	pktType = binary.LittleEndian.Uint32(hdr[4:8])
	if length < 8 {
		return 0, nil, fmt.Errorf("invalid PTP/IP packet length %d", length)
	}
	if n := int(length) - 8; n > 0 {
		payload = make([]byte, n)
		if _, err = io.ReadFull(conn, payload); err != nil {
			return 0, nil, fmt.Errorf("read packet payload: %w", err)
		}
	}
	return pktType, payload, nil
}

// ---- PTP/IP handshake packets ----

func (c *Client) sendInitCommandRequest(conn net.Conn) error {
	name := encodeUTF16LE("canon-proxy")
	// Payload: GUID(16) + Name(UTF-16LE, null-terminated) + ProtocolVersion(4)
	payload := make([]byte, 16+len(name)+4)
	copy(payload[0:16], clientGUID[:])
	copy(payload[16:], name)
	binary.LittleEndian.PutUint32(payload[16+len(name):], 0x00010000) // version 1.0
	log.Printf("level=debug component=canon msg=\"sending InitCommandRequest\" guid=%x name=%q", clientGUID, "canon-proxy")
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	return sendPacket(conn, ptpipInitCommandRequest, payload)
}

func (c *Client) recvInitCommandAck(conn net.Conn) (connNum uint32, err error) {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	pktType, payload, err := recvPacket(conn)
	if err != nil {
		return 0, fmt.Errorf("recv InitCommandAck: %w", err)
	}
	if pktType == ptpipInitFail {
		if len(payload) >= 4 {
			code := binary.LittleEndian.Uint32(payload[0:4])
			return 0, fmt.Errorf("camera rejected InitCommandRequest (reason 0x%08X)", code)
		}
		return 0, fmt.Errorf("camera rejected InitCommandRequest")
	}
	if pktType != ptpipInitCommandAck {
		return 0, fmt.Errorf("expected InitCommandAck (0x%02X), got 0x%02X", ptpipInitCommandAck, pktType)
	}
	if len(payload) < 4 {
		return 0, fmt.Errorf("InitCommandAck payload too short")
	}
	connNum = binary.LittleEndian.Uint32(payload[0:4])
	log.Printf("level=debug component=canon msg=\"InitCommandAck received\" conn_num=0x%08X", connNum)
	return connNum, nil
}

func (c *Client) sendInitEventRequest(conn net.Conn, connNum uint32) error {
	payload := make([]byte, 4)
	binary.LittleEndian.PutUint32(payload, connNum)
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	return sendPacket(conn, ptpipInitEventRequest, payload)
}

func (c *Client) recvInitEventAck(conn net.Conn) error {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	pktType, _, err := recvPacket(conn)
	if err != nil {
		return fmt.Errorf("recv InitEventAck: %w", err)
	}
	if pktType == ptpipInitFail {
		return fmt.Errorf("camera rejected InitEventRequest")
	}
	if pktType != ptpipInitEventAck {
		return fmt.Errorf("expected InitEventAck (0x%02X), got 0x%02X", ptpipInitEventAck, pktType)
	}
	return nil
}

// ---- PTP data structure parsing ----

// parsePTPUint32Array decodes a PTP array of uint32 values: count(4) + elements(4 each).
func parsePTPUint32Array(data []byte) []uint32 {
	if len(data) < 4 {
		return nil
	}
	count := binary.LittleEndian.Uint32(data[0:4])
	if count == 0 || int(count)*4+4 > len(data) {
		return nil
	}
	result := make([]uint32, count)
	for i := range result {
		result[i] = binary.LittleEndian.Uint32(data[4+i*4 : 8+i*4])
	}
	return result
}

// parseObjectInfo extracts the format code, filename and capture date from a PTP ObjectInfo dataset.
//
// PTP ObjectInfo layout (all little-endian):
//
//		StorageID(4) ObjectFormat(2) Protection(2) ObjSize(4)
//		ThumbFmt(2) ThumbSize(4) ThumbW(4) ThumbH(4)
//		ImgW(4) ImgH(4) ImgDepth(4) ParentObj(4)
//		AssocType(2) AssocDesc(4) SeqNum(4)
//		Filename<PTPString> CaptureDate<PTPString> …
func parseObjectInfo(data []byte) ptpObjectInfo {
	if len(data) < 6 {
		return ptpObjectInfo{}
	}
	format := binary.LittleEndian.Uint16(data[4:6])
	// Filename PTPString starts at byte offset 52 (sum of all fixed fields above).
	filename, nextOff := parsePTPString(data, 52)
	// CaptureDate PTPString immediately follows the filename string.
	captureDateStr, _ := parsePTPString(data, nextOff)
	return ptpObjectInfo{format: format, filename: filename, captureDate: parsePTPDate(captureDateStr)}
}

// parsePTPDate decodes a PTP date string in the form "YYYYMMDDThhmmss[.s][±hhmm]".
// Returns the zero Time if s is empty or malformed.
func parsePTPDate(s string) time.Time {
	if len(s) < 15 {
		return time.Time{}
	}
	// Strip fractional seconds and timezone offset; we only need date+time.
	base := s[:15]
	t, err := time.ParseInLocation("20060102T150405", base, time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

// parsePTPString decodes a PTP string at offset within data.
// Format: uint8 numChars + numChars×uint16 (UTF-16LE, includes null terminator).
// Returns the decoded string and the offset immediately after the string.
func parsePTPString(data []byte, offset int) (string, int) {
	if offset >= len(data) {
		return "", offset
	}
	numChars := int(data[offset])
	offset++
	if numChars == 0 {
		return "", offset
	}
	end := offset + numChars*2
	if end > len(data) {
		end = len(data)
	}
	words := make([]uint16, (end-offset)/2)
	for i := range words {
		words[i] = binary.LittleEndian.Uint16(data[offset+i*2 : offset+i*2+2])
	}
	// Remove null terminator if present.
	if len(words) > 0 && words[len(words)-1] == 0 {
		words = words[:len(words)-1]
	}
	return string(utf16.Decode(words)), end
}

// encodeUTF16LE encodes s as null-terminated UTF-16LE bytes (used in init handshake packets).
func encodeUTF16LE(s string) []byte {
	words := utf16.Encode(append([]rune(s), 0)) // include null terminator
	buf := make([]byte, len(words)*2)
	for i, w := range words {
		binary.LittleEndian.PutUint16(buf[i*2:], w)
	}
	return buf
}


