package storage

import (
	"fmt"
	"strings"
)

// StorageSettings holds the global storage provider configuration.
// Serialized as JSON in the settings table and distributed to workers.
type StorageSettings struct {
	Provider string        `json:"provider"` // "local" or "rclone"
	Rclone   *RcloneConfig `json:"rclone,omitempty"`
}

// MustLocalStorageProvider returns a local storage provider (no remote capabilities).
func MustLocalStorageProvider() StorageProvider {
	return NewLocalStorageProvider("")
}

// NewStorageProvider creates a StorageProvider from the given settings.
func NewStorageProvider(settings StorageSettings) (StorageProvider, error) {
	switch settings.Provider {
	case "", "local":
		return NewLocalStorageProvider(""), nil
	case "rclone":
		if settings.Rclone == nil {
			return nil, fmt.Errorf("rclone provider requires rclone config")
		}
		return NewRcloneStorageProvider(*settings.Rclone), nil
	default:
		return nil, fmt.Errorf("unknown storage provider %q", settings.Provider)
	}
}

// IsRemoteURI returns true if the URI refers to remote storage (not local filesystem).
func IsRemoteURI(uri string) bool {
	return strings.HasPrefix(uri, "rclone://")
}

// BuildRemoteFileURI combines a base remote URI with a file-relative path.
func BuildRemoteFileURI(remoteURI, fileRelPath string) string {
	if fileRelPath == "" {
		return remoteURI
	}
	return strings.TrimRight(remoteURI, "/") + "/" + fileRelPath
}
