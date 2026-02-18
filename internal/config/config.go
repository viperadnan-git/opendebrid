package config

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Server     ServerConfig     `toml:"server"`
	Database   DatabaseConfig   `toml:"database"`
	Auth       AuthConfig       `toml:"auth"`
	Node       NodeConfig       `toml:"node"`
	Engines    EnginesConfig    `toml:"engines"`
	Storage    StorageConfig    `toml:"storage"`
	Scheduler  SchedulerConfig  `toml:"scheduler"`
	Limits     LimitsConfig     `toml:"limits"`
	Logging    LoggingConfig    `toml:"logging"`
	Controller ControllerConfig `toml:"controller"`
}

type ServerConfig struct {
	Host string `toml:"host"`
	Port int    `toml:"port"`
	URL  string `toml:"url"`
}

type DatabaseConfig struct {
	URL            string `toml:"url"`
	MaxConnections int    `toml:"max_connections"`
}

type AuthConfig struct {
	JWTSecret     string `toml:"jwt_secret"`
	JWTExpiry     string `toml:"jwt_expiry"`
	AdminUsername string `toml:"admin_username"`
	AdminPassword string `toml:"admin_password"`
}

type NodeConfig struct {
	ID          string `toml:"id"`
	DownloadDir string `toml:"download_dir"`
	AuthToken   string `toml:"auth_token"`
}

type EnginesConfig struct {
	Aria2 Aria2Config `toml:"aria2"`
	YtDlp YtDlpConfig `toml:"ytdlp"`
}

type Aria2Config struct {
	Enabled       bool   `toml:"enabled"`
	RPCURL        string `toml:"rpc_url"`
	RPCSecret     string `toml:"rpc_secret"`
	MaxConcurrent int    `toml:"max_concurrent"`
	DownloadDir   string `toml:"download_dir"`
}

type YtDlpConfig struct {
	Enabled       bool   `toml:"enabled"`
	Binary        string `toml:"binary"`
	MaxConcurrent int    `toml:"max_concurrent"`
	DefaultFormat string `toml:"default_format"`
	DownloadDir   string `toml:"download_dir"`
}

type StorageConfig struct {
	Provider string `toml:"provider"`
}

type SchedulerConfig struct {
	Adapter string `toml:"adapter"`
}

type LimitsConfig struct {
	MinDiskFree       string `toml:"min_disk_free"`
	PerUserConcurrent int    `toml:"per_user_concurrent"`
	PerNodeConcurrent int    `toml:"per_node_concurrent"`
	LinkExpiry        string `toml:"link_expiry"`
	LRUEnabled        bool   `toml:"lru_enabled"`
}

type LoggingConfig struct {
	Level  string `toml:"level"`
	Format string `toml:"format"`
}

type ControllerConfig struct {
	URL string `toml:"url"`
}

// Load reads config from defaults, overlays TOML file, then overlays OD_ env vars.
func Load(configPath string) (*Config, error) {
	cfg := defaults()

	if configPath == "" {
		if _, err := os.Stat("config.toml"); err == nil {
			configPath = "config.toml"
		}
	}

	if configPath != "" {
		if _, err := toml.DecodeFile(configPath, cfg); err != nil {
			return nil, err
		}
	}

	applyEnv(cfg)

	// Derived defaults
	if cfg.Server.URL == "" {
		hostname, _ := os.Hostname()
		cfg.Server.URL = fmt.Sprintf("http://%s:%d", hostname, cfg.Server.Port)
	}

	if cfg.Engines.Aria2.DownloadDir == "" {
		cfg.Engines.Aria2.DownloadDir = cfg.Node.DownloadDir + "/aria2"
	}
	if cfg.Engines.YtDlp.DownloadDir == "" {
		cfg.Engines.YtDlp.DownloadDir = cfg.Node.DownloadDir + "/ytdlp"
	}

	return cfg, nil
}

// defaults returns a Config with sensible default values.
func defaults() *Config {
	return &Config{
		Server: ServerConfig{
			Host: "0.0.0.0",
			Port: 8080,
		},
		Database: DatabaseConfig{
			MaxConnections: 25,
		},
		Auth: AuthConfig{
			JWTExpiry:     "24h",
			AdminUsername: "admin",
		},
		Node: NodeConfig{
			ID:          "main",
			DownloadDir: "/data/downloads",
		},
		Engines: EnginesConfig{
			Aria2: Aria2Config{
				Enabled:       true,
				RPCURL:        "http://localhost:6800/jsonrpc",
				MaxConcurrent: 5,
			},
			YtDlp: YtDlpConfig{
				Enabled:       true,
				Binary:        "yt-dlp",
				MaxConcurrent: 3,
				DefaultFormat: "bestvideo+bestaudio/best",
			},
		},
		Storage: StorageConfig{
			Provider: "local",
		},
		Scheduler: SchedulerConfig{
			Adapter: "round-robin",
		},
		Limits: LimitsConfig{
			MinDiskFree:       "1GB",
			PerUserConcurrent: 5,
			PerNodeConcurrent: 10,
			LinkExpiry:        "60m",
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "pretty",
		},
	}
}

// applyEnv overlays OD_ prefixed environment variables onto the config.
// Mapping: OD_SERVER_PORT -> server.port (struct tag path joined with _).
// This correctly handles TOML keys containing underscores (e.g. rpc_url)
// because the env key is built by joining tag segments with _, preserving
// underscores within tags.
func applyEnv(cfg *Config) {
	setters := make(map[string]func(string))
	buildSetters(reflect.ValueOf(cfg).Elem(), "", setters)

	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "OD_") {
			continue
		}
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 || parts[1] == "" {
			continue
		}
		key := strings.ToLower(strings.TrimPrefix(parts[0], "OD_"))
		if setter, ok := setters[key]; ok {
			setter(parts[1])
		}
	}
}

// buildSetters walks a struct recursively and builds a map of
// normalized env key -> setter function. Keys are built by joining
// toml tags with "_", so "engines.aria2.rpc_url" becomes "engines_aria2_rpc_url".
func buildSetters(v reflect.Value, prefix string, setters map[string]func(string)) {
	t := v.Type()
	for i := range t.NumField() {
		field := t.Field(i)
		tag := field.Tag.Get("toml")
		if tag == "" || tag == "-" {
			continue
		}

		key := tag
		if prefix != "" {
			key = prefix + "_" + tag
		}

		fv := v.Field(i)
		if field.Type.Kind() == reflect.Struct {
			buildSetters(fv, key, setters)
			continue
		}

		switch field.Type.Kind() {
		case reflect.String:
			setters[key] = func(val string) { fv.SetString(val) }
		case reflect.Int, reflect.Int64:
			setters[key] = func(val string) {
				if n, err := strconv.ParseInt(val, 10, 64); err == nil {
					fv.SetInt(n)
				}
			}
		case reflect.Bool:
			setters[key] = func(val string) {
				fv.SetBool(val == "true" || val == "1")
			}
		}
	}
}
