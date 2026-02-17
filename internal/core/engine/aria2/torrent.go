package aria2

import (
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	"net/http"
	"time"
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

	return torrentInfoHash(data)
}

// torrentInfoHash extracts the SHA1 info hash from raw .torrent bytes.
// The info hash is the SHA1 of the bencoded "info" dictionary value.
func torrentInfoHash(data []byte) (string, error) {
	if len(data) == 0 || data[0] != 'd' {
		return "", fmt.Errorf("not a bencoded dict")
	}

	pos := 1 // skip 'd'
	for pos < len(data) && data[pos] != 'e' {
		key, next, err := bdecodeString(data, pos)
		if err != nil {
			return "", err
		}
		pos = next

		valueStart := pos
		next, err = bdecodeSkip(data, pos)
		if err != nil {
			return "", err
		}

		if key == "info" {
			h := sha1.Sum(data[valueStart:next])
			return fmt.Sprintf("%x", h), nil
		}
		pos = next
	}

	return "", fmt.Errorf("info key not found")
}

// bdecodeString reads a bencode string (<length>:<data>) at pos.
func bdecodeString(data []byte, pos int) (string, int, error) {
	end := pos
	for end < len(data) && data[end] != ':' {
		if data[end] < '0' || data[end] > '9' {
			return "", 0, fmt.Errorf("invalid string length at %d", pos)
		}
		end++
	}
	if end >= len(data) {
		return "", 0, fmt.Errorf("unterminated string at %d", pos)
	}

	length := 0
	for i := pos; i < end; i++ {
		length = length*10 + int(data[i]-'0')
	}

	start := end + 1
	if start+length > len(data) {
		return "", 0, fmt.Errorf("string overflow at %d", pos)
	}

	return string(data[start : start+length]), start + length, nil
}

// bdecodeSkip advances past a bencode value at pos.
func bdecodeSkip(data []byte, pos int) (int, error) {
	if pos >= len(data) {
		return 0, fmt.Errorf("unexpected end at %d", pos)
	}

	switch data[pos] {
	case 'i': // integer: i<digits>e
		pos++
		for pos < len(data) && data[pos] != 'e' {
			pos++
		}
		if pos >= len(data) {
			return 0, fmt.Errorf("unterminated integer")
		}
		return pos + 1, nil

	case 'l', 'd': // list or dict
		pos++
		for pos < len(data) && data[pos] != 'e' {
			next, err := bdecodeSkip(data, pos)
			if err != nil {
				return 0, err
			}
			pos = next
		}
		if pos >= len(data) {
			return 0, fmt.Errorf("unterminated list/dict")
		}
		return pos + 1, nil

	default: // string
		_, next, err := bdecodeString(data, pos)
		return next, err
	}
}
