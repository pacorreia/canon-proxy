package canon

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

func TestIsVideoFilename(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		want bool
	}{
		{"MVI_0001.MOV", true},
		{"clip.mp4", true},
		{"footage.AVI", true},
		{"stream.MTS", true},
		{"IMG_0001.JPG", false},
		{"IMG_0001.CR3", false},
		{"IMG_0001.CR2", false},
		{"noextension", false},
		{"", false},
	}
	for _, tc := range cases {
		got := isVideoFilename(tc.name)
		if got != tc.want {
			t.Errorf("isVideoFilename(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestParsePTPDate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		s       string
		wantZero bool
		year    int
		month   time.Month
		day     int
		hour    int
		min     int
		sec     int
	}{
		{"20240315T142530", false, 2024, time.March, 15, 14, 25, 30},
		{"20231225T000000", false, 2023, time.December, 25, 0, 0, 0},
		// With fractional seconds and timezone (only first 15 chars used)
		{"20240315T142530.5+0100", false, 2024, time.March, 15, 14, 25, 30},
		// Too short
		{"20240315T1425", true, 0, 0, 0, 0, 0, 0},
		// Empty
		{"", true, 0, 0, 0, 0, 0, 0},
		// Malformed (letters in wrong place)
		{"YYYYMMDDTHHMMSS", true, 0, 0, 0, 0, 0, 0},
	}
	for _, tc := range cases {
		got := parsePTPDate(tc.s)
		if tc.wantZero {
			if !got.IsZero() {
				t.Errorf("parsePTPDate(%q) = %v, want zero", tc.s, got)
			}
			continue
		}
		if got.IsZero() {
			t.Errorf("parsePTPDate(%q) returned zero, want %d-%02d-%02d %02d:%02d:%02d",
				tc.s, tc.year, tc.month, tc.day, tc.hour, tc.min, tc.sec)
			continue
		}
		if got.Year() != tc.year || got.Month() != tc.month || got.Day() != tc.day ||
			got.Hour() != tc.hour || got.Minute() != tc.min || got.Second() != tc.sec {
			t.Errorf("parsePTPDate(%q) = %v, want %d-%02d-%02d %02d:%02d:%02d",
				tc.s, got, tc.year, tc.month, tc.day, tc.hour, tc.min, tc.sec)
		}
	}
}

func TestEncodeUTF16LE(t *testing.T) {
	t.Parallel()

	// Encode "AB" — should produce 0x41 0x00 0x42 0x00 0x00 0x00 (null-terminated UTF-16LE).
	got := encodeUTF16LE("AB")
	want := []byte{0x41, 0x00, 0x42, 0x00, 0x00, 0x00}
	if len(got) != len(want) {
		t.Fatalf("encodeUTF16LE(\"AB\") len=%d, want %d", len(got), len(want))
	}
	for i, b := range want {
		if got[i] != b {
			t.Errorf("byte[%d] = 0x%02x, want 0x%02x", i, got[i], b)
		}
	}

	// Round-trip: encode then decode via parsePTPString.
	original := "canon-proxy"
	encoded := encodeUTF16LE(original)
	// Prepend the numChars byte that parsePTPString expects.
	numChars := len([]rune(original)) + 1 // +1 for null terminator
	data := make([]byte, 1+len(encoded))
	data[0] = byte(numChars)
	copy(data[1:], encoded)
	decoded, _ := parsePTPString(data, 0)
	if decoded != original {
		t.Errorf("round-trip: got %q, want %q", decoded, original)
	}
}

func TestParsePTPString_Empty(t *testing.T) {
	t.Parallel()
	// numChars=0 → empty string
	data := []byte{0x00}
	got, next := parsePTPString(data, 0)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
	if next != 1 {
		t.Errorf("expected next offset=1, got %d", next)
	}
}

func TestParsePTPString_BeyondEnd(t *testing.T) {
	t.Parallel()
	// offset beyond data length → empty string, offset unchanged
	data := []byte{0x41, 0x00}
	got, next := parsePTPString(data, 99)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
	if next != 99 {
		t.Errorf("expected next=99, got %d", next)
	}
}

func TestParsePTPString_Truncated(t *testing.T) {
	t.Parallel()
	// numChars claims 5 chars but only 4 bytes of data follow → should not panic
	data := make([]byte, 1+4)
	data[0] = 5 // 5 chars = 10 bytes expected, only 4 present
	binary.LittleEndian.PutUint16(data[1:3], 0x0041) // 'A'
	binary.LittleEndian.PutUint16(data[3:5], 0x0042) // 'B'
	got, _ := parsePTPString(data, 0)
	// Should return something without panicking; exact value depends on truncation.
	_ = got
}

func TestParsePTPString_AsciiRoundTrip(t *testing.T) {
	t.Parallel()
	words := []string{"IMG_0042.JPG", "MVI_0001.MOV", "A"}
	for _, s := range words {
		runes := []rune(s)
		numChars := len(runes) + 1
		data := make([]byte, 1+numChars*2)
		data[0] = byte(numChars)
		for i, r := range runes {
			binary.LittleEndian.PutUint16(data[1+i*2:], uint16(r))
		}
		got, _ := parsePTPString(data, 0)
		if got != s {
			t.Errorf("round-trip %q: got %q", s, got)
		}
		if !strings.ContainsRune(got, rune(s[0])) {
			t.Errorf("sanity: %q not in decoded string %q", string(s[0]), got)
		}
	}
}
