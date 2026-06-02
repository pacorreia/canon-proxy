package canon

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

// DiscoveredCamera is a PTP/IP camera found on the LAN.
type DiscoveredCamera struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
	Name string `json:"name"` // friendly name from Init Command Ack, or IP if unavailable
}

// DiscoverLAN probes all hosts on every local subnet for PTP/IP port 15740.
// It respects ctx for cancellation. Concurrency controls how many parallel
// TCP dials are in flight (default 64).
func DiscoverLAN(ctx context.Context) ([]DiscoveredCamera, error) {
	addrs, err := localSubnetHosts()
	if err != nil {
		return nil, fmt.Errorf("discover: enumerate subnets: %w", err)
	}

	const workers = 64
	sem := make(chan struct{}, workers)
	var (
		mu      sync.Mutex
		results []DiscoveredCamera
		wg      sync.WaitGroup
	)

	for _, ip := range addrs {
		select {
		case <-ctx.Done():
			break
		default:
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(ip string) {
			defer wg.Done()
			defer func() { <-sem }()
			if cam, ok := probePTPIP(ctx, ip, 15740); ok {
				mu.Lock()
				results = append(results, cam)
				mu.Unlock()
			}
		}(ip)
	}
	wg.Wait()
	return results, nil
}

// localSubnetHosts returns all host addresses across every local IPv4 subnet.
func localSubnetHosts() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var hosts []string
	seen := make(map[string]bool)
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip, ipNet, err := net.ParseCIDR(addr.String())
			if err != nil {
				continue
			}
			if ip.To4() == nil {
				continue // skip IPv6
			}
			// Expand all hosts in the /24-or-smaller subnet (cap at 1024 hosts).
			ones, bits := ipNet.Mask.Size()
			if bits-ones > 10 {
				// Subnet too large (e.g. /8); limit scan to local /24.
				ones = 24
				ipNet = &net.IPNet{
					IP:   ip.Mask(net.CIDRMask(24, 32)),
					Mask: net.CIDRMask(24, 32),
				}
			}
			_ = ones
			for host := ipNet.IP.Mask(ipNet.Mask); ipNet.Contains(host); inc(host) {
				h := host.String()
				if !seen[h] {
					seen[h] = true
					hosts = append(hosts, h)
				}
			}
		}
	}
	return hosts, nil
}

func inc(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

// probePTPIP tries to open a TCP connection to host:port and performs a minimal
// PTP/IP Init Command Request handshake. Returns the camera name on success.
func probePTPIP(ctx context.Context, host string, port int) (DiscoveredCamera, bool) {
	addr := fmt.Sprintf("%s:%d", host, port)
	d := net.Dialer{Timeout: 800 * time.Millisecond}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return DiscoveredCamera{}, false
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(1 * time.Second))

	// Send PTP/IP Init Command Request (type 0x01).
	// Packet: length(4) + type(4) + GUID(16) + friendlyName(UTF-16LE, "canon-proxy\0") + version(4)
	name := probeEncodeUTF16LE("canon-proxy")
	pktLen := uint32(4 + 4 + 16 + len(name) + 4)
	pkt := make([]byte, pktLen)
	le := func(b []byte, off int, v uint32) { b[off] = byte(v); b[off+1] = byte(v >> 8); b[off+2] = byte(v >> 16); b[off+3] = byte(v >> 24) }
	le(pkt, 0, pktLen)
	le(pkt, 4, 0x01) // Init Command Request
	copy(pkt[8:24], clientGUID[:])
	copy(pkt[24:], name)
	le(pkt, 24+len(name), 0x00010000) // version 1.0

	if _, err := conn.Write(pkt); err != nil {
		return DiscoveredCamera{}, false
	}

	// Read response header: length(4) + type(4).
	hdr := make([]byte, 8)
	if _, err := readFull(conn, hdr); err != nil {
		// If we successfully connected and sent the PTP/IP Init Command Request
		// but the camera closed/reset the connection (e.g. already paired to another
		// client), it's still a PTP/IP camera — report it as busy.
		return DiscoveredCamera{IP: host, Port: port, Name: host + " (in use)"}, true
	}
	respType := uint32(hdr[4]) | uint32(hdr[5])<<8 | uint32(hdr[6])<<16 | uint32(hdr[7])<<24
	respLen := uint32(hdr[0]) | uint32(hdr[1])<<8 | uint32(hdr[2])<<16 | uint32(hdr[3])<<24

	// 0x02 = Init Command Ack, 0x05 = Init Fail — both mean a PTP/IP camera responded.
	if respType != 0x02 && respType != 0x05 {
		return DiscoveredCamera{}, false
	}

	camName := host
	// If Init Command Ack, try to parse the camera friendly name.
	// Ack payload: GUID(16) + name(UTF-16LE) + version(4)
	if respType == 0x02 && respLen > 28 {
		payload := make([]byte, int(respLen)-8)
		if _, err := readFull(conn, payload); err == nil && len(payload) > 16 {
			camName = probeDecodeUTF16LE(payload[16:])
			if camName == "" {
				camName = host
			}
		}
	}

	return DiscoveredCamera{IP: host, Port: port, Name: camName}, true
}

func readFull(conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func probeEncodeUTF16LE(s string) []byte {
	runes := []rune(s + "\x00")
	b := make([]byte, len(runes)*2)
	for i, r := range runes {
		b[i*2] = byte(r)
		b[i*2+1] = byte(r >> 8)
	}
	return b
}

func probeDecodeUTF16LE(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	u16 := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		v := uint16(b[i]) | uint16(b[i+1])<<8
		if v == 0 {
			break
		}
		u16 = append(u16, v)
	}
	runes := make([]rune, len(u16))
	for i, v := range u16 {
		runes[i] = rune(v)
	}
	return string(runes)
}
