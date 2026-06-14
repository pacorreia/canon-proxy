package canon

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pacorreia/canon-proxy/internal/logger"
)

const maxSeenImages = 50_000

// Poller repeatedly enumerates the camera for new images and emits them on a channel.
//
// After the first successful full scan it switches to delta mode: only handles not yet seen
// are queried via GetObjectInfo, which dramatically reduces PTP round-trips when the camera
// already has many images.
//
// Connection drops are handled with an exponential back-off: on error the poller waits
// progressively longer before retrying (up to maxErrBackoff) and resets to zero after
// a successful poll.
type Poller struct {
	client   *Client
	interval time.Duration
	autoEnabled bool

	mu            sync.Mutex
	seen          map[string]struct{} // URL → already emitted
	seenKeys      []string            // for ring-buffer eviction
	imageHandles  map[uint32]struct{} // handle → known image (skip GetObjectInfo)
	folderHandles map[uint32]struct{} // handle → known folder (always recurse)
	initialDone   bool                // true after first successful full scan

	triggerCh chan struct{} // manual poll trigger (buffered 1)
	configCh  chan struct{} // interval/enabled changed (buffered 1)

	cameraConnected atomic.Bool // true after a successful pollOnce
}

// NewPoller creates a Poller that polls the camera at the given interval.
func NewPoller(client *Client, interval time.Duration) *Poller {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Poller{
		client:        client,
		interval:      interval,
		autoEnabled:   true,
		seen:          make(map[string]struct{}),
		imageHandles:  make(map[uint32]struct{}),
		folderHandles: make(map[uint32]struct{}),
		triggerCh:     make(chan struct{}, 1),
		configCh:      make(chan struct{}, 1),
	}
}

// TriggerNow forces an immediate poll without waiting for the next scheduled interval.
func (p *Poller) TriggerNow() {
	select {
	case p.triggerCh <- struct{}{}:
	default:
	}
}

// SetEnabled enables or disables automatic periodic polling.
func (p *Poller) SetEnabled(v bool) {
	p.mu.Lock()
	p.autoEnabled = v
	p.mu.Unlock()
	select {
	case p.configCh <- struct{}{}:
	default:
	}
}

// SetInterval changes the automatic polling interval dynamically.
func (p *Poller) SetInterval(d time.Duration) {
	if d < time.Second {
		d = time.Second
	}
	p.mu.Lock()
	p.interval = d
	p.mu.Unlock()
	select {
	case p.configCh <- struct{}{}:
	default:
	}
}

// CameraConnected reports whether the last poll attempt succeeded.
func (p *Poller) CameraConnected() bool {
	return p.cameraConnected.Load()
}

// AutoEnabled reports whether automatic periodic polling is enabled.
func (p *Poller) AutoEnabled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.autoEnabled
}

// Interval returns the current automatic polling interval.
func (p *Poller) Interval() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.interval
}

// EvictHandle removes a handle from the image cache so it can be re-discovered.
// Call this after deleting an image from the camera so the slot can be reused.
func (p *Poller) EvictHandle(url string) {
	handle, err := parseHandle(url)
	if err != nil {
		return
	}
	p.mu.Lock()
	delete(p.imageHandles, handle)
	delete(p.seen, url)
	p.mu.Unlock()
}

// Run starts polling and returns a channel on which new images are sent.
// The channel is closed when ctx is cancelled.
func (p *Poller) Run(ctx context.Context) <-chan Image {
	out := make(chan Image)

	go func() {
		defer close(out)

		const maxErrBackoff = 60 * time.Second
		errBackoff := time.Duration(0)

		p.mu.Lock()
		curInterval := p.interval
		curEnabled := p.autoEnabled
		p.mu.Unlock()

		ticker := time.NewTicker(curInterval)
		if !curEnabled {
			ticker.Stop()
		}
		defer ticker.Stop()

		// resetTicker reads the latest config and resets the ticker accordingly.
		resetTicker := func() {
			p.mu.Lock()
			curInterval = p.interval
			curEnabled = p.autoEnabled
			p.mu.Unlock()
			ticker.Stop()
			if curEnabled {
				ticker.Reset(curInterval)
			}
		}

		for {
			// Wait out any error back-off before the next attempt.
			if errBackoff > 0 {
				logger.Info("component=poller msg=\"waiting before retry\" backoff=%s", errBackoff)
				select {
				case <-ctx.Done():
					return
				case <-time.After(errBackoff):
				case <-p.triggerCh:
					// manual trigger overrides back-off
				}
			}

			if err := p.pollOnce(ctx, out); err != nil {
				// Double the back-off on each consecutive failure.
				if errBackoff == 0 {
					errBackoff = 5 * time.Second
				} else if errBackoff < maxErrBackoff {
					errBackoff *= 2
					if errBackoff > maxErrBackoff {
						errBackoff = maxErrBackoff
					}
				}
				logger.Warn("component=poller msg=\"poll failed\" backoff=%s", errBackoff)
				continue // skip the normal interval wait; errBackoff handles pacing
			}

			// Successful poll — reset error back-off and wait for the next trigger.
			errBackoff = 0
			resetTicker()

		waitNext:
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					break waitNext
				case <-p.triggerCh:
					break waitNext
				case <-p.configCh:
					resetTicker() // apply new interval/enabled; stay in wait loop
				}
			}
		}
	}()

	return out
}

// pollOnce performs one scan of the camera. Returns a non-nil error only on
// camera/connection failures (context cancellation returns nil so the goroutine
// exits via ctx.Done() in the select).
func (p *Poller) pollOnce(ctx context.Context, out chan<- Image) error {
	p.mu.Lock()
	initialDone := p.initialDone
	// Snapshot the known-handle maps for the delta query (without holding the lock during I/O).
	knownImg := make(map[uint32]struct{}, len(p.imageHandles))
	for h := range p.imageHandles {
		knownImg[h] = struct{}{}
	}
	knownFld := make(map[uint32]struct{}, len(p.folderHandles))
	for h := range p.folderHandles {
		knownFld[h] = struct{}{}
	}
	p.mu.Unlock()

	mode := "delta"
	if !initialDone {
		mode = "full"
	}
	logger.Info("component=poller msg=\"poll started\" mode=%s known_images=%d", mode, len(knownImg))

	var (
		images     []Image
		newFolders map[uint32]struct{}
		err        error
	)

	if !initialDone {
		// First run: full scan.
		images, err = p.client.ListImages(ctx)
		newFolders = make(map[uint32]struct{})
	} else {
		// Subsequent runs: delta scan — only new handles incur GetObjectInfo.
		images, newFolders, err = p.client.ListImagesDelta(ctx, knownImg, knownFld)
	}

	if err != nil {
		if ctx.Err() != nil {
			return nil // context cancelled, not a camera error
		}
		p.cameraConnected.Store(false)
		logger.Error("component=poller msg=\"failed to list images\" err=%q", err)
		return err
	}
	p.cameraConnected.Store(true)

	p.mu.Lock()
	if !p.initialDone {
		p.initialDone = true
	}
	for h := range newFolders {
		p.folderHandles[h] = struct{}{}
	}
	p.mu.Unlock()

	for _, img := range images {
		p.mu.Lock()
		// Cache the handle so subsequent delta polls skip GetObjectInfo for it.
		if img.Handle != 0 {
			p.imageHandles[img.Handle] = struct{}{}
		}

		// Deduplicate by URL.
		if _, ok := p.seen[img.URL]; ok {
			p.mu.Unlock()
			continue
		}
		p.seen[img.URL] = struct{}{}
		p.seenKeys = append(p.seenKeys, img.URL)
		if len(p.seenKeys) > maxSeenImages {
			oldest := p.seenKeys[0]
			p.seenKeys = p.seenKeys[1:]
			delete(p.seen, oldest)
			// Also evict the handle so the delta scan can rediscover it
			// if the URL ever reappears (e.g. camera power-cycled).
			if h, herr := parseHandle(oldest); herr == nil {
				delete(p.imageHandles, h)
			}
		}
		p.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil
		case out <- img:
		}
	}

	if !initialDone || len(images) > 0 {
		logger.Info("component=poller msg=\"poll complete\" mode=%s new=%d", map[bool]string{true: "delta", false: "full"}[initialDone], len(images))
	}
	return nil
}

