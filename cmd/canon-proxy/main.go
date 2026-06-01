package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/pacorreia/canon-proxy/internal/backend"
	"github.com/pacorreia/canon-proxy/internal/canon"
	"github.com/pacorreia/canon-proxy/internal/config"
	"github.com/pacorreia/canon-proxy/internal/pipeline"
	"github.com/pacorreia/canon-proxy/internal/store"
	"github.com/pacorreia/canon-proxy/internal/web"
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

	log.Printf("level=info msg=\"starting canon proxy\" backend=%q upload_workers=%d mode=%q",
		uploadBackend.Name(), cfg.Upload.Workers, cfg.Web.Mode)

	cameraBase := fmt.Sprintf("http://%s:%d", cfg.Camera.Host, cfg.Camera.Port)

	var p *pipeline.Pipeline

	if cfg.Web.Mode == "manual" {
		st := store.New()
		p = pipeline.NewManual(client, poller, uploadBackend, cfg.Upload.Workers, st)
		srv := web.New(st, cameraBase, p.Push, cfg.Web.Listen)
		go srv.Start(ctx)
	} else {
		// auto mode: upload every detected image immediately; web UI is not started.
		p = pipeline.New(client, poller, uploadBackend, cfg.Upload.Workers)
	}

	if err := p.Run(ctx); err != nil {
		log.Fatalf("level=fatal msg=\"pipeline terminated with error\" err=%q", err)
	}

	log.Printf("level=info msg=\"canon proxy stopped\"")
}


