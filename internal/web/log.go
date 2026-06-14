package web

// log.go — live log broadcast infrastructure.
// Fans log lines written to LogBroadcaster out to all active /api/logs SSE clients
// and maintains a ring-buffer of recent lines for new subscribers.

import (
	"strings"
	"sync"
)

// logClient is a subscriber to the live log stream.
type logClient struct {
	ch chan string
}

// LogBroadcaster is an io.Writer that fans log lines out to all connected /api/logs SSE clients.
// Wire it as a tee on log.SetOutput so every log.Printf line reaches the browser.
type LogBroadcaster struct {
	mu      sync.Mutex
	clients map[*logClient]struct{}
	buf     []string // ring buffer of recent lines
}

// NewLogBroadcaster returns an initialised LogBroadcaster.
func NewLogBroadcaster() *LogBroadcaster { return &LogBroadcaster{clients: make(map[*logClient]struct{})} }

const logRingSize = 200

// Write implements io.Writer. Each call is treated as one log line.
func (lb *LogBroadcaster) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n")
	lb.mu.Lock()
	if len(lb.buf) >= logRingSize {
		lb.buf = lb.buf[1:]
	}
	lb.buf = append(lb.buf, line)
	for c := range lb.clients {
		select {
		case c.ch <- line:
		default: // slow client: drop the line rather than block
		}
	}
	lb.mu.Unlock()
	return len(p), nil
}

func (lb *LogBroadcaster) subscribe() (*logClient, []string) {
	c := &logClient{ch: make(chan string, 64)}
	lb.mu.Lock()
	snap := make([]string, len(lb.buf))
	copy(snap, lb.buf)
	lb.clients[c] = struct{}{}
	lb.mu.Unlock()
	return c, snap
}

func (lb *LogBroadcaster) unsubscribe(c *logClient) {
	lb.mu.Lock()
	delete(lb.clients, c)
	lb.mu.Unlock()
}
