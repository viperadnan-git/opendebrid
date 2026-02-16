package job

import "time"

type Job struct {
	ID           string
	UserID       string
	NodeID       string
	Engine       string
	EngineJobID  string
	URL          string
	CacheKey     string
	Status       string
	EngineState  string
	FileLocation string
	ErrorMessage string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	CompletedAt  *time.Time
}
