package backend

import (
	"fmt"
	"strings"

	"github.com/pacorreia/canon-proxy/internal/config"
)

func New(cfg *config.Config) (Backend, error) {
	switch strings.ToLower(cfg.Upload.Backend) {
	case "smb":
		return NewSMBBackend(cfg.Backends.SMB), nil
	case "ftp":
		return NewFTPBackend(cfg.Backends.FTP), nil
	case "s3":
		return NewS3Backend(cfg.Backends.S3)
	case "azure":
		return NewAzureBackend(cfg.Backends.Azure)
	case "gcs":
		return NewGCSBackend(cfg.Backends.GCS)
	default:
		return nil, fmt.Errorf("unsupported backend: %s", cfg.Upload.Backend)
	}
}
