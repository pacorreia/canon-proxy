package backend

import (
	"context"
	"fmt"
	"io"
	"net"
	"path"
	"strings"

	"github.com/hirochachacha/go-smb2"
	"github.com/pacorreia/canon-proxy/internal/config"
)

type SMBBackend struct {
	cfg config.SMBConfig
}

func NewSMBBackend(cfg config.SMBConfig) *SMBBackend {
	return &SMBBackend{cfg: cfg}
}

func (b *SMBBackend) Name() string {
	return "smb"
}

func (b *SMBBackend) Upload(ctx context.Context, filename string, r io.Reader) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context cancelled before smb upload: %w", err)
	}

	addr := b.cfg.Host
	if !strings.Contains(addr, ":") {
		addr = net.JoinHostPort(addr, "445")
	}

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("dial smb server %s: %w", addr, err)
	}
	defer conn.Close()

	d := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     b.cfg.Username,
			Password: b.cfg.Password,
		},
	}

	session, err := d.Dial(conn)
	if err != nil {
		return fmt.Errorf("start smb session: %w", err)
	}
	defer session.Logoff()

	share, err := session.Mount(b.cfg.Share)
	if err != nil {
		return fmt.Errorf("mount smb share %s: %w", b.cfg.Share, err)
	}
	defer share.Umount()

	dir := strings.TrimPrefix(path.Clean(b.cfg.Path), "/")
	if dir != "." && dir != "" {
		if err := share.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create smb directory %s: %w", dir, err)
		}
	}

	target := path.Join(dir, filename)
	f, err := share.Create(target)
	if err != nil {
		return fmt.Errorf("create smb file %s: %w", target, err)
	}
	defer f.Close()

	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("write smb file %s: %w", target, err)
	}

	return nil
}
