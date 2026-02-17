package cache

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/viperadnan-git/opendebrid/internal/core/event"
	"github.com/viperadnan-git/opendebrid/internal/database/gen"
	"github.com/rs/zerolog/log"
)

// Manager handles global cache lookup and invalidation.
type Manager struct {
	queries *gen.Queries
	bus     event.Bus
}

func NewManager(db *pgxpool.Pool, bus event.Bus) *Manager {
	return &Manager{
		queries: gen.New(db),
		bus:     bus,
	}
}

// SetupSubscribers subscribes to EventBus for cache management.
func (m *Manager) SetupSubscribers() {
	// On job completed: insert cache entry
	m.bus.Subscribe(event.EventJobCompleted, func(ctx context.Context, e event.Event) error {
		payload, ok := e.Payload.(event.JobEvent)
		if !ok || payload.CacheKey == "" {
			return nil
		}

		_, err := m.queries.InsertCacheEntry(ctx, gen.InsertCacheEntryParams{
			CacheKey:     payload.CacheKey,
			JobID:        strToUUID(payload.JobID),
			NodeID:       payload.NodeID,
			Engine:       payload.Engine,
			FileLocation: "", // filled later by file server
		})
		if err != nil {
			log.Error().Err(err).Str("cache_key", payload.CacheKey).Msg("failed to insert cache entry")
		}
		return nil
	})

	// On job failed: clean up cache entry
	m.bus.Subscribe(event.EventJobFailed, func(ctx context.Context, e event.Event) error {
		payload, ok := e.Payload.(event.JobEvent)
		if !ok || payload.CacheKey == "" {
			return nil
		}
		m.queries.DeleteCacheEntry(ctx, payload.CacheKey)
		return nil
	})

	// On node offline: invalidate that node's cache entries
	m.bus.Subscribe(event.EventNodeOffline, func(ctx context.Context, e event.Event) error {
		payload, ok := e.Payload.(event.NodeEvent)
		if !ok {
			return nil
		}
		m.queries.DeleteCacheByNode(ctx, payload.NodeID)
		log.Info().Str("node_id", payload.NodeID).Msg("invalidated cache entries for offline node")
		return nil
	})
}

// Lookup checks if a cache key exists and the node is online.
func (m *Manager) Lookup(ctx context.Context, cacheKey string) (*gen.CacheEntry, error) {
	entry, err := m.queries.LookupCache(ctx, cacheKey)
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

func strToUUID(s string) pgtype.UUID {
	var u pgtype.UUID
	clean := ""
	for _, c := range s {
		if c != '-' {
			clean += string(c)
		}
	}
	if len(clean) != 32 {
		return u
	}
	for i := 0; i < 16; i++ {
		u.Bytes[i] = hexByte(clean[i*2])<<4 | hexByte(clean[i*2+1])
	}
	u.Valid = true
	return u
}

func hexByte(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}
