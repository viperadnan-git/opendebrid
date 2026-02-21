package util

import (
	"encoding/hex"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

// TextToUUID parses a UUID string (with or without hyphens) into pgtype.UUID.
func TextToUUID(s string) pgtype.UUID {
	var u pgtype.UUID
	s = strings.ReplaceAll(s, "-", "")
	if len(s) != 32 {
		return u
	}
	decoded, err := hex.DecodeString(s)
	if err != nil {
		return u
	}
	copy(u.Bytes[:], decoded)
	u.Valid = true
	return u
}

// ToText converts a non-null string to pgtype.Text.
// Use when passing plain string node IDs to sqlc functions that accept pgtype.Text.
func ToText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: true}
}

// UUIDToStr formats a pgtype.UUID as a standard hyphenated UUID string.
func UUIDToStr(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	const hexChars = "0123456789abcdef"
	buf := make([]byte, 36)
	pos := 0
	for i := 0; i < 16; i++ {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			buf[pos] = '-'
			pos++
		}
		buf[pos] = hexChars[b[i]>>4]
		buf[pos+1] = hexChars[b[i]&0x0f]
		pos += 2
	}
	return string(buf)
}
