package handlers

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

func pgUUID(s string) pgtype.UUID {
	var u pgtype.UUID
	b, err := parseUUIDBytes(s)
	if err != nil {
		return u
	}
	u.Bytes = b
	u.Valid = true
	return u
}

func pgUUIDToString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func parseUUIDBytes(s string) ([16]byte, error) {
	var b [16]byte
	s = strings.ReplaceAll(s, "-", "")
	if len(s) != 32 {
		return b, fmt.Errorf("invalid UUID length")
	}
	decoded, err := hex.DecodeString(s)
	if err != nil {
		return b, err
	}
	copy(b[:], decoded)
	return b, nil
}
