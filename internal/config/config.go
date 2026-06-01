package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Camera   CameraConfig   `yaml:"camera"`
	Upload   UploadConfig   `yaml:"upload"`
	Backends BackendsConfig `yaml:"backends"`
}

type CameraConfig struct {
	Host            string        `yaml:"host"`
	Port            int           `yaml:"port"`
	PollInterval    time.Duration `yaml:"poll_interval"`
	DownloadWorkers int           `yaml:"download_workers"`
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

func Load(path string) (*Config, error) {
	cfg := &Config{
		Camera: CameraConfig{
			Port:            8080,
			PollInterval:    5 * time.Second,
			DownloadWorkers: 4,
		},
		Upload: UploadConfig{
			Workers: 4,
		},
		Backends: BackendsConfig{
			FTP: FTPConfig{Port: 21},
		},
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config file: %w", err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("decode config yaml: %w", err)
	}
	}

	if cfg.Camera.Host == "" {
		return nil, fmt.Errorf("camera.host is required")
	}
	if cfg.Upload.Backend == "" {
		return nil, fmt.Errorf("upload.backend is required")
	}

	return cfg, nil
}
