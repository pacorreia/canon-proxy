package backend

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pacorreia/canon-proxy/internal/config"
)

// New builds a Backend from a *config.Config (used only for legacy wiring).
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

// NewFromSettings builds a Backend from a flat key-value settings map loaded from the database.
// Known keys: upload.backend, smb.host, smb.share, smb.username, smb.password, smb.path,
// ftp.host, ftp.port, ftp.username, ftp.password, ftp.tls, ftp.path,
// s3.bucket, s3.region, s3.prefix, s3.access_key, s3.secret_key,
// azure.account, azure.container, azure.prefix, azure.sas_token,
// gcs.bucket, gcs.prefix, gcs.credentials_file.
func NewFromSettings(m map[string]string) (Backend, error) {
	get := func(k, def string) string {
		if v, ok := m[k]; ok && v != "" {
			return v
		}
		return def
	}
	getInt := func(k string, def int) int {
		if v, ok := m[k]; ok && v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				return n
			}
		}
		return def
	}
	getBool := func(k string) bool {
		v := strings.ToLower(m[k])
		return v == "true" || v == "1" || v == "yes"
	}

	switch strings.ToLower(get("upload.backend", "smb")) {
	case "smb":
		return NewSMBBackend(config.SMBConfig{
			Host:     get("smb.host", ""),
			Share:    get("smb.share", ""),
			Username: get("smb.username", ""),
			Password: get("smb.password", ""),
		}), nil
	case "ftp":
		return NewFTPBackend(config.FTPConfig{
			Host:     get("ftp.host", ""),
			Port:     getInt("ftp.port", 21),
			Username: get("ftp.username", ""),
			Password: get("ftp.password", ""),
			TLS:      getBool("ftp.tls"),
			Path:     get("ftp.path", "/"),
		}), nil
	case "s3":
		return NewS3Backend(config.S3Config{
			Bucket:    get("s3.bucket", ""),
			Region:    get("s3.region", ""),
			Prefix:    get("s3.prefix", ""),
			AccessKey: get("s3.access_key", ""),
			SecretKey: get("s3.secret_key", ""),
		})
	case "azure":
		return NewAzureBackend(config.AzureConfig{
			Account:   get("azure.account", ""),
			Container: get("azure.container", ""),
			Prefix:    get("azure.prefix", ""),
			SASToken:  get("azure.sas_token", ""),
		})
	case "gcs":
		return NewGCSBackend(config.GCSConfig{
			Bucket:          get("gcs.bucket", ""),
			Prefix:          get("gcs.prefix", ""),
			CredentialsFile: get("gcs.credentials_file", ""),
		})
	default:
		return nil, fmt.Errorf("unsupported backend: %s", get("upload.backend", "smb"))
	}
}

