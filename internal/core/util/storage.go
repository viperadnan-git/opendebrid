package util

import (
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"
)

// IsStorageKeyDir checks if a directory name looks like a storage key (32 hex chars).
func IsStorageKeyDir(name string) bool {
	if len(name) != 32 {
		return false
	}
	for _, c := range name {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ScanStorageKeys scans the given directories for storage key subdirectories
// and returns deduplicated storage keys.
func ScanStorageKeys(dirs []string) []string {
	seen := make(map[string]bool)
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() && IsStorageKeyDir(entry.Name()) {
				seen[entry.Name()] = true
			}
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	return keys
}

// RemoveOrphanedDirs removes storage key directories not in the valid set.
func RemoveOrphanedDirs(dirs []string, validKeys map[string]bool) {
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || !IsStorageKeyDir(entry.Name()) {
				continue
			}
			if validKeys[entry.Name()] {
				continue
			}
			fullPath := filepath.Join(dir, entry.Name())
			log.Info().Str("path", fullPath).Msg("removing orphaned download directory")
			_ = os.RemoveAll(fullPath)
		}
	}
}
