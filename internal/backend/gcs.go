package backend

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/pacorreia/canon-proxy/internal/config"
	"google.golang.org/api/option"
)

type GCSBackend struct {
	client *storage.Client
	bucket string
	prefix string
}

func NewGCSBackend(cfg config.GCSConfig) (*GCSBackend, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("gcs bucket is required")
	}

	var (
		client *storage.Client
		err    error
	)
	if cfg.CredentialsFile != "" {
		client, err = storage.NewClient(context.Background(), option.WithCredentialsFile(cfg.CredentialsFile))
	} else {
		client, err = storage.NewClient(context.Background())
	}
	if err != nil {
		return nil, fmt.Errorf("create gcs client: %w", err)
	}

	return &GCSBackend{
		client: client,
		bucket: cfg.Bucket,
		prefix: strings.TrimPrefix(cfg.Prefix, "/"),
	}, nil
}

func (b *GCSBackend) Close() error {
	return b.client.Close()
}

func (b *GCSBackend) Name() string {
	return "gcs"
}

func (b *GCSBackend) Upload(ctx context.Context, filename string, r io.Reader) error {
	objectName := path.Join(b.prefix, filename)

	wc := b.client.Bucket(b.bucket).Object(objectName).NewWriter(ctx)
	if _, err := io.Copy(wc, r); err != nil {
		_ = wc.Close()
		return fmt.Errorf("write gcs object %s: %w", objectName, err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("close gcs object writer %s: %w", objectName, err)
	}

	return nil
}
