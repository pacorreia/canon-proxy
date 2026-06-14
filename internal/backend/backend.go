package backend

import (
	"context"
	"io"
)

type Backend interface {
	// Upload writes r to the backend. destPath is the destination subdirectory
	// (relative to the backend's configured base path); pass "" to use the base
	// path directly.
	Upload(ctx context.Context, filename, destPath string, r io.Reader) error
	Name() string
	Close() error
}

// FolderLister is an optional extension of Backend that can enumerate
// directories on the remote file share.
type FolderLister interface {
	Backend
	// ListFolders returns the names of subdirectories at path.
	// path is a slash-separated path relative to the share root (e.g. "/" or "/photos").
	// Results are sorted alphabetically.
	ListFolders(ctx context.Context, path string) ([]string, error)
}
