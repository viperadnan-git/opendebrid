package fileserver

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/opendebrid/opendebrid/internal/core/storage"
	"github.com/rs/zerolog/log"
)

// Server serves files via signed URLs with range request support.
type Server struct {
	signer   *Signer
	storage  *storage.LocalProvider
	basePath string
}

func NewServer(secret string, basePath string) *Server {
	return &Server{
		signer:   NewSigner(secret),
		storage:  storage.NewLocalProvider(basePath),
		basePath: basePath,
	}
}

// Handler returns an http.Handler for serving signed download URLs.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/d/", s.serveFile)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	return mux
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request) {
	// Extract token from path: /d/{token}
	token := strings.TrimPrefix(r.URL.Path, "/d/")
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	// Verify token
	_, filePath, _, err := s.signer.Verify(token)
	if err != nil {
		log.Debug().Err(err).Msg("invalid download token")
		http.Error(w, "invalid or expired link", http.StatusForbidden)
		return
	}

	// Resolve file path (prevent path traversal)
	fullPath := filepath.Join(s.basePath, filepath.Clean(filePath))
	if !strings.HasPrefix(fullPath, s.basePath) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	// Open file
	uri := "file://" + fullPath
	f, meta, err := s.storage.Open(r.Context(), uri)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	// Set headers
	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(filePath)))

	// ServeContent handles Range requests automatically
	http.ServeContent(w, r, filepath.Base(filePath), meta.ModTime, f)
}

// GetSigner returns the signer for generating tokens externally.
func (s *Server) GetSigner() *Signer {
	return s.signer
}
