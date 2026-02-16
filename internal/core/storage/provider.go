package storage

import (
	"context"
	"io"
	"time"
)

// Provider abstracts file storage.
type Provider interface {
	Name() string
	Open(ctx context.Context, storageURI string) (io.ReadSeekCloser, FileMetadata, error)
	Delete(ctx context.Context, storageURI string) error
	Exists(ctx context.Context, storageURI string) (bool, error)
	DiskUsage(ctx context.Context) (DiskStats, error)
}

type FileMetadata struct {
	Size        int64
	ContentType string
	ModTime     time.Time
}

type DiskStats struct {
	Total     int64
	Used      int64
	Available int64
}
