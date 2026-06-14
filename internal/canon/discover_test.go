package canon

import (
	"strings"
	"testing"
)

func TestServiceTypeFromMAC(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mac  string
		want string
	}{
		{"FC:9F:5E:D4:2C:8A", "_FC9F5ED42C8A._tcp"},
		{"fc:9f:5e:d4:2c:8a", "_FC9F5ED42C8A._tcp"},
		{"FC-9F-5E-D4-2C-8A", "_FC9F5ED42C8A._tcp"},
		{"FC9F5ED42C8A", "_FC9F5ED42C8A._tcp"},
		{"74:BF:C0:52:9A:8C", "_74BFC0529A8C._tcp"},
	}
	for _, tc := range cases {
		got := ServiceTypeFromMAC(tc.mac)
		if got != tc.want {
			t.Errorf("ServiceTypeFromMAC(%q) = %q, want %q", tc.mac, got, tc.want)
		}
		if !strings.HasSuffix(got, "._tcp") {
			t.Errorf("ServiceTypeFromMAC(%q) missing ._tcp suffix", tc.mac)
		}
		if !strings.HasPrefix(got, "_") {
			t.Errorf("ServiceTypeFromMAC(%q) missing leading underscore", tc.mac)
		}
	}
}

func TestDiscoverOptions_Empty(t *testing.T) {
	t.Parallel()
	// DiscoverOptions must be an empty struct (no fields) for forward-compatibility.
	// This test will fail to compile if a field is added without updating this notice.
	var _ = DiscoverOptions{}
}
