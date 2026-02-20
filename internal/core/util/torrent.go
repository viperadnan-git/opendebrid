package util

import (
	"crypto/sha1"
	"fmt"
	"net/url"
)

// TorrentInfoHash extracts the SHA1 info hash from raw .torrent bytes.
// The info hash is the SHA1 of the bencoded "info" dictionary value.
func TorrentInfoHash(data []byte) (string, error) {
	if len(data) == 0 || data[0] != 'd' {
		return "", fmt.Errorf("not a bencoded dict")
	}

	pos := 1 // skip 'd'
	for pos < len(data) && data[pos] != 'e' {
		key, next, err := BdecodeString(data, pos)
		if err != nil {
			return "", err
		}
		pos = next

		valueStart := pos
		next, err = BdecodeSkip(data, pos)
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

// TorrentToMagnet parses raw .torrent bytes and returns a magnet URI.
// Extracts info hash, display name, and announce tracker.
func TorrentToMagnet(data []byte) (string, error) {
	if len(data) == 0 || data[0] != 'd' {
		return "", fmt.Errorf("not a bencoded dict")
	}

	var infoHash, name, announce string

	pos := 1 // skip 'd'
	for pos < len(data) && data[pos] != 'e' {
		key, next, err := BdecodeString(data, pos)
		if err != nil {
			return "", err
		}
		pos = next

		valueStart := pos
		next, err = BdecodeSkip(data, pos)
		if err != nil {
			return "", err
		}

		switch key {
		case "info":
			h := sha1.Sum(data[valueStart:next])
			infoHash = fmt.Sprintf("%x", h)
			// Walk into info dict to find "name"
			name = extractInfoName(data[valueStart:next])
		case "announce":
			announce, _, _ = BdecodeString(data, valueStart)
		}
		pos = next
	}

	if infoHash == "" {
		return "", fmt.Errorf("info key not found in torrent")
	}

	magnet := "magnet:?xt=urn:btih:" + infoHash
	if name != "" {
		magnet += "&dn=" + url.QueryEscape(name)
	}
	if announce != "" {
		magnet += "&tr=" + url.QueryEscape(announce)
	}

	return magnet, nil
}

// extractInfoName walks the bencoded info dict to find the "name" string.
func extractInfoName(infoDict []byte) string {
	if len(infoDict) == 0 || infoDict[0] != 'd' {
		return ""
	}
	pos := 1
	for pos < len(infoDict) && infoDict[pos] != 'e' {
		key, next, err := BdecodeString(infoDict, pos)
		if err != nil {
			return ""
		}
		pos = next

		if key == "name" {
			val, _, err := BdecodeString(infoDict, pos)
			if err != nil {
				return ""
			}
			return val
		}

		next, err = BdecodeSkip(infoDict, pos)
		if err != nil {
			return ""
		}
		pos = next
	}
	return ""
}

// BdecodeString reads a bencode string (<length>:<data>) at pos.
func BdecodeString(data []byte, pos int) (string, int, error) {
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

// BdecodeSkip advances past a bencode value at pos.
func BdecodeSkip(data []byte, pos int) (int, error) {
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
			next, err := BdecodeSkip(data, pos)
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
		_, next, err := BdecodeString(data, pos)
		return next, err
	}
}
