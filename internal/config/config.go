package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	// Database is the only mandatory bootstrap configuration.
	// Everything else is stored in the database and editable from the UI.
	Database DatabaseConfig `yaml:"database"`

	// Web listen address (may also be stored in DB, but needs to be known before DB opens).
	Web WebConfig `yaml:"web"`

	// Legacy fields: present only so that the first-run seed can read them
	// from an old-style config.yaml and populate the database.
	// They are ignored once settings exist in the database.
	Camera   CameraConfig   `yaml:"camera"`
	Upload   UploadConfig   `yaml:"upload"`
	Backends BackendsConfig `yaml:"backends"`
}

// DatabaseConfig selects the database backend and connection string.
type DatabaseConfig struct {
	// Driver is one of: sqlite (default), postgres, mssql.
	Driver string `yaml:"driver"`
	// DSN is the data source name / connection string.
	// For SQLite: a file path, e.g. "./canon-proxy.db".
	// For Postgres/MSSQL: the standard connection string.
	DSN string `yaml:"dsn"`
}

type CameraConfig struct {
	Host         string        `yaml:"host"`
	Port         int           `yaml:"port"`
	// ListenAddr, when set, switches the camera client to server mode:
	// the proxy listens on this address and the camera connects to us.
	// Use this for Canon EOS "Computer" WiFi in infrastructure networks.
	// Example: ":15740"
	ListenAddr   string        `yaml:"listen_addr"`
	PollInterval time.Duration `yaml:"poll_interval"`
}

type UploadConfig struct {
	Workers int    `yaml:"workers"`
	Backend string `yaml:"backend"`
}

type BackendsConfig struct {
	SMB   SMBConfig   `yaml:"smb"`
	FTP   FTPConfig   `yaml:"ftp"`
	S3    S3Config    `yaml:"s3"`
	Azure AzureConfig `yaml:"azure"`
	GCS   GCSConfig   `yaml:"gcs"`
}

type SMBConfig struct {
	Host     string `yaml:"host"`
	Share    string `yaml:"share"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Path     string `yaml:"path"`
}

type FTPConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	TLS      bool   `yaml:"tls"`
	Path     string `yaml:"path"`
}

type S3Config struct {
	Bucket    string `yaml:"bucket"`
	Region    string `yaml:"region"`
	Prefix    string `yaml:"prefix"`
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
}

type AzureConfig struct {
	Account   string `yaml:"account"`
	Container string `yaml:"container"`
	Prefix    string `yaml:"prefix"`
	SASToken  string `yaml:"sas_token"`
}

type GCSConfig struct {
	Bucket          string `yaml:"bucket"`
	Prefix          string `yaml:"prefix"`
	CredentialsFile string `yaml:"credentials_file"`
}

// WebConfig configures the HTTP server.
type WebConfig struct {
	// Listen is the address for the HTTP server, e.g. ":9090".
	Listen string `yaml:"listen"`
}

func Load(path string) (*Config, error) {
	cfg := &Config{
		Database: DatabaseConfig{
			Driver: "sqlite",
			DSN:    "./canon-proxy.db",
		},
		Web: WebConfig{
			Listen: ":9090",
		},
		// Legacy defaults (used for first-run seeding only).
		Camera: CameraConfig{
			Port:         15740,
			PollInterval: 5 * time.Second,
		},
		Upload: UploadConfig{
			Workers: 1,
		},
		Backends: BackendsConfig{
			FTP: FTPConfig{Port: 21},
		},
	}

	f, err := os.Open(path)
	if err != nil {
		// Config file not found is not fatal — use defaults.
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("open config file: %w", err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("decode config yaml: %w", err)
	}

	return cfg, nil
}
