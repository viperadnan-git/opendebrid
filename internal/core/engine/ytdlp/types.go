package ytdlp

import (
	"context"
	"sync"

	"github.com/viperadnan-git/opendebrid/internal/core/engine"
)

// InfoJSON is the parsed output of yt-dlp --dump-json
type InfoJSON struct {
	ID             string     `json:"id"`
	Title          string     `json:"title"`
	Extractor      string     `json:"extractor"`
	ExtractorKey   string     `json:"extractor_key"`
	URL            string     `json:"url"`
	WebpageURL     string     `json:"webpage_url"`
	Duration       float64    `json:"duration"`
	Filename       string     `json:"filename"`
	Filesize       int64      `json:"filesize"`
	FilesizeApprox int64      `json:"filesize_approx"`
	Format         string     `json:"format"`
	FormatID       string     `json:"format_id"`
	Thumbnail      string     `json:"thumbnail"`
	Description    string     `json:"description"`
	Entries        []InfoJSON `json:"entries"` // for playlists
}

// jobState tracks a running yt-dlp download.
// mu protects mutable fields written by the background runDownload goroutine.
type jobState struct {
	mu sync.Mutex

	// Immutable after creation
	JobID       string
	URL         string
	DownloadDir string
	Done        chan struct{}
	cancel      context.CancelFunc

	// Mutable — protected by mu
	Name        string
	Status      engine.JobState
	EngineState string
	Progress    float64
	Speed       int64
	TotalSize   int64
	Downloaded  int64
	Error       string
	Files       []engine.FileInfo
}

// snapshot returns a copy of the mutable fields under lock.
func (s *jobState) snapshot() jobSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return jobSnapshot{
		Name:        s.Name,
		Status:      s.Status,
		EngineState: s.EngineState,
		Progress:    s.Progress,
		Speed:       s.Speed,
		TotalSize:   s.TotalSize,
		Downloaded:  s.Downloaded,
		Error:       s.Error,
	}
}

type jobSnapshot struct {
	Name        string
	Status      engine.JobState
	EngineState string
	Progress    float64
	Speed       int64
	TotalSize   int64
	Downloaded  int64
	Error       string
}
