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
	EngineJobID string
	Name        string
	Status      string
	Progress    float64
	Speed       int64
	Size        int64
	Error       string
}

type NodeEvent struct {
	NodeID        string
	DiskAvailable int64
}
