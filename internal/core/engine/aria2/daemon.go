package aria2

import (
	"context"
	"strings"
	"time"

	"github.com/opendebrid/opendebrid/internal/core/process"
)

// Daemon implements process.Daemon for aria2c.
type Daemon struct {
	downloadDir string
	rpcPort     string
	client      *Client
	trackers    []string
}

func NewDaemon(downloadDir, rpcPort string, client *Client, trackers []string) *Daemon {
	return &Daemon{
		downloadDir: downloadDir,
		rpcPort:     rpcPort,
		client:      client,
		trackers:    trackers,
	}
}

func (d *Daemon) Name() string { return "aria2c" }

func (d *Daemon) Command() (string, []string) {
	args := []string{
		"--enable-rpc",
		"--rpc-listen-all=true",
		"--rpc-listen-port=" + d.rpcPort,
		"--rpc-max-request-size=1024M",
		"--dir=" + d.downloadDir,
		"--quiet=true",
		"--allow-overwrite=true",
		"--file-allocation=none",
		// Torrent settings
		"--follow-torrent=mem",
		"--enable-dht=true",
		"--enable-peer-exchange=true",
		"--bt-enable-lpd=true",
		"--bt-max-peers=0",
		"--seed-time=0.01",
		"--max-overall-upload-limit=1K",
		"--listen-port=6881-6999",
		"--dht-listen-port=6881-6999",
		// Download optimization
		"--max-connection-per-server=10",
		"--split=10",
		"--min-split-size=10M",
		"--max-overall-download-limit=0",
	}
	if len(d.trackers) > 0 {
		args = append(args, "--bt-tracker="+strings.Join(d.trackers, ","))
	}
	return "aria2c", args
}

func (d *Daemon) ReadyCheck() process.ReadyProbe {
	return process.ReadyProbe{
		Check: func(ctx context.Context) bool {
			_, err := d.client.GetVersion(ctx)
			return err == nil
		},
		Interval: 200 * time.Millisecond,
		Timeout:  5 * time.Second,
	}
}

func (d *Daemon) Healthy(ctx context.Context) bool {
	_, err := d.client.GetVersion(ctx)
	return err == nil
}
