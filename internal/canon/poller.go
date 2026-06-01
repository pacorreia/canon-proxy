package canon

import (
	"context"
	"log"
	"sync"
	"time"
)

type Poller struct {
	client   *Client
	interval time.Duration
	seen     map[string]struct{}
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
		p.mu.Unlock()

		select {
		case <-ctx.Done():
			return false
		case out <- img:
		}
	}

	return true
}
