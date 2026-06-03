package canon

import (
	"context"
	"sync"
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

	mu            sync.Mutex
	seen          map[string]struct{} // URL → already emitted
	seenKeys      []string            // for ring-buffer eviction
	imageHandles  map[uint32]struct{} // handle → known image (skip GetObjectInfo)
	folderHandles map[uint32]struct{} // handle → known folder (always recurse)
	initialDone   bool                // true after first successful full scan
}

// NewPoller creates a Poller that polls the camera at the given interval.
func NewPoller(client *Client, interval time.Duration) *Poller {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Poller{
		client:        client,
		interval:      interval,
		seen:          make(map[string]struct{}),
		imageHandles:  make(map[uint32]struct{}),
		folderHandles: make(map[uint32]struct{}),
	}
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
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()

		for {
			// Wait out any error back-off before the next attempt.
			if errBackoff > 0 {
				logger.Info("component=poller msg=\"waiting before retry\" backoff=%s", errBackoff)
				select {
				case <-ctx.Done():
					return
				case <-time.After(errBackoff):
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
				continue // skip the normal interval wait; errBackoff handles pacing
			}

			// Successful poll — reset error back-off and wait for the normal interval.
			errBackoff = 0
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
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
		logger.Error("component=poller msg=\"failed to list images\" err=%q", err)
		return err
	}

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
		}
		p.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil
		case out <- img:
		}
	}

	return nil
}

