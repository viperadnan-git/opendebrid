package storage

import (
	"context"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// LocalProvider implements Provider for local filesystem.
type LocalProvider struct {
	basePath string
}

func NewLocalProvider(basePath string) *LocalProvider {
	return &LocalProvider{basePath: basePath}
}

func (p *LocalProvider) Name() string { return "local" }

func (p *LocalProvider) Open(_ context.Context, storageURI string) (*os.File, FileMetadata, error) {
	path := uriToPath(storageURI)

	f, err := os.Open(path)
	if err != nil {
		return nil, FileMetadata{}, fmt.Errorf("open file: %w", err)
	}

	stat, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, FileMetadata{}, fmt.Errorf("stat file: %w", err)
	}

	contentType := mime.TypeByExtension(filepath.Ext(path))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	return f, FileMetadata{
		Size:        stat.Size(),
		ContentType: contentType,
		ModTime:     stat.ModTime(),
	}, nil
}

func (p *LocalProvider) Delete(_ context.Context, storageURI string) error {
	return os.Remove(uriToPath(storageURI))
}

func (p *LocalProvider) Exists(_ context.Context, storageURI string) (bool, error) {
	_, err := os.Stat(uriToPath(storageURI))
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func (p *LocalProvider) DiskUsage(_ context.Context) (DiskStats, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(p.basePath, &stat); err != nil {
		return DiskStats{}, err
	}

	total := int64(stat.Blocks) * int64(stat.Bsize)
	available := int64(stat.Bavail) * int64(stat.Bsize)

	return DiskStats{
		Total:     total,
		Used:      total - available,
		Available: available,
	}, nil
}

func uriToPath(uri string) string {
	return strings.TrimPrefix(uri, "file://")
}
