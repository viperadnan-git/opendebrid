package storage

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LocalStorageProvider implements StorageProvider for local filesystem storage.
// Remote methods (Upload, Delete, GenerateURL, ServeFile, ListFiles) return ErrNotSupported.
type LocalStorageProvider struct {
	basePath string
}

func NewLocalStorageProvider(basePath string) *LocalStorageProvider {
	return &LocalStorageProvider{basePath: basePath}
}

func (p *LocalStorageProvider) Name() string                  { return "local" }
func (p *LocalStorageProvider) Capabilities() Capability      { return CapLocal }
func (p *LocalStorageProvider) Check(_ context.Context) error { return nil }

func (p *LocalStorageProvider) Upload(_ context.Context, _, _ string) (string, error) {
	return "", ErrNotSupported
}

func (p *LocalStorageProvider) Delete(_ context.Context, _ string) error {
	return ErrNotSupported
}

func (p *LocalStorageProvider) GenerateURL(_ context.Context, _, _ string, _ time.Duration) (string, error) {
	return "", ErrNotSupported
}

func (p *LocalStorageProvider) ServeFile(_ context.Context, _, _ string, _ http.ResponseWriter, _ *http.Request) error {
	return ErrNotSupported
}

func (p *LocalStorageProvider) ListFiles(_ context.Context, _ string) ([]RemoteFileInfo, error) {
	return nil, ErrNotSupported
}

// Open opens a local file by its storage URI (file:///path) and returns the
// file handle with metadata. Used by the file server for local serving.
func (p *LocalStorageProvider) Open(_ context.Context, storageURI string) (*os.File, FileMetadata, error) {
	path := URIToPath(storageURI)

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

// URIToPath strips the file:// prefix from a storage URI.
func URIToPath(uri string) string {
	return strings.TrimPrefix(uri, "file://")
}
