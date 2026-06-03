package pipeline

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/pacorreia/canon-proxy/internal/backend"
	"github.com/pacorreia/canon-proxy/internal/canon"
	"github.com/pacorreia/canon-proxy/internal/store"
)

type Pipeline struct {
	client  *canon.Client
	poller  *canon.Poller
	backend backend.Backend
	workers int
	store   *store.Store // nil in auto mode
	pushCh  chan canon.Image
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

// NewManual creates a Pipeline in manual mode. Images detected by the poller
// are registered in st but not uploaded until Push is called.
func NewManual(client *canon.Client, poller *canon.Poller, backend backend.Backend, workers int, st *store.Store) *Pipeline {
	if workers <= 0 {
		workers = 4
	}
	return &Pipeline{
		client:  client,
		poller:  poller,
		backend: backend,
		workers: workers,
		store:   st,
		pushCh:  make(chan canon.Image, 256),
	}
}

// Push enqueues the given images for upload (manual mode only).
// Images that are already uploading or done are silently skipped.
func (p *Pipeline) Push(images []canon.Image) {
	if p.pushCh == nil {
		return
	}
	for _, img := range images {
		if p.store != nil {
			p.store.SetStatus(img.URL, store.StatusUploading, "")
		}
		select {
		case p.pushCh <- img:
		default:
			log.Printf("level=warn component=pipeline msg=\"push channel full, dropping image\" file=%q", img.Filename)
			if p.store != nil {
				p.store.SetStatus(img.URL, store.StatusFailed, "push channel full")
			}
		}
	}
}

func (p *Pipeline) Run(ctx context.Context) error {
	if p.client == nil || p.poller == nil || p.backend == nil {
		return fmt.Errorf("pipeline dependencies are not initialized")
	}

	if p.store != nil {
		return p.runManual(ctx)
	}
	return p.runAuto(ctx)
}

// runAuto is the original behaviour: every detected image is uploaded.
func (p *Pipeline) runAuto(ctx context.Context) error {
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

// runManual registers detected images in the store and waits for Push calls.
func (p *Pipeline) runManual(ctx context.Context) error {
	polledCh := p.poller.Run(ctx)

	// Goroutine: move newly detected images into the store.
	go func() {
		for img := range polledCh {
			p.store.Add(img.Filename, img.URL)
			log.Printf("level=info component=pipeline msg=\"image queued\" file=%q", img.Filename)
		}
	}()

	// Workers process images from the push channel.
	var wg sync.WaitGroup
	for i := 0; i < p.workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case img, ok := <-p.pushCh:
					if !ok {
						return
					}
					if err := p.processImageManual(ctx, img, workerID); err != nil {
						log.Printf("level=error component=pipeline worker=%d msg=\"failed to process image\" file=%q err=%q", workerID, img.Filename, err)
					}
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

func (p *Pipeline) processImageManual(ctx context.Context, img canon.Image, workerID int) error {
	if err := ctx.Err(); err != nil {
		p.store.SetStatus(img.URL, store.StatusFailed, "context cancelled")
		return fmt.Errorf("context cancelled before processing image %s: %w", img.Filename, err)
	}

	rc, err := p.client.DownloadImage(ctx, img)
	if err != nil {
		p.store.SetStatus(img.URL, store.StatusFailed, err.Error())
		return fmt.Errorf("download image %s: %w", img.Filename, err)
	}
	defer rc.Close()

	if err := p.backend.Upload(ctx, img.Filename, rc); err != nil {
		p.store.SetStatus(img.URL, store.StatusFailed, err.Error())
		return fmt.Errorf("upload image %s to %s: %w", img.Filename, p.backend.Name(), err)
	}

	p.store.SetStatus(img.URL, store.StatusDone, "")
	log.Printf("level=info component=pipeline worker=%d msg=\"image uploaded\" file=%q backend=%q", workerID, img.Filename, p.backend.Name())
	return nil
}

