package fileserver

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
	"github.com/viperadnan-git/opendebrid/internal/core/storage"
	"github.com/viperadnan-git/opendebrid/internal/database/gen"
)

// Server serves files via DB-backed download tokens.
// URL pattern: /dl/{token}/{filename}
type Server struct {
	queries  *gen.Queries
	storage  *storage.LocalProvider
	basePath string
}

func NewServer(db *pgxpool.Pool, basePath string) *Server {
	absPath, _ := filepath.Abs(basePath)
	return &Server{
		queries:  gen.New(db),
		storage:  storage.NewLocalProvider(absPath),
		basePath: absPath,
	}
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

	// Look up token in DB (also checks expiry)
	link, err := s.queries.GetDownloadLinkByToken(r.Context(), token)
	if err != nil {
		log.Debug().Err(err).Str("token", token).Msg("invalid download token")
		http.Error(w, "invalid or expired link", http.StatusForbidden)
		return
	}

	// Increment access count (best-effort, non-critical)
	_ = s.queries.IncrementLinkAccess(r.Context(), token)

	// Resolve file path (prevent path traversal)
	filePath := link.FilePath
	fullPath, _ := filepath.Abs(filepath.Clean(filePath))
	absBase, _ := filepath.Abs(s.basePath)
	if !strings.HasPrefix(fullPath, absBase) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	// Open file
	uri := "file://" + fullPath
	log.Debug().Str("token", token).Str("file_path", fullPath).Msg("serving file")
	f, meta, err := s.storage.Open(r.Context(), uri)
	if err != nil {
		log.Debug().Err(err).Str("uri", uri).Msg("file not found")
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	defer func() { _ = f.Close() }()

	// Set headers
	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(filePath)))

	// ServeContent handles Range requests automatically
	http.ServeContent(w, r, filepath.Base(filePath), meta.ModTime, f)
}
