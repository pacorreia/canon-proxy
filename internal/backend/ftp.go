package backend

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"path"
	"strings"
	"time"

	"github.com/jlaffaye/ftp"
	"github.com/pacorreia/canon-proxy/internal/config"
)

type FTPBackend struct {
	cfg config.FTPConfig
}

func NewFTPBackend(cfg config.FTPConfig) *FTPBackend {
	if cfg.Port == 0 {
		cfg.Port = 21
	}
	return &FTPBackend{cfg: cfg}
}

func (b *FTPBackend) Name() string {
	return "ftp"
}

func (b *FTPBackend) Close() error { return nil }

func (b *FTPBackend) Upload(ctx context.Context, filename, destPath string, r io.Reader) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context cancelled before ftp upload: %w", err)
	}

	addr := net.JoinHostPort(b.cfg.Host, fmt.Sprintf("%d", b.cfg.Port))
	dialOptions := []ftp.DialOption{ftp.DialWithTimeout(10 * time.Second)}
	if b.cfg.TLS {
		dialOptions = append(dialOptions, ftp.DialWithExplicitTLS(&tls.Config{MinVersion: tls.VersionTLS12}))
	}

	conn, err := ftp.Dial(addr, dialOptions...)
	if err != nil {
		return fmt.Errorf("dial ftp server %s: %w", addr, err)
	}
	defer conn.Quit()

	if err := conn.Login(b.cfg.Username, b.cfg.Password); err != nil {
		return fmt.Errorf("ftp login: %w", err)
	}

	base := path.Clean("/" + strings.TrimPrefix(b.cfg.Path, "/"))
	var dir string
	if destPath != "" {
		dir = path.Join(base, strings.TrimPrefix(path.Clean("/"+destPath), "/"))
	} else {
		dir = base
	}
	if dir != "/" {
		if err := conn.ChangeDir(dir); err != nil {
			if err := conn.MakeDir(dir); err != nil {
				return fmt.Errorf("create ftp directory %s: %w", dir, err)
			}
			if err := conn.ChangeDir(dir); err != nil {
				return fmt.Errorf("change ftp directory %s after create: %w", dir, err)
			}
		}
	}

	target := path.Join(dir, filename)
	if err := conn.Stor(target, r); err != nil {
		return fmt.Errorf("store ftp file %s: %w", target, err)
	}

	return nil
}
