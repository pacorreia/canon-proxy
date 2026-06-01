package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/pacorreia/canon-proxy/internal/backend"
	"github.com/pacorreia/canon-proxy/internal/canon"
	"github.com/pacorreia/canon-proxy/internal/config"
	"github.com/pacorreia/canon-proxy/internal/pipeline"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to YAML config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("level=fatal msg=\"failed to load config\" err=%q", err)
	}

	client := canon.NewClient(cfg.Camera.Host, cfg.Camera.Port)
	poller := canon.NewPoller(client, cfg.Camera.PollInterval)

	uploadBackend, err := backend.New(cfg)
	if err != nil {
		log.Fatalf("level=fatal msg=\"failed to initialize backend\" err=%q", err)
	}
	defer func() {
		if err := uploadBackend.Close(); err != nil {
			log.Printf("level=warn msg=\"failed to close backend\" err=%q", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("level=info msg=\"starting canon proxy\" backend=%q upload_workers=%d", uploadBackend.Name(), cfg.Upload.Workers)

	p := pipeline.New(client, poller, uploadBackend, cfg.Upload.Workers)
	if err := p.Run(ctx); err != nil {
		log.Fatalf("level=fatal msg=\"pipeline terminated with error\" err=%q", err)
	}

	log.Printf("level=info msg=\"canon proxy stopped\"")
}
