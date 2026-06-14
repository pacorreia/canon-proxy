package canon

// ptp.go — PTP/IP protocol constants and pure binary encoding/decoding functions.
// Nothing in this file depends on network I/O or application state.

import (
	"encoding/binary"
	"strings"
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

// ptpObjectInfo holds the subset of PTP ObjectInfo fields used by this implementation.
type ptpObjectInfo struct {
	format       uint16
	parentHandle uint32 // PTP ObjectInfo.ParentObject field (byte offset 32)
	filename     string
	captureDate  time.Time // zero value if not present or unparseable
}

// isVideoFilename reports whether the filename looks like a video based on its extension.
func isVideoFilename(name string) bool {
	n := strings.ToUpper(name)
	return strings.HasSuffix(n, ".MOV") || strings.HasSuffix(n, ".MP4") ||
		strings.HasSuffix(n, ".AVI") || strings.HasSuffix(n, ".MTS")
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

// parsePTPUint32Array decodes a PTP array of uint32 values: count(4) + elements(4 each).
func parsePTPUint32Array(data []byte) []uint32 {
	if len(data) < 4 {
		return nil
	}
	count := binary.LittleEndian.Uint32(data[0:4])
	// Guard against malformed data: reject implausibly large counts and
	// validate that the buffer actually contains count*4 bytes after the header.
	// The explicit count > maxSafeCount check prevents integer overflow on
	// 32-bit platforms before the multiplication.
	const maxSafeCount = 1 << 20 // 1 M entries
	if count == 0 || count > maxSafeCount || int(count)*4+4 > len(data) {
		return nil
	}
	result := make([]uint32, int(count))
	for i := range result {
		result[i] = binary.LittleEndian.Uint32(data[4+i*4 : 8+i*4])
	}
	return result
}

// parseObjectInfo extracts the format code, filename and capture date from a PTP ObjectInfo dataset.
//
// PTP ObjectInfo layout (all little-endian):
//
//	StorageID(4) ObjectFormat(2) Protection(2) ObjSize(4)
//	ThumbFmt(2) ThumbSize(4) ThumbW(4) ThumbH(4)
//	ImgW(4) ImgH(4) ImgDepth(4) ParentObj(4)
//	AssocType(2) AssocDesc(4) SeqNum(4)
//	Filename<PTPString> CaptureDate<PTPString> …
func parseObjectInfo(data []byte) ptpObjectInfo {
	if len(data) < 6 {
		return ptpObjectInfo{}
	}
	format := binary.LittleEndian.Uint16(data[4:6])
	var parentHandle uint32
	if len(data) >= 36 {
		parentHandle = binary.LittleEndian.Uint32(data[32:36])
	}
	// Filename PTPString starts at byte offset 52 (sum of all fixed fields above).
	// Guard against cameras that send a truncated ObjectInfo dataset.
	if len(data) <= 52 {
		return ptpObjectInfo{format: format, parentHandle: parentHandle}
	}
	filename, nextOff := parsePTPString(data, 52)
	// CaptureDate PTPString immediately follows the filename string.
	captureDateStr, _ := parsePTPString(data, nextOff)
	return ptpObjectInfo{format: format, parentHandle: parentHandle, filename: filename, captureDate: parsePTPDate(captureDateStr)}
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
