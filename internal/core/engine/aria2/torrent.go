package aria2

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/viperadnan-git/opendebrid/internal/core/util"
)

const maxTorrentSize = 10 << 20 // 10 MB

// fetchTorrentInfoHash downloads a URL and tries to parse the response as a
// .torrent file. Returns the hex-encoded info hash on success.
func fetchTorrentInfoHash(ctx context.Context, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return "", err
	}

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxTorrentSize))
	if err != nil {
		return "", err
	}

	return util.TorrentInfoHash(data)
}
