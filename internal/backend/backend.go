package backend

import (
	"context"
	"io"
)

type Backend interface {
	Upload(ctx context.Context, filename string, r io.Reader) error
	Name() string
	Close() error
}
