package fileserver

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog/log"
	"github.com/viperadnan-git/opendebrid/internal/core/storage"
	dbgen "github.com/viperadnan-git/opendebrid/internal/database/gen"
)

// TokenResolver validates a download token and returns the relative file path.
// increment=true on the initial (non-range) request so the resolver can record the access.
type TokenResolver interface {
	ResolveToken(ctx context.Context, token string, increment bool) (relPath string, err error)
}

// DBTokenResolver validates tokens by querying download_links directly.
// Used by the controller node which has direct DB access.
type DBTokenResolver struct {
	queries *dbgen.Queries
}

func NewDBTokenResolver(queries *dbgen.Queries) TokenResolver {
	return &DBTokenResolver{queries: queries}
}

func (r *DBTokenResolver) ResolveToken(ctx context.Context, token string, increment bool) (string, error) {
	link, err := r.queries.GetDownloadLinkByToken(ctx, token)
	if err != nil {
		return "", fmt.Errorf("invalid or expired token")
	}
	if increment {
		go func() { _ = r.queries.IncrementLinkAccess(context.Background(), token) }()
	}
	return link.FilePath, nil
}

// Server serves files via short token-based download URLs.
// URL pattern: /dl/{token}/{filename}
type Server struct {
	resolver TokenResolver
	storage  *storage.LocalProvider
	basePath string
}

// NewServer creates a file server rooted at basePath.
func NewServer(basePath string, resolver TokenResolver) *Server {
	absPath, _ := filepath.Abs(basePath)
	return &Server{
		resolver: resolver,
		storage:  storage.NewLocalProvider(absPath),
		basePath: absPath,
	}
}

// RegisterRoutes registers the /dl/ download route on the given Echo instance.
func (s *Server) RegisterRoutes(e *echo.Echo) {
	e.GET("/dl/:token/:filename", echo.WrapHandler(http.HandlerFunc(s.ServeFile)))
}

// ServeFile is the http.HandlerFunc for /dl/{token}/{filename} routes.
func (s *Server) ServeFile(w http.ResponseWriter, r *http.Request) {
	s.serveFile(w, r)
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request) {
	// Parse path: /dl/{token}/{filename}
	path := strings.TrimPrefix(r.URL.Path, "/dl/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}
	token := parts[0]
	log.Debug().Str("token", token).Str("path", r.URL.Path).Msg("file download request")

	// Increment only on the initial request — range requests issued for every
	// seek/chunk during streaming would inflate the count. Treat bytes=0- as
	// initial too: some clients (browsers, media players) always send a Range
	// header even for the very first fetch.
	increment := isInitialRequest(r)
	relPath, err := s.resolver.ResolveToken(r.Context(), token, increment)
	if err != nil {
		log.Debug().Err(err).Str("token", token).Msg("invalid download token")
		http.Error(w, "invalid or expired link", http.StatusForbidden)
		return
	}

	// Resolve relative path to absolute under basePath (prevents path traversal)
	fullPath := filepath.Join(s.basePath, filepath.Clean(relPath))
	if !strings.HasPrefix(fullPath, s.basePath+string(filepath.Separator)) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	uri := "file://" + fullPath
	log.Debug().Str("file_path", fullPath).Msg("serving file")
	f, meta, err := s.storage.Open(r.Context(), uri)
	if err != nil {
		log.Debug().Err(err).Str("uri", uri).Msg("file not found")
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	defer func() { _ = f.Close() }()

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(relPath)))

	// ServeContent handles Range requests automatically
	http.ServeContent(w, r, filepath.Base(relPath), meta.ModTime, f)
}

// isInitialRequest returns true when the request should count as a new access.
// A missing Range header is the common case; bytes=0- is also treated as initial
// because some clients (browsers, media players) always include it on the first fetch.
// Mid-stream range requests (bytes=N- where N>0) are not counted.
func isInitialRequest(r *http.Request) bool {
	h := r.Header.Get("Range")
	return h == "" || strings.HasPrefix(h, "bytes=0-")
}
