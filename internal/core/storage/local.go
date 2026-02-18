package storage

import (
	"context"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
)

// LocalProvider implements Provider for local filesystem.
type LocalProvider struct {
	basePath string
}

func NewLocalProvider(basePath string) *LocalProvider {
	return &LocalProvider{basePath: basePath}
}

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

func uriToPath(uri string) string {
	return strings.TrimPrefix(uri, "file://")
}
