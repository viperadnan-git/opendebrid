package service

import (
	"crypto/sha256"
	"fmt"

	"github.com/viperadnan-git/opendebrid/internal/core/engine"
)

// cacheKeyPrefix returns the namespace prefix for a cache key.
// Torrent info hashes use "bt:" so all torrent-based engines share one pool.
// Other key types use the engine name as prefix.
func cacheKeyPrefix(engineName string, key engine.CacheKey) string {
	if key.Type == engine.CacheKeyHash {
		return "bt:"
	}
	return engineName + ":"
}

// StorageKeyFromCacheKey derives a deterministic, filesystem-safe directory
// name from a cache key. Returns hex(sha256(cacheKey))[:32].
func StorageKeyFromCacheKey(cacheKey string) string {
	h := sha256.Sum256([]byte(cacheKey))
	return fmt.Sprintf("%x", h[:16])
}
