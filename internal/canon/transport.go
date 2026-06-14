package canon

// transport.go — raw PTP/IP packet I/O and per-transaction command/data channel operations.
// Responsible for framing bytes on the wire; knows nothing about connection lifecycle or
// PTP object semantics.

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/pacorreia/canon-proxy/internal/logger"
)

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
	// 30 s is enough for the 8-byte header to arrive.
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
		// For large payloads (e.g. image data), extend the deadline so that a
		// slow WiFi connection does not cause a premature i/o timeout.
		// Allow at least 30 s plus 1 s per 50 KB (≈ 400 Kbit/s floor).
		payloadDeadline := 30*time.Second + time.Duration(n/50_000)*time.Second
		_ = conn.SetReadDeadline(time.Now().Add(payloadDeadline))
		payload = make([]byte, n)
		if _, err = io.ReadFull(conn, payload); err != nil {
			return 0, nil, fmt.Errorf("read packet payload: %w", err)
		}
	}
	return pktType, payload, nil
}

func (c *Client) nextTxID() uint32 {
	c.txID++
	return c.txID
}

// sendCmdRequest sends a PTP/IP OperationRequest packet.
// hostSendsData=true sets DataPhase=2 (initiator sends data to device, e.g. SetPropValue).
// hostSendsData=false sets DataPhase=1 (device sends data to initiator, or no data phase).
// Per PTP/IP spec CIPA DC-X005: DataPhase 0x01 covers both "no data" and "data-in" cases;
// DataPhase 0x02 is used only when the initiator (host) is sending data to the device.
func (c *Client) sendCmdRequest(opcode uint16, txID uint32, hostSendsData bool, params ...uint32) error {
	dp := uint32(1)
	if hostSendsData {
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
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pktType, payload, err := recvPacket(c.cmdConn)
		if err != nil {
			return nil, fmt.Errorf("recv response: %w", err)
		}
		logger.Debug("component=canon msg=\"recvResponse pkt\" type=0x%02X len=%d txID=%d", pktType, len(payload), txID)
		switch pktType {
		case ptpipCmdResponse:
			if len(payload) < 6 {
				return nil, fmt.Errorf("response packet too short (%d bytes)", len(payload))
			}
			rc := binary.LittleEndian.Uint16(payload[0:2])
			rTxID := binary.LittleEndian.Uint32(payload[2:6])
			logger.Debug("component=canon msg=\"response code\" rc=0x%04X rTxID=%d", rc, rTxID)
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
			logger.Debug("component=canon msg=\"unexpected packet in recvResponse\" type=0x%02X len=%d", pktType, len(payload))
		}
	}
}

// recvData reads StartDataPacket + optional DataPackets + EndDataPacket and
// returns the assembled payload bytes.
func (c *Client) recvData(ctx context.Context, txID uint32) ([]byte, error) {
	var buf []byte

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pktType, payload, err := recvPacket(c.cmdConn)
		if err != nil {
			return nil, fmt.Errorf("recv data: %w", err)
		}
		switch pktType {
		case ptpipStartDataPacket:
			// payload: TxID(4) + TotalDataLength(8)
			if len(payload) >= 4 {
				pktTxID := binary.LittleEndian.Uint32(payload[0:4])
				if pktTxID != txID {
					logger.Debug("component=canon msg=\"recvData: stale StartDataPacket\" expected=%d got=%d\"", txID, pktTxID)
					continue
				}
			}
			if len(payload) >= 12 {
				totalLen := binary.LittleEndian.Uint64(payload[4:12])
				if totalLen > 0 {
					if totalLen > uint64(int(^uint(0)>>1)) {
						return nil, fmt.Errorf("data payload too large: %d", totalLen)
					}
					buf = make([]byte, 0, int(totalLen))
				}
			}
		case ptpipDataPacket:
			// payload: TxID(4) + Data
			if len(payload) >= 4 {
				pktTxID := binary.LittleEndian.Uint32(payload[0:4])
				if pktTxID != txID {
					logger.Debug("component=canon msg=\"recvData: stale DataPacket\" expected=%d got=%d\"", txID, pktTxID)
					continue
				}
				buf = append(buf, payload[4:]...)
			}
		case ptpipEndDataPacket:
			// payload: TxID(4) + Data (last chunk)
			if len(payload) >= 4 {
				pktTxID := binary.LittleEndian.Uint32(payload[0:4])
				if pktTxID != txID {
					logger.Debug("component=canon msg=\"recvData: stale EndDataPacket\" expected=%d got=%d\"", txID, pktTxID)
					continue
				}
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
			logger.Debug("component=canon msg=\"unexpected packet in recvData\" type=0x%02X len=%d", pktType, len(payload))
		}
	}
}
