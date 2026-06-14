package backend

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/pacorreia/canon-proxy/internal/config"
)

type AzureBackend struct {
	client    *azblob.Client
	container string
	prefix    string
}

func NewAzureBackend(cfg config.AzureConfig) (*AzureBackend, error) {
	if cfg.Account == "" {
		return nil, fmt.Errorf("azure account is required")
	}
	if cfg.Container == "" {
		return nil, fmt.Errorf("azure container is required")
	}
	if cfg.SASToken == "" {
		return nil, fmt.Errorf("azure sas_token is required")
	}

	sasToken := strings.TrimPrefix(cfg.SASToken, "?")
	serviceURL := fmt.Sprintf("https://%s.blob.core.windows.net/?%s", cfg.Account, sasToken)
	client, err := azblob.NewClientWithNoCredential(serviceURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create azure blob client: %w", err)
	}

	return &AzureBackend{
		client:    client,
		container: cfg.Container,
		prefix:    strings.TrimPrefix(cfg.Prefix, "/"),
	}, nil
}

func (b *AzureBackend) Name() string {
	return "azure"
}

func (b *AzureBackend) Close() error { return nil }

func (b *AzureBackend) Upload(ctx context.Context, filename, destPath string, r io.Reader) error {
	var blobName string
	if destPath != "" {
		clean := strings.TrimPrefix(path.Clean("/"+destPath), "/")
		blobName = path.Join(b.prefix, clean, filename)
	} else {
		blobName = path.Join(b.prefix, filename)
	}

	_, err := b.client.UploadStream(ctx, b.container, blobName, r, nil)
	if err != nil {
		return fmt.Errorf("upload blob to azure container %s blob %s: %w", b.container, blobName, err)
	}

	return nil
}
