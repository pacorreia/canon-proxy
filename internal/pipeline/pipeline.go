package pipeline

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/pacorreia/canon-proxy/internal/backend"
	"github.com/pacorreia/canon-proxy/internal/canon"
)

type Pipeline struct {
	client  *canon.Client
	poller  *canon.Poller
	backend backend.Backend
	workers int
}

func New(client *canon.Client, poller *canon.Poller, backend backend.Backend, workers int) *Pipeline {
	if workers <= 0 {
		workers = 4
	}
	return &Pipeline{
		client:  client,
		poller:  poller,
		backend: backend,
		workers: workers,
	}
}

func (p *Pipeline) Run(ctx context.Context) error {
	if p.client == nil || p.poller == nil || p.backend == nil {
		return fmt.Errorf("pipeline dependencies are not initialized")
	}

	imagesCh := p.poller.Run(ctx)

	var wg sync.WaitGroup
	for i := 0; i < p.workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for img := range imagesCh {
				if err := p.processImage(ctx, img, workerID); err != nil {
					log.Printf("level=error component=pipeline worker=%d msg=\"failed to process image\" file=%q err=%q", workerID, img.Filename, err)
				}
			}
		}(i + 1)
	}

	wg.Wait()
	return nil
}

func (p *Pipeline) processImage(ctx context.Context, img canon.Image, workerID int) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context cancelled before processing image %s: %w", img.Filename, err)
	}

	rc, err := p.client.DownloadImage(ctx, img)
	if err != nil {
		return fmt.Errorf("download image %s: %w", img.Filename, err)
	}
	defer rc.Close()

	if err := p.backend.Upload(ctx, img.Filename, rc); err != nil {
		return fmt.Errorf("upload image %s to %s: %w", img.Filename, p.backend.Name(), err)
	}

	log.Printf("level=info component=pipeline worker=%d msg=\"image uploaded\" file=%q backend=%q", workerID, img.Filename, p.backend.Name())
	return nil
}
