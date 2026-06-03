package pipeline

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/pacorreia/canon-proxy/internal/backend"
	"github.com/pacorreia/canon-proxy/internal/canon"
	"github.com/pacorreia/canon-proxy/internal/store"
)

const (
	maxRetries         = 3
	retrySchedulerTick = 5 * time.Second
	pushChanCapacity   = 512
)

// retryBackoff returns the delay before the nth retry attempt (1-indexed).
// attempt 1 → 5 s, 2 → 10 s, 3 → 20 s.
func retryBackoff(attempt int) time.Duration {
	return time.Duration(5<<uint(attempt-1)) * time.Second
}

// Pipeline orchestrates camera polling and upload workers in queue mode.
type Pipeline struct {
	client             *canon.Client
	poller             *canon.Poller
	backend            backend.Backend
	workers            int
	store              *store.Store
	pushCh             chan canon.Image
	deleteAfterUpload  bool

	gateMu sync.Mutex
	gate   chan struct{}
	paused bool
}

// NewManual creates a Pipeline in queue mode.
// Set deleteAfterUpload=true to delete each image from the camera after a successful upload.
func NewManual(client *canon.Client, poller *canon.Poller, b backend.Backend, workers int, st *store.Store, deleteAfterUpload bool) *Pipeline {
	if workers <= 0 {
		workers = 1
	}
	openGate := make(chan struct{})
	close(openGate)
	return &Pipeline{
		client:            client,
		poller:            poller,
		backend:           b,
		workers:           workers,
		store:             st,
		pushCh:            make(chan canon.Image, pushChanCapacity),
		deleteAfterUpload: deleteAfterUpload,
		gate:              openGate,
	}
}

func (p *Pipeline) Pause() {
	p.gateMu.Lock()
	defer p.gateMu.Unlock()
	if !p.paused {
		p.paused = true
		p.gate = make(chan struct{})
	}
}

func (p *Pipeline) Resume() {
	p.gateMu.Lock()
	defer p.gateMu.Unlock()
	if p.paused {
		p.paused = false
		close(p.gate)
	}
}

func (p *Pipeline) IsPaused() bool {
	p.gateMu.Lock()
	defer p.gateMu.Unlock()
	return p.paused
}

// ClearQueue drains the in-memory push channel and resets all queued images to "discovered".
// This covers both items already in the worker channel and items still waiting in the DB
// (which would otherwise be re-enqueued by retryScheduler on the next tick).
func (p *Pipeline) ClearQueue() int {
	// Reset all DB-queued records first so the retryScheduler doesn't re-enqueue them.
	n := int(p.store.ResetQueued())
	// Also drain any items already sitting in the channel (they may have been set to
	// "uploading" by Queue(); reset them to "discovered" as well).
	for {
		select {
		case img := <-p.pushCh:
			p.store.SetStatus(img.URL, store.StatusDiscovered, "")
			n++
		default:
			return n
		}
	}
}

// Queue sends images for upload. If the pipeline is paused, images are marked
// as queued so the retry scheduler picks them up after resume.
func (p *Pipeline) Queue(images []canon.Image) {
	if p.IsPaused() {
		for _, img := range images {
			p.store.SetStatus(img.URL, store.StatusQueued, "")
		}
		return
	}
	for _, img := range images {
		p.store.SetStatus(img.URL, store.StatusUploading, "")
		select {
		case p.pushCh <- img:
		default:
			log.Printf("level=warn component=pipeline msg=\"push channel full\" file=%q", img.Filename)
			p.store.SetStatus(img.URL, store.StatusQueued, "channel full")
		}
	}
}

// Run starts the pipeline. Blocks until ctx is cancelled.
func (p *Pipeline) Run(ctx context.Context) error {
	if p.client == nil || p.poller == nil || p.backend == nil {
		return fmt.Errorf("pipeline: dependencies not initialized")
	}

	polledCh := p.poller.Run(ctx)

	go func() {
		for img := range polledCh {
			if added := p.store.Add(img.Filename, img.URL, img.CapturedAt, img.IsVideo); added {
				log.Printf("level=info component=pipeline msg=\"image discovered\" file=%q", img.Filename)
			}
		}
	}()

	go p.retryScheduler(ctx)

	var wg sync.WaitGroup
	for i := 0; i < p.workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				if !p.awaitGate(ctx) {
					return
				}
				select {
				case <-ctx.Done():
					return
				case img, ok := <-p.pushCh:
					if !ok {
						return
					}
					// Re-check the gate after dequeueing: if Pause() was called
					// while this worker was blocking on pushCh, re-queue the image
					// so that ClearQueue() can drain/reset it instead of leaving it
					// stuck in "uploading" while this worker blocks.
					if p.IsPaused() {
						p.store.SetStatus(img.URL, store.StatusQueued, "paused")
						continue
					}
					if !p.awaitGate(ctx) {
						p.store.SetStatus(img.URL, store.StatusQueued, "context cancelled")
						return
					}
					if err := p.processImage(ctx, img, id); err != nil {
						log.Printf("level=error component=pipeline worker=%d file=%q err=%q", id, img.Filename, err)
					}
				}
			}
		}(i + 1)
	}

	wg.Wait()
	return nil
}

func (p *Pipeline) retryScheduler(ctx context.Context) {
	ticker := time.NewTicker(retrySchedulerTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !p.awaitGate(ctx) {
				return
			}
			todo := append(p.store.AllFreshQueued(), p.store.ListReadyToRetry()...)
			for _, e := range todo {
				// Mark as uploading before pushing to pushCh so that the record
				// is not picked up again on the next scheduler tick (which would
				// cause duplicate uploads if the tick fires before the worker drains).
				p.store.SetStatus(e.URL, store.StatusUploading, "")
				img := canon.Image{Filename: e.Filename, URL: e.URL}
				select {
				case p.pushCh <- img:
					log.Printf("level=info component=pipeline msg=\"enqueued\" file=%q retry_count=%d", e.Filename, e.RetryCount)
				default:
					// Channel full: revert to queued so the scheduler retries next tick.
					p.store.SetStatus(e.URL, store.StatusQueued, "channel full")
				}
			}
		}
	}
}

func (p *Pipeline) processImage(ctx context.Context, img canon.Image, workerID int) error {
	if err := ctx.Err(); err != nil {
		p.store.SetStatus(img.URL, store.StatusQueued, "context cancelled")
		return nil
	}

	entry := p.store.GetByFilename(img.Filename)
	retryCount := 0
	if entry != nil {
		retryCount = entry.RetryCount
	}

	rc, err := p.client.DownloadImage(ctx, img)
	if err != nil {
		return p.handleFailure(img, retryCount, fmt.Errorf("download: %w", err))
	}
	defer rc.Close()

	if err := p.backend.Upload(ctx, img.Filename, rc); err != nil {
		return p.handleFailure(img, retryCount, fmt.Errorf("upload: %w", err))
	}

	p.store.SetStatus(img.URL, store.StatusDone, "")
	log.Printf("level=info component=pipeline worker=%d msg=\"uploaded\" file=%q backend=%q", workerID, img.Filename, p.backend.Name())

	// Optionally delete the image from camera after a successful upload (transactional).
	if p.deleteAfterUpload {
		if err := p.client.DeleteObject(ctx, img); err != nil {
			log.Printf("level=warn component=pipeline worker=%d msg=\"delete from camera failed\" file=%q err=%q", workerID, img.Filename, err)
		} else {
			p.poller.EvictHandle(img.URL)
		}
	}

	return nil
}

func (p *Pipeline) handleFailure(img canon.Image, retryCount int, err error) error {
	if retryCount < maxRetries {
		attempt := retryCount + 1
		backoff := retryBackoff(attempt)
		p.store.SetRetryQueued(img.URL, attempt, time.Now().Add(backoff), err.Error())
		log.Printf("level=warn component=pipeline msg=\"scheduling retry\" file=%q attempt=%d backoff=%s err=%q",
			img.Filename, attempt, backoff, err)
		return nil
	}
	p.store.SetStatus(img.URL, store.StatusFailed, err.Error())
	log.Printf("level=error component=pipeline msg=\"max retries exhausted\" file=%q err=%q", img.Filename, err)
	return err
}

func (p *Pipeline) awaitGate(ctx context.Context) bool {
	p.gateMu.Lock()
	ch := p.gate
	p.gateMu.Unlock()
	select {
	case <-ctx.Done():
		return false
	case <-ch:
		return true
	}
}
