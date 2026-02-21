package storage

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// ErrNotSupported is returned by provider methods that are not supported.
var ErrNotSupported = errors.New("operation not supported by this provider")

// Capability is a bitmask describing what a storage provider can do.
type Capability int

const (
	CapLocal     Capability = 1 << iota // serve from local disk
	CapStreaming                        // stream via provider (e.g. rclone cat)
	CapURL                              // generate signed/permanent URLs
)

// Has returns true if c includes the given capability.
func (c Capability) Has(flag Capability) bool {
	return c&flag != 0
}

// RemoteFileInfo describes a file in remote storage.
type RemoteFileInfo struct {
	Path        string
	Size        int64
	ContentType string
}

// FileMetadata holds metadata about a local file.
type FileMetadata struct {
	Size        int64
	ContentType string
	ModTime     time.Time
}

// StorageProvider is the transport-agnostic interface for storage backends.
// Implementations may use subprocesses (rclone), HTTP APIs (native cloud),
// SDKs, or any other mechanism. No assumptions are made about the transport.
type StorageProvider interface {
	// Name returns the provider identifier (e.g. "local", "rclone").
	Name() string

	// Capabilities returns a bitmask of what this provider supports.
	Capabilities() Capability

	// Check validates that the provider is ready (binary exists, credentials
	// valid, remote accessible, etc.). Called on worker startup.
	Check(ctx context.Context) error

	// Upload copies a local directory to remote storage under the given key.
	// Returns a URI like "rclone://remote:bucket/storageKey".
	Upload(ctx context.Context, localDir, storageKey string) (remoteURI string, err error)

	// Delete removes all files for the given remote URI from storage.
	Delete(ctx context.Context, remoteURI string) error

	// GenerateURL generates a signed or permanent URL for a specific file.
	// Returns ErrNotSupported if the provider does not support URL generation.
	GenerateURL(ctx context.Context, remoteURI, fileRelPath string, expiry time.Duration) (string, error)

	// ServeFile streams a remote file to the HTTP response, handling Range
	// headers internally.
	ServeFile(ctx context.Context, remoteURI, fileRelPath string, w http.ResponseWriter, r *http.Request) error

	// ListFiles lists all files under the given remote URI.
	ListFiles(ctx context.Context, remoteURI string) ([]RemoteFileInfo, error)
}
