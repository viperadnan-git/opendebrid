package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// RcloneConfig holds the configuration for an rclone-based storage provider.
type RcloneConfig struct {
	RemoteName string            `json:"remote_name"`      // e.g. "myremote"
	BasePath   string            `json:"base_path"`        // e.g. "mybucket/opendebrid"
	Binary     string            `json:"binary"`           // default: "rclone"
	ServeMode  string            `json:"serve_mode"`       // "streaming" (default) or "url"
	Config     map[string]string `json:"config,omitempty"` // rclone remote params — if set, creates remote via "rclone config create"
}

// RcloneStorageProvider implements Provider using the rclone CLI.
type RcloneStorageProvider struct {
	cfg RcloneConfig
}

// NewRcloneStorageProvider creates a new rclone-based storage provider.
func NewRcloneStorageProvider(cfg RcloneConfig) *RcloneStorageProvider {
	if cfg.Binary == "" {
		cfg.Binary = "rclone"
	}
	return &RcloneStorageProvider{cfg: cfg}
}

func (p *RcloneStorageProvider) Name() string { return "rclone" }

func (p *RcloneStorageProvider) Capabilities() Capability {
	caps := CapStreaming
	if p.cfg.ServeMode == "url" {
		caps |= CapURL
	}
	return caps
}

// Check validates that the rclone binary exists and the remote is accessible.
// If Config params are provided, the remote is created/updated first via "rclone config create".
func (p *RcloneStorageProvider) Check(ctx context.Context) error {
	log.Debug().Str("binary", p.cfg.Binary).Str("remote", p.cfg.RemoteName).Msg("checking rclone provider")

	// Verify binary exists and get version
	start := time.Now()
	out, err := p.run(ctx, "version")
	if err != nil {
		return fmt.Errorf("rclone binary not found or not working: %w", err)
	}
	version := strings.SplitN(string(out), "\n", 2)[0]
	log.Debug().Str("version", version).Dur("duration", time.Since(start)).Msg("rclone binary found")

	// Create/update remote from inline config if provided
	if err := p.ensureRemote(ctx); err != nil {
		return fmt.Errorf("rclone config create: %w", err)
	}

	// Verify remote is accessible
	start = time.Now()
	if _, err := p.run(ctx, "lsd", p.remotePath("")); err != nil {
		return fmt.Errorf("rclone remote %q not accessible: %w", p.cfg.RemoteName, err)
	}
	log.Info().Str("remote", p.cfg.RemoteName).Str("base_path", p.cfg.BasePath).Dur("duration", time.Since(start)).Msg("rclone provider ready")

	return nil
}

// ensureRemote creates or updates the rclone remote from inline Config params.
// This is a no-op if Config is empty (assumes remote is pre-configured).
func (p *RcloneStorageProvider) ensureRemote(ctx context.Context) error {
	if len(p.cfg.Config) == 0 {
		return nil
	}

	remoteType, ok := p.cfg.Config["type"]
	if !ok || remoteType == "" {
		return fmt.Errorf("rclone config requires a 'type' field")
	}

	// Build: rclone config create <name> <type> key=value key=value ...
	args := []string{"config", "create", p.cfg.RemoteName, remoteType}
	for k, v := range p.cfg.Config {
		if k == "type" {
			continue
		}
		args = append(args, k+"="+v)
	}

	log.Debug().Str("remote", p.cfg.RemoteName).Str("type", remoteType).Msg("creating rclone remote from inline config")
	if _, err := p.run(ctx, args...); err != nil {
		return err
	}
	log.Info().Str("remote", p.cfg.RemoteName).Str("type", remoteType).Msg("rclone remote created")
	return nil
}

// Upload copies a local directory to remote storage.
func (p *RcloneStorageProvider) Upload(ctx context.Context, localDir, storageKey string) (string, error) {
	dest := p.remotePath(storageKey)
	log.Debug().Str("local_dir", localDir).Str("dest", dest).Str("storage_key", storageKey).Msg("starting rclone upload")

	start := time.Now()
	if _, err := p.run(ctx, "copy", localDir, dest); err != nil {
		log.Warn().Err(err).Str("storage_key", storageKey).Dur("duration", time.Since(start)).Msg("rclone upload failed")
		return "", fmt.Errorf("rclone copy: %w", err)
	}

	uri := "rclone://" + dest
	log.Info().Str("storage_key", storageKey).Str("remote_uri", uri).Dur("duration", time.Since(start)).Msg("rclone upload completed")
	return uri, nil
}

// Delete removes all files for the given remote URI.
func (p *RcloneStorageProvider) Delete(ctx context.Context, remoteURI string) error {
	dest := strings.TrimPrefix(remoteURI, "rclone://")
	log.Debug().Str("remote_uri", remoteURI).Msg("deleting from rclone storage")

	start := time.Now()
	if _, err := p.run(ctx, "purge", dest); err != nil {
		log.Warn().Err(err).Str("remote_uri", remoteURI).Dur("duration", time.Since(start)).Msg("rclone delete failed")
		return fmt.Errorf("rclone purge: %w", err)
	}

	log.Info().Str("remote_uri", remoteURI).Dur("duration", time.Since(start)).Msg("rclone delete completed")
	return nil
}

// GenerateURL generates a URL for the given file using rclone link.
func (p *RcloneStorageProvider) GenerateURL(ctx context.Context, remoteURI, fileRelPath string, expiry time.Duration) (string, error) {
	if p.cfg.ServeMode != "url" {
		return "", ErrNotSupported
	}

	dest := strings.TrimPrefix(remoteURI, "rclone://")
	if fileRelPath != "" {
		dest += "/" + fileRelPath
	}

	log.Debug().Str("dest", dest).Dur("expiry", expiry).Msg("generating rclone link")

	args := []string{"link", dest}
	if expiry > 0 {
		args = append(args, "--expire", expiry.String())
	}
	out, err := p.run(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("rclone link: %w", err)
	}

	link := strings.TrimSpace(string(out))
	log.Debug().Str("link", link).Msg("rclone link generated")
	return link, nil
}

// ServeFile streams a remote file to the HTTP response with Range support.
func (p *RcloneStorageProvider) ServeFile(ctx context.Context, remoteURI, fileRelPath string, w http.ResponseWriter, r *http.Request) error {
	dest := strings.TrimPrefix(remoteURI, "rclone://")
	if fileRelPath != "" {
		dest += "/" + fileRelPath
	}

	// Get file size for Range header support
	size, err := p.fileSize(ctx, dest)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return fmt.Errorf("rclone size: %w", err)
	}

	// Determine content type and filename from the path
	filename := filepath.Base(dest)
	contentType := mime.TypeByExtension(filepath.Ext(filename))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Parse Range header
	rangeHeader := r.Header.Get("Range")
	var offset, length int64
	statusCode := http.StatusOK

	if rangeHeader != "" {
		start, end, ok := parseRangeHeader(rangeHeader, size)
		if !ok {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			http.Error(w, "invalid range", http.StatusRequestedRangeNotSatisfiable)
			return nil
		}
		offset = start
		length = end - start + 1
		statusCode = http.StatusPartialContent
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
	} else {
		length = size
	}

	log.Debug().
		Str("dest", dest).
		Int64("offset", offset).
		Int64("length", length).
		Str("range", rangeHeader).
		Msg("serving file from rclone")

	// Build rclone cat command
	args := []string{"cat", dest}
	if offset > 0 {
		args = append(args, "--offset", strconv.FormatInt(offset, 10))
	}
	if rangeHeader != "" {
		args = append(args, "--count", strconv.FormatInt(length, 10))
	}

	cmd := p.command(ctx, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("rclone cat pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("rclone cat start: %w", err)
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.WriteHeader(statusCode)

	if _, err := io.Copy(w, stdout); err != nil {
		log.Debug().Err(err).Str("dest", dest).Msg("client disconnected during rclone stream")
	}

	if err := cmd.Wait(); err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		if stderrStr != "" {
			log.Warn().Str("stderr", stderrStr).Str("dest", dest).Msg("rclone cat stderr")
		}
	}

	return nil
}

// rcloneLsjsonEntry represents a single entry from rclone lsjson output.
type rcloneLsjsonEntry struct {
	Path    string `json:"Path"`
	Name    string `json:"Name"`
	Size    int64  `json:"Size"`
	IsDir   bool   `json:"IsDir"`
	ModTime string `json:"ModTime"`
}

// ListFiles lists all files under the given remote URI.
func (p *RcloneStorageProvider) ListFiles(ctx context.Context, remoteURI string) ([]RemoteFileInfo, error) {
	dest := strings.TrimPrefix(remoteURI, "rclone://")

	log.Debug().Str("dest", dest).Msg("listing files from rclone storage")

	start := time.Now()
	out, err := p.run(ctx, "lsjson", "--recursive", dest)
	if err != nil {
		return nil, fmt.Errorf("rclone lsjson: %w", err)
	}

	var entries []rcloneLsjsonEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("parse rclone lsjson output: %w", err)
	}

	var files []RemoteFileInfo
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		ct := mime.TypeByExtension(filepath.Ext(e.Name))
		if ct == "" {
			ct = "application/octet-stream"
		}
		files = append(files, RemoteFileInfo{
			Path:        e.Path,
			Size:        e.Size,
			ContentType: ct,
		})
	}

	log.Debug().Int("count", len(files)).Dur("duration", time.Since(start)).Msg("rclone file listing complete")
	return files, nil
}

// remotePath builds the full rclone remote path for a storage key.
func (p *RcloneStorageProvider) remotePath(storageKey string) string {
	base := p.cfg.RemoteName + ":" + p.cfg.BasePath
	if storageKey != "" {
		base += "/" + storageKey
	}
	return base
}

// command builds an exec.Cmd for an rclone command.
func (p *RcloneStorageProvider) command(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, p.cfg.Binary, args...)
}

// run executes an rclone command and returns stdout.
func (p *RcloneStorageProvider) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := p.command(ctx, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Debug().Strs("args", args).Msg("running rclone command")
	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)

	if err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		if stderrStr != "" {
			log.Warn().Str("stderr", stderrStr).Strs("args", args).Msg("rclone command stderr")
		}
		return nil, fmt.Errorf("%s (stderr: %s)", err, stderrStr)
	}

	log.Debug().Dur("duration", dur).Strs("args", args).Msg("rclone command completed")
	return stdout.Bytes(), nil
}

// fileSize gets the size of a remote file using rclone size.
func (p *RcloneStorageProvider) fileSize(ctx context.Context, dest string) (int64, error) {
	out, err := p.run(ctx, "size", "--json", dest)
	if err != nil {
		return 0, err
	}
	var result struct {
		Bytes int64 `json:"bytes"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return 0, fmt.Errorf("parse rclone size: %w", err)
	}
	return result.Bytes, nil
}

// parseRangeHeader parses an HTTP Range header value and returns start, end.
// Returns ok=false if the range is invalid.
func parseRangeHeader(rangeHeader string, totalSize int64) (start, end int64, ok bool) {
	if !strings.HasPrefix(rangeHeader, "bytes=") {
		return 0, 0, false
	}
	spec := strings.TrimPrefix(rangeHeader, "bytes=")
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}

	if parts[0] == "" {
		// Suffix range: bytes=-500
		suffixLen, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || suffixLen <= 0 {
			return 0, 0, false
		}
		start = max(totalSize-suffixLen, 0)
		return start, totalSize - 1, true
	}

	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 || start >= totalSize {
		return 0, 0, false
	}

	if parts[1] == "" {
		// Open-ended: bytes=100-
		return start, totalSize - 1, true
	}

	end, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil || end < start {
		return 0, 0, false
	}
	if end >= totalSize {
		end = totalSize - 1
	}

	return start, end, true
}
