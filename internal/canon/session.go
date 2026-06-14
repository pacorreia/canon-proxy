package canon

// session.go — PTP/IP connection lifecycle and protocol handshake.
// Responsible for establishing (and re-establishing) the TCP command+event channel
// pair, performing the PTP/IP Init handshake, opening the PTP session, and
// sending Canon EOS-specific mode-setup commands.

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/pacorreia/canon-proxy/internal/logger"
)

func (c *Client) ensureConnected(ctx context.Context) error {
	if c.cmdConn != nil {
		return nil
	}
	// Fail immediately if the context is already done before attempting any I/O.
	if err := ctx.Err(); err != nil {
		return err
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
		logger.Warn("component=canon msg=\"reconnect failed, retrying\" attempt=%d backoff=%s err=%q", attempt, backoff, lastErr)
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

	setTCPKeepalive(cmdConn)
	setTCPKeepalive(evtConn)
	c.cmdConn = cmdConn
	c.evtConn = evtConn
	c.txID = 0

	if err := c.openSession(ctx); err != nil {
		c.closeConns()
		return fmt.Errorf("open PTP session: %w", err)
	}

	// SetRemoteMode + SetEventMode are Canon-specific extensions required after
	// OpenSession to put the camera into remote-control mode and enable event
	// reporting. Per the PTP/IP spec (CIPA DC-X005) and observed behaviour with
	// Canon EOS cameras, these must follow OpenSession.
	if err := c.canonEOSInit(ctx); err != nil {
		c.closeConns()
		return fmt.Errorf("Canon EOS init (remote/event mode): %w", err)
	}

	// The EOS 2000D floods dozens of property-change events after OpenSession
	// and is not ready to handle PTP commands for ~5 seconds. Without this
	// delay, the first GetObjectInfo call gets a connection reset.
	// Ref: https://github.com/gphoto/gphoto2/issues/382
	logger.Info("component=canon msg=\"PTP session open, waiting for camera to settle\" remote=%q", cmdConn.RemoteAddr())
	select {
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
		c.closeConns()
		return ctx.Err()
	}
	logger.Info("component=canon msg=\"camera ready\" remote=%q", cmdConn.RemoteAddr())

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
// the connection is truly closed.
//
// recvPacket sets a 30-second read deadline. During quiet periods the camera
// sends no events, so the deadline fires. We treat that as a normal idle case
// and keep the goroutine alive — exiting only on real connection errors.
func (c *Client) drainEvents(conn net.Conn) {
	for {
		_, _, err := recvPacket(conn)
		if err == nil {
			continue
		}
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			continue // camera idle; keep the drain goroutine alive
		}
		return // connection closed or reset
	}
}

// setTCPKeepalive enables TCP keepalive on conn so that silent WiFi drops or
// NAT evictions are detected within roughly 15 s rather than only on the next
// read/write attempt.
func setTCPKeepalive(conn net.Conn) {
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(15 * time.Second)
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
		logger.Info("component=canon msg=\"listening for camera\" addr=%q", c.listenAddr)
	}

	logger.Info("component=canon msg=\"waiting for camera to connect\" addr=%q", c.listenAddr)

	// Accept command channel — camera dials us first.
	cmdConn, err := acceptWithContext(ctx, c.listener)
	if err != nil {
		return fmt.Errorf("accept command channel: %w", err)
	}
	logger.Info("component=canon msg=\"camera connected (command channel)\" remote=%q", cmdConn.RemoteAddr())

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
	logger.Info("component=canon msg=\"camera connected (event channel)\" remote=%q", evtConn.RemoteAddr())

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

	setTCPKeepalive(cmdConn)
	setTCPKeepalive(evtConn)
	c.cmdConn = cmdConn
	c.evtConn = evtConn
	c.txID = 0

	if err := c.openSession(ctx); err != nil {
		c.closeConns()
		return fmt.Errorf("open PTP session: %w", err)
	}

	if err := c.canonEOSInit(ctx); err != nil {
		c.closeConns()
		return fmt.Errorf("Canon EOS init (remote/event mode): %w", err)
	}

	logger.Info("component=canon msg=\"PTP session open, waiting for camera to settle\" remote=%q", cmdConn.RemoteAddr())
	select {
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
		c.closeConns()
		return ctx.Err()
	}
	logger.Info("component=canon msg=\"camera ready\" remote=%q", cmdConn.RemoteAddr())
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

// ---- PTP/IP handshake packets ----

func (c *Client) sendInitCommandRequest(conn net.Conn) error {
	name := encodeUTF16LE("canon-proxy")
	// Payload: GUID(16) + Name(UTF-16LE, null-terminated) + ProtocolVersion(4)
	payload := make([]byte, 16+len(name)+4)
	guid := readClientGUID()
	copy(payload[0:16], guid[:])
	copy(payload[16:], name)
	binary.LittleEndian.PutUint32(payload[16+len(name):], 0x00010000) // version 1.0
	logger.Debug("component=canon msg=\"sending InitCommandRequest\" guid=%x name=%q", guid, "canon-proxy")
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
	// InitCommandAck payload layout (CIPA DC-X005 §4.2.3):
	//   ConnectionNumber(4) + CameraGUID(16) + CameraName(UTF-16LE, null-terminated) + PTPVersion(4)
	connNum = binary.LittleEndian.Uint32(payload[0:4])
	var cameraGUID [16]byte
	var cameraName string
	if len(payload) >= 20 {
		copy(cameraGUID[:], payload[4:20])
		cameraName, _ = parsePTPString(payload, 20)
	}
	logger.Debug("component=canon msg=\"InitCommandAck received\" conn_num=0x%08X camera_guid=%x camera_name=%q", connNum, cameraGUID, cameraName)
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
