package canon

// discover.go — LAN discovery of Canon PTP/IP cameras via TCP port scan with
// an ARP fast-path for already-known Canon devices, and passive mDNS advertisement
// for the Camera Connect protocol.
//
// SSDP (UPnP) was removed: Canon cameras only broadcast SSDP during the EOS
// Utility pairing wizard, making it unreliable once the camera is already paired.
// TCP scanning is the reliable alternative.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
	"github.com/pacorreia/canon-proxy/internal/logger"
)

// DiscoveredCamera is a PTP/IP camera found on the LAN.
type DiscoveredCamera struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
	Name string `json:"name"`
}

// DiscoverOptions controls the behaviour of DiscoverLAN.
// Reserved for future use; currently has no fields.
type DiscoverOptions struct{}

// DiscoverLAN finds Canon EOS cameras on the local network via TCP port scan
// (port 15740). An ARP cache fast-path is tried first: if the OS already knows
// the MAC address of a Canon device, only that host is probed.
//
// The TCP probe opens and immediately closes the connection — it does NOT send
// any PTP/IP data, so it will not trigger a pairing dialog on the camera.
func DiscoverLAN(ctx context.Context, _ DiscoverOptions) ([]DiscoveredCamera, error) {
	return discoverTCP(ctx)
}

// canonOUIs contains known Canon Inc. MAC address OUI prefixes (first 3 bytes).
// Canon cameras (EOS, PowerShot) use these prefixes on their WiFi interfaces.
// Source: IEEE OUI registry + observed devices.
var canonOUIs = []string{
	"74bfc0", // Canon Inc. (EOS cameras, observed)
	"e0b947", // Canon Inc.
	"14c2ef", // Canon Inc.
	"c80e77", // Canon Inc.
	"fc256e", // Canon Inc.
	"183452", // Canon Inc.
	"2e5bc8", // Canon Inc.
	"f40f24", // Canon Inc.
}

// discoverARP finds Canon cameras already in the ARP cache — fast, no scan needed.
// Returns the IPs of any ARP entries whose MAC matches a known Canon OUI.
func discoverARP() []string {
	data, err := os.ReadFile("/proc/net/arp")
	if err != nil {
		return nil
	}
	var ips []string
	lines := strings.Split(string(data), "\n")
	for _, line := range lines[1:] { // skip header
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		ip := fields[0]
		mac := strings.ToLower(strings.ReplaceAll(fields[3], ":", ""))
		if mac == "000000000000" || len(mac) < 6 {
			continue
		}
		for _, oui := range canonOUIs {
			if strings.HasPrefix(mac, oui) {
				logger.Debug("component=discover msg=\"ARP Canon device found\" ip=%s mac=%s", ip, mac)
				ips = append(ips, ip)
				break
			}
		}
	}
	return ips
}

// discoverTCP probes all hosts on every local subnet for PTP/IP port 15740.
// It first checks the ARP cache for Canon OUI MACs (fast), then falls back
// to probing all hosts on the subnet.
func discoverTCP(ctx context.Context) ([]DiscoveredCamera, error) {
	// Fast path: check ARP cache for Canon MACs already known to the OS.
	arpIPs := discoverARP()
	if len(arpIPs) > 0 {
		logger.Debug("component=discover msg=\"ARP fast path\" candidates=%d", len(arpIPs))
		var results []DiscoveredCamera
		for _, ip := range arpIPs {
			if cam, ok := probePTPIP(ctx, ip, 15740); ok {
				results = append(results, cam)
			}
		}
		if len(results) > 0 {
			return results, nil
		}
	}

	// Slow path: probe all hosts on the local subnet.
	addrs, err := localSubnetHosts()
	if err != nil {
		return nil, fmt.Errorf("TCP scan: enumerate subnets: %w", err)
	}

	const workers = 64
	sem := make(chan struct{}, workers)
	var (
		mu      sync.Mutex
		results []DiscoveredCamera
		wg      sync.WaitGroup
	)

	for _, ip := range addrs {
		if err := ctx.Err(); err != nil {
			break
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

// probePTPIP checks whether host:port accepts a TCP connection.
// It does NOT send an InitCommandRequest — doing so would trigger a pairing
// dialog on the camera screen. A plain TCP connect is sufficient to confirm
// the camera is present and reachable.
func probePTPIP(ctx context.Context, host string, port int) (DiscoveredCamera, bool) {
	addr := fmt.Sprintf("%s:%d", host, port)
	d := net.Dialer{Timeout: 800 * time.Millisecond}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return DiscoveredCamera{}, false
	}
	conn.Close()
	return DiscoveredCamera{IP: host, Port: port, Name: host}, true
}

// ServiceTypeFromMAC converts a MAC address (any format with colons, dashes or
// no separators) to the mDNS service type used by Canon Camera Connect.
//
// Canon EOS cameras store the paired phone's MAC during Bluetooth/WiFi pairing
// and only respond to mDNS advertisements whose service type encodes that MAC.
// Example: MAC "FC:9F:5E:D4:2C:8A" → service type "_FC9F5ED42C8A._tcp".
func ServiceTypeFromMAC(mac string) string {
	r := strings.NewReplacer(":", "", "-", "", ".", "")
	clean := strings.ToUpper(r.Replace(mac))
	return "_" + clean + "._tcp"
}

// AdvertiseAndWait advertises the proxy as a Camera Connect-compatible endpoint
// via mDNS and waits for a Canon EOS camera to connect.
//
// Canon EOS cameras store the MAC address of the paired phone during initial
// setup and only connect to an mDNS service whose type encodes that MAC
// (e.g. "_FC9F5ED42C8A._tcp" for a phone with MAC FC:9F:5E:D4:2C:8A).
// Use ServiceTypeFromMAC(phoneMACAddress) to build the correct service type.
func AdvertiseAndWait(ctx context.Context, svcType string) (*DiscoveredCamera, error) {
	if svcType == "" {
		return nil, fmt.Errorf("advertise: svcType is required (use ServiceTypeFromMAC with the paired phone MAC)")
	}

	localIP, err := localOutboundIP()
	if err != nil {
		return nil, fmt.Errorf("advertise: resolve local IP: %w", err)
	}

	ln, err := net.Listen("tcp4", localIP.String()+":0")
	if err != nil {
		return nil, fmt.Errorf("advertise: TCP listen: %w", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	instanceName := randomToken(9)
	nonce := randomToken(18)
	txtRecords := []string{
		"f=5240",
		"n=" + nonce,
		"IPv4=" + localIP.String(),
	}

	server, err := zeroconf.Register(
		instanceName,
		svcType,
		"local.",
		port,
		txtRecords,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("advertise: mDNS register: %w", err)
	}
	defer server.Shutdown()

	logger.Info("component=discover msg=\"advertising for camera\" svc=%s ip=%s port=%d",
		svcType, localIP, port)

	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		conn, err := ln.Accept()
		ch <- result{conn, err}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("advertise: accept: %w", r.err)
		}
		defer r.conn.Close()
		cameraIP := r.conn.RemoteAddr().(*net.TCPAddr).IP.String()
		logger.Info("component=discover msg=\"camera connected\" ip=%s", cameraIP)

		buf := make([]byte, 512)
		r.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, _ := r.conn.Read(buf)
		if n > 0 {
			logger.Debug("component=discover msg=\"camera initial payload\" len=%d hex=%x", n, buf[:n])
			fmt.Printf("Camera sent %d bytes: %x\n  text: %q\n", n, buf[:n], buf[:n])
		}

		return &DiscoveredCamera{
			IP:   cameraIP,
			Port: 15740,
			Name: cameraIP,
		}, nil
	}
}

// localOutboundIP returns the local IP address used for outbound connections.
func localOutboundIP() (net.IP, error) {
	conn, err := net.Dial("udp4", "8.8.8.8:80")
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP, nil
}

// randomToken returns a URL-safe base64 random string of approximately n bytes of entropy.
func randomToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
