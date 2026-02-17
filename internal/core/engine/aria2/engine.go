package aria2

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/opendebrid/opendebrid/internal/core/engine"
	"github.com/rs/zerolog/log"
)

const trackersURL = "https://raw.githubusercontent.com/ngosang/trackerslist/master/trackers_all.txt"

type Engine struct {
	client        *Client
	downloadDir   string
	maxConcurrent int
	trackers      []string
}

func New() *Engine {
	return &Engine{}
}

func (e *Engine) Name() string { return "aria2" }

func (e *Engine) Capabilities() engine.Capabilities {
	return engine.Capabilities{
		AcceptsSchemes:   []string{"magnet", "http", "https", "ftp"},
		AcceptsMIME:      []string{"application/x-bittorrent"},
		SupportsPlaylist: false,
		SupportsStreaming: false,
		SupportsInfo:     false,
	}
}

func (e *Engine) Init(_ context.Context, cfg engine.EngineConfig) error {
	rpcURL := cfg.Extra["rpc_url"]
	if rpcURL == "" {
		rpcURL = "http://localhost:6800/jsonrpc"
	}
	e.client = NewClient(rpcURL, cfg.Extra["rpc_secret"])
	e.downloadDir = cfg.DownloadDir
	e.maxConcurrent = cfg.MaxConcurrent

	if err := os.MkdirAll(e.downloadDir, 0o755); err != nil {
		return fmt.Errorf("create download dir: %w", err)
	}

	e.trackers = fetchTrackers(trackersURL)
	log.Info().Int("count", len(e.trackers)).Msg("loaded bt trackers")

	return nil
}

func (e *Engine) Client() *Client       { return e.client }
func (e *Engine) DownloadDir() string    { return e.downloadDir }
func (e *Engine) Trackers() []string     { return e.trackers }

func (e *Engine) Start(_ context.Context) error {
	return nil
}

func (e *Engine) Stop(_ context.Context) error {
	return nil
}

func (e *Engine) Health(ctx context.Context) engine.HealthStatus {
	start := time.Now()
	version, err := e.client.GetVersion(ctx)
	latency := time.Since(start)
	if err != nil {
		return engine.HealthStatus{OK: false, Message: err.Error(), Latency: latency}
	}
	return engine.HealthStatus{OK: true, Message: "aria2 " + version, Latency: latency}
}

func (e *Engine) Add(ctx context.Context, req engine.AddRequest) (engine.AddResponse, error) {
	jobDir := filepath.Join(e.downloadDir, req.JobID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return engine.AddResponse{}, fmt.Errorf("create job dir: %w", err)
	}

	opts := map[string]string{
		"dir": jobDir,
	}
	// Add trackers for magnet links to speed up peer discovery
	if strings.HasPrefix(req.URL, "magnet:") {
		if _, ok := opts["bt-tracker"]; !ok && len(e.trackers) > 0 {
			opts["bt-tracker"] = strings.Join(e.trackers, ",")
		}
	}
	for k, v := range req.Options {
		opts[k] = v
	}

	log.Debug().Str("job_id", req.JobID).Str("url", req.URL).Msg("aria2 adding URI")

	gid, err := e.client.AddURI(ctx, []string{req.URL}, opts)
	if err != nil {
		return engine.AddResponse{}, fmt.Errorf("aria2 add: %w", err)
	}

	log.Debug().Str("job_id", req.JobID).Str("gid", gid).Msg("aria2 URI added")

	return engine.AddResponse{
		EngineJobID: gid,
	}, nil
}

func (e *Engine) Status(ctx context.Context, engineJobID string) (engine.JobStatus, error) {
	s, err := e.client.TellStatus(ctx, engineJobID)
	if err != nil {
		return engine.JobStatus{}, fmt.Errorf("aria2 status: %w", err)
	}

	// When aria2 downloads a .torrent file via HTTP, the original GID completes
	// and a new GID is created for the actual torrent download. Follow the chain.
	if len(s.FollowedBy) > 0 {
		followedGID := s.FollowedBy[0]
		log.Debug().Str("gid", engineJobID).Str("followed_by", followedGID).Msg("aria2 GID chain detected")
		fs, err := e.client.TellStatus(ctx, followedGID)
		if err == nil {
			s = fs
			engineJobID = followedGID
		}
	}

	state, engineState := mapStatus(s.Status, s.Seeder == "true")
	total, _ := strconv.ParseInt(s.TotalLength, 10, 64)
	completed, _ := strconv.ParseInt(s.CompletedLength, 10, 64)
	speed, _ := strconv.ParseInt(s.DownloadSpeed, 10, 64)

	var progress float64
	if total > 0 {
		progress = float64(completed) / float64(total)
	}

	var eta time.Duration
	if speed > 0 && total > completed {
		eta = time.Duration((total-completed)/speed) * time.Second
	}

	js := engine.JobStatus{
		EngineJobID:    engineJobID,
		State:          state,
		EngineState:    engineState,
		Progress:       progress,
		Speed:          speed,
		TotalSize:      total,
		DownloadedSize: completed,
		ETA:            eta,
		Extra:          make(map[string]any),
	}

	if s.ErrorMessage != "" {
		js.Error = s.ErrorMessage
	}
	if s.InfoHash != "" {
		js.Extra["info_hash"] = s.InfoHash
	}
	if s.NumSeeders != "" {
		js.Extra["seeders"] = s.NumSeeders
	}

	return js, nil
}

func (e *Engine) ListFiles(ctx context.Context, engineJobID string) ([]engine.FileInfo, error) {
	s, err := e.client.TellStatus(ctx, engineJobID)
	if err != nil {
		return nil, fmt.Errorf("aria2 list files: %w", err)
	}

	var files []engine.FileInfo
	for _, f := range s.Files {
		size, _ := strconv.ParseInt(f.Length, 10, 64)
		if f.Path == "" {
			continue
		}
		relPath, _ := filepath.Rel(e.downloadDir, f.Path)
		files = append(files, engine.FileInfo{
			Path:       relPath,
			Size:       size,
			StorageURI: "file://" + f.Path,
		})
	}
	return files, nil
}

func (e *Engine) Cancel(ctx context.Context, engineJobID string) error {
	if err := e.client.ForceRemove(ctx, engineJobID); err != nil {
		return fmt.Errorf("aria2 cancel: %w", err)
	}
	return nil
}

func (e *Engine) Remove(ctx context.Context, engineJobID string) error {
	_ = e.client.ForceRemove(ctx, engineJobID)
	_ = e.client.RemoveDownloadResult(ctx, engineJobID)
	return nil
}

func (e *Engine) ResolveCacheKey(ctx context.Context, rawURL string) (engine.CacheKey, error) {
	// Magnet links: extract info_hash
	if strings.HasPrefix(rawURL, "magnet:") {
		u, err := url.Parse(rawURL)
		if err == nil {
			xt := u.Query().Get("xt")
			// xt=urn:btih:<hash>
			if strings.HasPrefix(xt, "urn:btih:") {
				hash := strings.ToLower(strings.TrimPrefix(xt, "urn:btih:"))
				return engine.CacheKey{Type: engine.CacheKeyHash, Value: hash}, nil
			}
		}
	}

	// HTTP/HTTPS: try downloading as torrent to extract info hash
	if strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://") {
		if hash, err := fetchTorrentInfoHash(ctx, rawURL); err == nil {
			return engine.CacheKey{Type: engine.CacheKeyHash, Value: hash}, nil
		}
	}

	// Fallback: SHA256 of URL
	h := sha256.Sum256([]byte(rawURL))
	return engine.CacheKey{
		Type:  engine.CacheKeyURL,
		Value: fmt.Sprintf("%x", h),
	}, nil
}

// fallbackTrackers are used when the remote tracker list cannot be fetched.
var fallbackTrackers = []string{
	"udp://tracker.opentrackr.org:1337/announce",
	"udp://open.tracker.cl:1337/announce",
	"udp://tracker.openbittorrent.com:6969/announce",
	"udp://open.stealth.si:80/announce",
	"udp://exodus.desync.com:6969/announce",
	"udp://tracker.torrent.eu.org:451/announce",
}

// fetchTrackers downloads a newline-separated tracker list from the given URL.
// Falls back to the hardcoded list on failure.
func fetchTrackers(rawURL string) []string {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(rawURL)
	if err != nil {
		log.Warn().Err(err).Msg("failed to fetch tracker list, using fallback")
		return fallbackTrackers
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Warn().Int("status", resp.StatusCode).Msg("tracker list HTTP error, using fallback")
		return fallbackTrackers
	}

	var trackers []string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			trackers = append(trackers, line)
		}
	}

	if len(trackers) == 0 {
		log.Warn().Msg("tracker list was empty, using fallback")
		return fallbackTrackers
	}

	return trackers
}
