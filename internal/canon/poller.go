package canon

import (
	"context"
	"log"
	"sync"
	"time"
)

const maxSeenImages = 50_000

type Poller struct {
	client   *Client
	interval time.Duration
	seen     map[string]struct{}
	seenKeys []string
	mu       sync.Mutex
}

func NewPoller(client *Client, interval time.Duration) *Poller {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Poller{
		client:   client,
		interval: interval,
		seen:     make(map[string]struct{}),
	}
}

func (p *Poller) Run(ctx context.Context) <-chan Image {
	out := make(chan Image)

	go func() {
		defer close(out)
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()

		for {
			if !p.pollOnce(ctx, out) {
				return
			}

			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	return out
}

func (p *Poller) pollOnce(ctx context.Context, out chan<- Image) bool {
	images, err := p.client.ListImages(ctx)
	if err != nil {
		log.Printf("level=error component=poller msg=\"failed to list images\" err=%q", err)
		return true
	}

	for _, img := range images {
		p.mu.Lock()
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
			return false
		case out <- img:
		}
	}

	return true
}
