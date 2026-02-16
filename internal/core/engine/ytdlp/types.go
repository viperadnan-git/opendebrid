package ytdlp

import "github.com/opendebrid/opendebrid/internal/core/engine"

// InfoJSON is the parsed output of yt-dlp --dump-json
type InfoJSON struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Extractor   string  `json:"extractor"`
	ExtractorKey string `json:"extractor_key"`
	URL         string  `json:"url"`
	WebpageURL  string  `json:"webpage_url"`
	Duration    float64 `json:"duration"`
	Filename    string  `json:"filename"`
	Filesize    int64   `json:"filesize"`
	FilesizeApprox int64 `json:"filesize_approx"`
	Format      string  `json:"format"`
	FormatID    string  `json:"format_id"`
	Thumbnail   string  `json:"thumbnail"`
	Description string  `json:"description"`
	Entries     []InfoJSON `json:"entries"` // for playlists
}

// jobState tracks a running yt-dlp download
type jobState struct {
	JobID       string
	URL         string
	DownloadDir string
	Status      engine.JobState
	EngineState string
	Progress    float64
	Speed       int64
	TotalSize   int64
	Downloaded  int64
	Error       string
	Files       []engine.FileInfo
	Done        chan struct{}
}
