package config

import (
	"github.com/knadh/koanf/v2"
)

func loadDefaults(k *koanf.Koanf) error {
	defaults := map[string]any{
		"server.host":      "0.0.0.0",
		"server.port":      8080,
		"server.grpc_port": 9090,

		"database.max_connections": 25,

		"auth.jwt_expiry":     "24h",
		"auth.admin_username": "admin",

		"node.file_server_port": 8081,
		"node.download_dir":    "/data/downloads",

		"engines.aria2.enabled":        true,
		"engines.aria2.rpc_url":        "http://localhost:6800/jsonrpc",
		"engines.aria2.rpc_secret":     "",
		"engines.aria2.max_concurrent": 5,

		"engines.ytdlp.enabled":        true,
		"engines.ytdlp.binary":         "yt-dlp",
		"engines.ytdlp.max_concurrent": 3,
		"engines.ytdlp.default_format": "bestvideo+bestaudio/best",

		"storage.provider": "local",

		"scheduler.adapter": "round-robin",

		"limits.min_disk_free":       "1GB",
		"limits.per_user_concurrent": 5,
		"limits.per_node_concurrent": 10,
		"limits.link_expiry":         "60m",
		"limits.lru_enabled":         false,

		"logging.level":  "info",
		"logging.format": "pretty",
	}

	for key, val := range defaults {
		k.Set(key, val)
	}
	return nil
}
