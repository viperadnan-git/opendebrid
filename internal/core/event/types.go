package event

import "time"

type EventType string

const (
	// Job lifecycle
	EventJobCreated   EventType = "job.created"
	EventJobStarted   EventType = "job.started"
	EventJobProgress  EventType = "job.progress"
	EventJobCompleted EventType = "job.completed"
	EventJobFailed    EventType = "job.failed"
	EventJobCancelled EventType = "job.cancelled"

	// Cache
	EventCacheHit     EventType = "cache.hit"
	EventCacheEvicted EventType = "cache.evicted"

	// Node
	EventNodeOnline  EventType = "node.online"
	EventNodeOffline EventType = "node.offline"
	EventNodeDiskLow EventType = "node.disk_low"
)

type Event struct {
	Type      EventType
	Timestamp time.Time
	Payload   any
}

type JobEvent struct {
	JobID       string
	UserID      string
	NodeID      string
	Engine      string
	CacheKey    string
	EngineJobID string
	Status      string
	Progress    float64
	Speed       int64
	Error       string
}

type CacheEvent struct {
	CacheKey string
	JobID    string
	NodeID   string
}

type NodeEvent struct {
	NodeID        string
	DiskAvailable int64
}
