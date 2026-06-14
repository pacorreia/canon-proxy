package backend

import (
	"context"
	"fmt"
	"io"
	"net"
	"path"
	"sort"
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

func (b *SMBBackend) Close() error { return nil }

func (b *SMBBackend) Upload(ctx context.Context, filename, destPath string, r io.Reader) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context cancelled before smb upload: %w", err)
	}

	addr := b.cfg.Host
	if !strings.Contains(addr, ":") {
		addr = net.JoinHostPort(addr, "445")
	}

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
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

	// Resolve destination directory from destPath (from pairing or manual upload).
	// Path comes exclusively from folder pairings and manual upload requests;
	// there is no longer a backend-level base path.
	var dir string
	if destPath != "" {
		dir = strings.TrimPrefix(path.Clean("/"+destPath), "/")
	}

	if dir != "" && dir != "." {
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
// ListFolders returns the names of subdirectories on the share at the given
// path. path is slash-separated and relative to the share root (e.g. "/" or
// "/photos"). Results are sorted alphabetically.
func (b *SMBBackend) ListFolders(ctx context.Context, relPath string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("context cancelled: %w", err)
	}

	// Sanitize: path.Clean resolves .. components so the result always stays
	// within the share root.
	clean := path.Clean("/" + relPath)
	dir := strings.TrimPrefix(clean, "/") // "" == share root
	if dir == "" {
		dir = "."
	}

	addr := b.cfg.Host
	if !strings.Contains(addr, ":") {
		addr = net.JoinHostPort(addr, "445")
	}

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial smb server %s: %w", addr, err)
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
		return nil, fmt.Errorf("start smb session: %w", err)
	}
	defer session.Logoff()

	share, err := session.Mount(b.cfg.Share)
	if err != nil {
		return nil, fmt.Errorf("mount smb share %s: %w", b.cfg.Share, err)
	}
	defer share.Umount()

	entries, err := share.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read smb directory %q: %w", dir, err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}