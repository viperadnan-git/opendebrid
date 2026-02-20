// Package enginesetup wires together the engine implementations (aria2, yt-dlp)
// into a ready Registry and process Manager. It sits above the engine interface
// package and its implementations so it can import both without a cycle.
package enginesetup

import (
	"context"
	"os/exec"

	"github.com/rs/zerolog/log"
	appconfig "github.com/viperadnan-git/opendebrid/internal/config"
	"github.com/viperadnan-git/opendebrid/internal/core/engine"
	"github.com/viperadnan-git/opendebrid/internal/core/engine/aria2"
	"github.com/viperadnan-git/opendebrid/internal/core/engine/ytdlp"
	"github.com/viperadnan-git/opendebrid/internal/core/process"
)

// Aria2SetupConfig holds aria2 engine configuration for InitEngines.
type Aria2SetupConfig struct {
	Enabled       bool
	DownloadDir   string
	MaxConcurrent int
	RPCURL        string
	RPCSecret     string
}

// YtDlpSetupConfig holds yt-dlp engine configuration for InitEngines.
type YtDlpSetupConfig struct {
	Enabled       bool
	Binary        string
	DownloadDir   string
	MaxConcurrent int
	DefaultFormat string
}

// ConfigFromAppConfig builds an engine Config from the application configuration.
func ConfigFromAppConfig(cfg *appconfig.Config) Config {
	return Config{
		Aria2: Aria2SetupConfig{
			Enabled:       cfg.Engines.Aria2.Enabled,
			DownloadDir:   cfg.Engines.Aria2.DownloadDir,
			MaxConcurrent: cfg.Engines.Aria2.MaxConcurrent,
			RPCURL:        cfg.Engines.Aria2.RPCURL,
			RPCSecret:     cfg.Engines.Aria2.RPCSecret,
		},
		YtDlp: YtDlpSetupConfig{
			Enabled:       cfg.Engines.YtDlp.Enabled,
			Binary:        cfg.Engines.YtDlp.Binary,
			DownloadDir:   cfg.Engines.YtDlp.DownloadDir,
			MaxConcurrent: cfg.Engines.YtDlp.MaxConcurrent,
			DefaultFormat: cfg.Engines.YtDlp.DefaultFormat,
		},
	}
}

// Config is the input for InitEngines.
type Config struct {
	Aria2 Aria2SetupConfig
	YtDlp YtDlpSetupConfig
}

// Result holds the outputs of InitEngines.
type Result struct {
	Registry    *engine.Registry
	ProcMgr     *process.Manager
	YtDlpEngine *ytdlp.Engine // exposed for the controller's /api/v1/ytdlp/info endpoint; may be nil
}

// InitEngines initialises all configured engines, registers their daemons, and
// returns a ready Registry and process Manager. Binary presence is always
// checked (LookPath) before attempting engine init.
func InitEngines(ctx context.Context, cfg Config) *Result {
	registry := engine.NewRegistry()
	procMgr := process.NewManager()

	tryInitAria2(ctx, cfg.Aria2, registry, procMgr)
	ytdlpEngine := tryInitYtDlp(ctx, cfg.YtDlp, registry)

	return &Result{
		Registry:    registry,
		ProcMgr:     procMgr,
		YtDlpEngine: ytdlpEngine,
	}
}

func tryInitAria2(ctx context.Context, cfg Aria2SetupConfig, registry *engine.Registry, procMgr *process.Manager) {
	if !cfg.Enabled {
		return
	}
	if _, err := exec.LookPath("aria2c"); err != nil {
		log.Info().Msg("aria2c not found in PATH, skipping")
		return
	}
	eng := aria2.New()
	if err := eng.Init(ctx, engine.EngineConfig{
		DownloadDir:   cfg.DownloadDir,
		MaxConcurrent: cfg.MaxConcurrent,
		Extra: aria2.Config{
			RPCURL:    cfg.RPCURL,
			RPCSecret: cfg.RPCSecret,
		},
	}); err != nil {
		log.Warn().Err(err).Msg("aria2 engine init failed")
		return
	}
	registry.Register(eng)
	if de, ok := engine.Engine(eng).(engine.DaemonEngine); ok {
		procMgr.Register(de.Daemon())
	}
	log.Info().Msg("aria2 engine registered")
}

func tryInitYtDlp(ctx context.Context, cfg YtDlpSetupConfig, registry *engine.Registry) *ytdlp.Engine {
	if !cfg.Enabled {
		return nil
	}
	binary := cfg.Binary
	if binary == "" {
		binary = "yt-dlp"
	}
	if _, err := exec.LookPath(binary); err != nil {
		log.Warn().Str("binary", binary).Msg("yt-dlp not found in PATH, skipping")
		return nil
	}
	eng := ytdlp.New()
	if err := eng.Init(ctx, engine.EngineConfig{
		DownloadDir:   cfg.DownloadDir,
		MaxConcurrent: cfg.MaxConcurrent,
		Extra: ytdlp.Config{
			Binary:        binary,
			DefaultFormat: cfg.DefaultFormat,
		},
	}); err != nil {
		log.Warn().Err(err).Msg("yt-dlp engine init failed")
		return nil
	}
	registry.Register(eng)
	log.Info().Msg("yt-dlp engine registered")
	return eng
}
