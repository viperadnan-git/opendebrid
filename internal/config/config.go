package config

import (
	"os"
	"strings"

	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

type Config struct {
	Server     ServerConfig     `koanf:"server"`
	Database   DatabaseConfig   `koanf:"database"`
	Auth       AuthConfig       `koanf:"auth"`
	Node       NodeConfig       `koanf:"node"`
	Engines    EnginesConfig    `koanf:"engines"`
	Storage    StorageConfig    `koanf:"storage"`
	Scheduler  SchedulerConfig  `koanf:"scheduler"`
	Limits     LimitsConfig     `koanf:"limits"`
	Logging    LoggingConfig    `koanf:"logging"`
	Controller ControllerConfig `koanf:"controller"`
}

type ServerConfig struct {
	Host     string `koanf:"host"`
	Port     int    `koanf:"port"`
	GRPCPort int    `koanf:"grpc_port"`
}

type DatabaseConfig struct {
	URL            string `koanf:"url"`
	MaxConnections int    `koanf:"max_connections"`
}

type AuthConfig struct {
	JWTSecret     string `koanf:"jwt_secret"`
	JWTExpiry     string `koanf:"jwt_expiry"`
	AdminUsername string `koanf:"admin_username"`
	AdminPassword string `koanf:"admin_password"`
}

type NodeConfig struct {
	ID              string `koanf:"id"`
	Name            string `koanf:"name"`
	FileServerPort  int    `koanf:"file_server_port"`
	DownloadDir     string `koanf:"download_dir"`
	WorkerAuthToken string `koanf:"worker_auth_token"`
}

type EnginesConfig struct {
	Aria2 Aria2Config `koanf:"aria2"`
	YtDlp YtDlpConfig `koanf:"ytdlp"`
}

type Aria2Config struct {
	Enabled       bool   `koanf:"enabled"`
	RPCURL        string `koanf:"rpc_url"`
	RPCSecret     string `koanf:"rpc_secret"`
	MaxConcurrent int    `koanf:"max_concurrent"`
	DownloadDir   string `koanf:"download_dir"`
}

type YtDlpConfig struct {
	Enabled       bool   `koanf:"enabled"`
	Binary        string `koanf:"binary"`
	MaxConcurrent int    `koanf:"max_concurrent"`
	DefaultFormat string `koanf:"default_format"`
	DownloadDir   string `koanf:"download_dir"`
}

type StorageConfig struct {
	Provider string `koanf:"provider"`
}

type SchedulerConfig struct {
	Adapter string `koanf:"adapter"`
}

type LimitsConfig struct {
	MinDiskFree      string `koanf:"min_disk_free"`
	PerUserConcurrent int   `koanf:"per_user_concurrent"`
	PerNodeConcurrent int   `koanf:"per_node_concurrent"`
	LinkExpiry        string `koanf:"link_expiry"`
	LRUEnabled        bool  `koanf:"lru_enabled"`
}

type LoggingConfig struct {
	Level  string `koanf:"level"`
	Format string `koanf:"format"`
}

type ControllerConfig struct {
	GRPCEndpoint string `koanf:"grpc_endpoint"`
	AuthToken    string `koanf:"auth_token"`
}

// Load reads config from TOML file (if provided) then overlays env vars.
func Load(configPath string) (*Config, error) {
	k := koanf.New(".")

	// 1. Load defaults
	if err := loadDefaults(k); err != nil {
		return nil, err
	}

	// 2. Load TOML config file if provided
	if configPath != "" {
		if err := k.Load(file.Provider(configPath), toml.Parser()); err != nil {
			return nil, err
		}
	}

	// 3. Load env vars: OD_SERVER_PORT -> server.port
	if err := k.Load(env.Provider("OD_", ".", func(s string) string {
		return strings.Replace(
			strings.ToLower(strings.TrimPrefix(s, "OD_")),
			"_", ".", -1,
		)
	}), nil); err != nil {
		return nil, err
	}

	// 4. Handle top-level convenience env vars
	if v := os.Getenv("DATABASE_URL"); v != "" {
		k.Set("database.url", v)
	}
	if v := os.Getenv("CONTROLLER_URL"); v != "" {
		k.Set("controller.grpc_endpoint", v)
	}
	if v := os.Getenv("CONTROLLER_TOKEN"); v != "" {
		k.Set("controller.auth_token", v)
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, err
	}

	// Set node ID from hostname if not configured
	if cfg.Node.ID == "" {
		hostname, _ := os.Hostname()
		cfg.Node.ID = hostname
	}
	if cfg.Node.Name == "" {
		cfg.Node.Name = cfg.Node.ID
	}

	// Set engine download dirs if not configured
	if cfg.Engines.Aria2.DownloadDir == "" {
		cfg.Engines.Aria2.DownloadDir = cfg.Node.DownloadDir + "/aria2"
	}
	if cfg.Engines.YtDlp.DownloadDir == "" {
		cfg.Engines.YtDlp.DownloadDir = cfg.Node.DownloadDir + "/ytdlp"
	}

	return &cfg, nil
}
