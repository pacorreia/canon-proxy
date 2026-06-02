package canon

import (
	"encoding/binary"
	"testing"
)

func TestParseHandle(t *testing.T) {
	t.Parallel()
	cases := []struct {
		url     string
		want    uint32
		wantErr bool
	}{
		{"ptpip://192.168.2.151:15740/12345", 12345, false},
		{"ptpip://192.168.2.151:15740/0", 0, false},
		{"ptpip://cam:15740/4294967295", 4294967295, false},
		{"no-slash", 0, true},
		{"ptpip://cam/notanumber", 0, true},
	}
	for _, tc := range cases {
		got, err := parseHandle(tc.url)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseHandle(%q): expected error, got nil", tc.url)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseHandle(%q): unexpected error: %v", tc.url, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseHandle(%q) = %d, want %d", tc.url, got, tc.want)
		}
	}
}

func TestHandleURL(t *testing.T) {
	t.Parallel()
	c := NewClient("192.168.2.151", 15740)
	got := c.handleURL(999)
	want := "ptpip://192.168.2.151:15740/999"
	if got != want {
		t.Errorf("handleURL(999) = %q, want %q", got, want)
	}
}

func TestHandleURLRoundTrip(t *testing.T) {
	t.Parallel()
	c := NewClient("cam.local", 15740)
	for _, handle := range []uint32{0, 1, 65535, 4294967295} {
		url := c.handleURL(handle)
		got, err := parseHandle(url)
		if err != nil {
			t.Errorf("parseHandle(%q): %v", url, err)
			continue
		}
		if got != handle {
			t.Errorf("round-trip handle %d: got %d", handle, got)
		}
	}
}

func TestIsImageFormat(t *testing.T) {
	t.Parallel()
	cases := []struct {
		format uint16
		want   bool
	}{
		{0x3000, false}, // Undefined
		{0x3001, false}, // Association (folder)
		{0x3800, true},  // Undefined image
		{0x3801, true},  // EXIF/JPEG
		{0x380D, true},  // TIFF/EP
		{0x3FFE, true},  // other standard image
		{0xB101, true},  // Canon CRW
		{0xB103, true},  // Canon CRW3/CR2
		{0xB108, true},  // Canon CR3
		{0x2000, false}, // not an image code
		{0x7FFF, false}, // not an image code
	}
	for _, tc := range cases {
		got := isImageFormat(tc.format)
		if got != tc.want {
			t.Errorf("isImageFormat(0x%04X) = %v, want %v", tc.format, got, tc.want)
		}
	}
}

func TestParsePTPUint32Array(t *testing.T) {
	t.Parallel()

	// Build a valid PTP array: count=3, values=[10, 20, 30]
	data := make([]byte, 4+3*4)
	binary.LittleEndian.PutUint32(data[0:], 3)
	binary.LittleEndian.PutUint32(data[4:], 10)
	binary.LittleEndian.PutUint32(data[8:], 20)
	binary.LittleEndian.PutUint32(data[12:], 30)

	got := parsePTPUint32Array(data)
	if len(got) != 3 {
		t.Fatalf("expected 3 elements, got %d", len(got))
	}
	for i, want := range []uint32{10, 20, 30} {
		if got[i] != want {
			t.Errorf("element %d: got %d, want %d", i, got[i], want)
		}
	}
}

func TestParsePTPUint32ArrayEmpty(t *testing.T) {
	t.Parallel()
	data := make([]byte, 4) // count=0
	if got := parsePTPUint32Array(data); len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestParsePTPString(t *testing.T) {
	t.Parallel()

	// Build "IMG_0001.JPG" as a PTP string (UTF-16LE, null-terminated).
	s := "IMG_0001.JPG"
	runes := []rune(s)
	numChars := len(runes) + 1 // +1 for null terminator
	data := make([]byte, 1+numChars*2)
	data[0] = byte(numChars)
	for i, r := range runes {
		binary.LittleEndian.PutUint16(data[1+i*2:], uint16(r))
	}
	// null terminator already zero

	got, _ := parsePTPString(data, 0)
	if got != s {
		t.Errorf("parsePTPString = %q, want %q", got, s)
	}
}

func TestParseObjectInfoFilename(t *testing.T) {
	t.Parallel()

	// Build a minimal ObjectInfo with a recognisable format and filename.
	const filename = "IMG_0042.JPG"
	runes := []rune(filename)
	numChars := len(runes) + 1 // include null
	strBytes := make([]byte, 1+numChars*2)
	strBytes[0] = byte(numChars)
	for i, r := range runes {
		binary.LittleEndian.PutUint16(strBytes[1+i*2:], uint16(r))
	}

	// Pad fixed fields up to offset 52, then append PTPString.
	data := make([]byte, 52+len(strBytes))
	binary.LittleEndian.PutUint16(data[4:6], 0x3801) // EXIF/JPEG
	copy(data[52:], strBytes)

	info := parseObjectInfo(data)
	if info.format != 0x3801 {
		t.Errorf("format = 0x%04X, want 0x3801", info.format)
	}
	if info.filename != filename {
		t.Errorf("filename = %q, want %q", info.filename, filename)
	}
}

