package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pacorreia/canon-proxy/internal/backend"
	"github.com/pacorreia/canon-proxy/internal/canon"
	"github.com/pacorreia/canon-proxy/internal/config"
	"github.com/pacorreia/canon-proxy/internal/db"
	"github.com/pacorreia/canon-proxy/internal/logger"
	"github.com/pacorreia/canon-proxy/internal/pipeline"
	"github.com/pacorreia/canon-proxy/internal/store"
	"github.com/pacorreia/canon-proxy/internal/web"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to YAML config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Fatal("msg=\"failed to load config\" err=%q", err)
	}

	// Initialise database.
	driver := cfg.Database.Driver
	if driver == "" {
		driver = "sqlite"
	}
	dsn := cfg.Database.DSN
	if dsn == "" {
		dsn = "./canon-proxy.db"
	}
	gdb, err := db.Open(driver, dsn)
	if err != nil {
		logger.Fatal("msg=\"failed to open database\" driver=%q dsn=%q err=%q", driver, dsn, err)
	}

	imageRepo := db.NewImageRepo(gdb)
	settingRepo := db.NewSettingRepo(gdb)

	// Check if camera settings already exist in the database before seeding.
	// Used below to warn when config.yaml provides camera settings that will be ignored.
	existingCameraHost, _ := settingRepo.Get("camera.host")
	existingListenAddr, _ := settingRepo.Get("camera.listen_addr")
	dbHasCameraConfig := hasCameraSettings(existingCameraHost, existingListenAddr)

	// Seed default settings from config (only inserts if key not yet present).
	seedSettingsFromConfig(settingRepo, cfg)

	// Warn when config.yaml provides camera/upload/backend settings that the database
	// already has its own values for — the file values are silently ignored by SeedDefaults.
	if cfg.Loaded && dbHasCameraConfig && (cfg.Camera.Host != "" || cfg.Camera.ListenAddr != "") {
		logger.Warn("msg=\"camera settings in config.yaml will be ignored; settings already stored in database\"")
	}

	// Load app settings from database.
	appSettings, err := settingRepo.All()
	if err != nil {
		logger.Fatal("msg=\"failed to load settings\" err=%q", err)
	}

	// Apply the stored log level immediately.
	logger.SetLevel(getStr(appSettings, "log.level", "info"))

	// Build camera client.
	cameraHost := getStr(appSettings, "camera.host", cfg.Camera.Host)
	cameraPort := getInt(appSettings, "camera.port", cfg.Camera.Port, 15740)
	listenAddr := getStr(appSettings, "camera.listen_addr", cfg.Camera.ListenAddr)
	pollInterval := getDuration(appSettings, "camera.poll_interval", cfg.Camera.PollInterval, 5*time.Second)

	hasCameraConfig := hasCameraSettings(cameraHost, listenAddr)

	var client *canon.Client
	var poller *canon.Poller
	if hasCameraConfig {
		if listenAddr != "" {
			client = canon.NewServerClient(listenAddr, cameraHost)
		} else {
			client = canon.NewClient(cameraHost, cameraPort)
		}
		poller = canon.NewPoller(client, pollInterval)
	} else {
		logger.Warn("msg=\"no camera configured; polling will not start until camera settings are saved and the process is restarted\"")
	}

	// Build upload backend.
	uploadBackend, err := backend.NewFromSettings(appSettings)
	if err != nil {
		logger.Fatal("msg=\"failed to initialize backend\" err=%q", err)
	}
	defer func() {
		if err := uploadBackend.Close(); err != nil {
			logger.Warn("msg=\"close backend error\" err=%q", err)
		}
	}()

	workers := getInt(appSettings, "upload.workers", cfg.Upload.Workers, 1)
	deleteAfterUpload := getBool(appSettings, "camera.delete_after_upload", false)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("msg=\"starting canon proxy\" camera=%q backend=%q workers=%d db=%q",
		fmt.Sprintf("%s:%d", cameraHost, cameraPort), uploadBackend.Name(), workers, driver)

	// Build store and recover any interrupted uploads.
	st := store.New(imageRepo)
	st.ResetStuckUploading()

	// Build pipeline.
	p := pipeline.NewManual(client, poller, uploadBackend, workers, st, deleteAfterUpload)

	// Enqueue all previously-queued images on startup (e.g. after a restart).
	freshQueued := st.AllFreshQueued()
	if len(freshQueued) > 0 {
		logger.Info("msg=\"re-enqueuing previously queued images\" count=%d", len(freshQueued))
		imgs := make([]canon.Image, len(freshQueued))
		for i, e := range freshQueued {
			imgs[i] = canon.Image{Filename: e.Filename, URL: e.URL}
		}
		p.Queue(imgs)
	}

	// thumbFunc and downloadFunc are only set when a camera client is available.
	// The web server handles nil values gracefully (returns 501 Not Implemented).
	var thumbFunc web.ThumbFunc
	var downloadFunc web.DownloadFunc
	if client != nil {
		thumbFunc = func(ctx context.Context, imageURL string) (io.ReadCloser, error) {
			return client.GetThumb(ctx, imageURL)
		}
		downloadFunc = func(ctx context.Context, image canon.Image) (io.ReadCloser, error) {
			return client.DownloadImage(ctx, image)
		}
	}

	// restartFunc re-execs the current process in-place, inheriting all
	// arguments and environment. The new process reads settings fresh from DB.
	restartFunc := func() {
		exe, err := os.Executable()
		if err != nil {
			logger.Error("msg=\"restart: could not resolve executable\" err=%q", err)
			return
		}
		logger.Info("msg=\"restarting process\" exe=%q", exe)
		// syscall.Exec replaces this process image; never returns on success.
		if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
			logger.Error("msg=\"restart failed\" err=%q", err)
		}
	}

	srv := web.New(st, thumbFunc, downloadFunc, p.Queue, cfg.Web.Listen, web.QueueController{
		Pause:    p.Pause,
		Resume:   p.Resume,
		Clear:    func() { p.ClearQueue() },
		IsPaused: p.IsPaused,
	}, settingRepo, restartFunc, initLogBroadcaster(), func(kv map[string]string) {
		if v, ok := kv["log.level"]; ok {
			logger.SetLevel(v)
			logger.Info("msg=\"log level changed\" level=%q", v)
		}
	})

	go srv.Start(ctx)

	if hasCameraConfig {
		if err := p.Run(ctx); err != nil {
			logger.Fatal("msg=\"pipeline terminated with error\" err=%q", err)
		}
	} else {
		// Pause the pipeline so that Queue calls from the web UI keep items in
		// StatusQueued rather than StatusUploading (which would leave them stuck
		// with no workers running to process them).
		p.Pause()
		logger.Info("msg=\"running without camera polling; waiting for web UI interactions or shutdown signal\"")
		<-ctx.Done()
	}
	logger.Info("msg=\"canon proxy stopped\"")
}

// seedSettingsFromConfig inserts default values from the YAML config for
// any settings key that does not yet exist in the database.
func seedSettingsFromConfig(repo *db.SettingRepo, cfg *config.Config) {
	defaults := map[string]string{
		"camera.host":              cfg.Camera.Host,
		"camera.port":              strconv.Itoa(cfg.Camera.Port),
		"camera.listen_addr":       cfg.Camera.ListenAddr,
		"camera.poll_interval":     cfg.Camera.PollInterval.String(),
		"camera.delete_after_upload": strconv.FormatBool(cfg.Camera.DeleteAfterUpload),
		"upload.backend":       cfg.Upload.Backend,
		"upload.workers":       strconv.Itoa(cfg.Upload.Workers),
		"smb.host":             cfg.Backends.SMB.Host,
		"smb.share":            cfg.Backends.SMB.Share,
		"smb.username":         cfg.Backends.SMB.Username,
		"smb.password":         envOrVal("SMB_PASSWORD", cfg.Backends.SMB.Password),
		"smb.path":             cfg.Backends.SMB.Path,
		"ftp.host":             cfg.Backends.FTP.Host,
		"ftp.port":             strconv.Itoa(cfg.Backends.FTP.Port),
		"ftp.username":         cfg.Backends.FTP.Username,
		"ftp.password":         envOrVal("FTP_PASSWORD", cfg.Backends.FTP.Password),
		"ftp.tls":              strconv.FormatBool(cfg.Backends.FTP.TLS),
		"ftp.path":             cfg.Backends.FTP.Path,
		"s3.bucket":            cfg.Backends.S3.Bucket,
		"s3.region":            cfg.Backends.S3.Region,
		"s3.prefix":            cfg.Backends.S3.Prefix,
		"s3.access_key":        cfg.Backends.S3.AccessKey,
		"s3.secret_key":        envOrVal("S3_SECRET_KEY", cfg.Backends.S3.SecretKey),
		"azure.account":        cfg.Backends.Azure.Account,
		"azure.container":      cfg.Backends.Azure.Container,
		"azure.prefix":         cfg.Backends.Azure.Prefix,
		"azure.sas_token":      envOrVal("AZURE_SAS_TOKEN", cfg.Backends.Azure.SASToken),
		"gcs.bucket":           cfg.Backends.GCS.Bucket,
		"gcs.prefix":           cfg.Backends.GCS.Prefix,
		"gcs.credentials_file": cfg.Backends.GCS.CredentialsFile,
		"log.level":            "info",
	}
	if err := repo.SeedDefaults(defaults); err != nil {
		logger.Warn("msg=\"failed to seed default settings\" err=%q", err)
	}
}

// ---- helpers ----------------------------------------------------------------

// hasCameraSettings reports whether a camera host or listen address is set.
func hasCameraSettings(host, listenAddr string) bool {
	return strings.TrimSpace(host) != "" || strings.TrimSpace(listenAddr) != ""
}

func getStr(m map[string]string, key, fallback string) string {
	if v, ok := m[key]; ok && v != "" {
		return v
	}
	return fallback
}

func getInt(m map[string]string, key string, fallback, hardDefault int) int {
	if v, ok := m[key]; ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	if fallback > 0 {
		return fallback
	}
	return hardDefault
}

func getDuration(m map[string]string, key string, fallback, hardDefault time.Duration) time.Duration {
	if v, ok := m[key]; ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	if fallback > 0 {
		return fallback
	}
	return hardDefault
}

func getBool(m map[string]string, key string, fallback bool) bool {
	if v, ok := m[key]; ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes":
			return true
		case "false", "0", "no", "":
			return false
		}
	}
	return fallback
}

// envOrVal returns the value of the named environment variable when non-empty,
// falling back to val otherwise. Use it to let Kubernetes secrets override
// config-file values for credentials.
func envOrVal(envKey, val string) string {
	if e := os.Getenv(envKey); e != "" {
		return e
	}
	return val
}

func init() {
	log.SetOutput(os.Stdout)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
}

// initLogBroadcaster creates a LogBroadcaster, tees the standard logger
// output through it, and returns it for passing to the web server.
func initLogBroadcaster() *web.LogBroadcaster {
	lb := web.NewLogBroadcaster()
	log.SetOutput(io.MultiWriter(os.Stdout, lb))
	return lb
}
