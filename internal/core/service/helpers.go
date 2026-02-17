package service

import (
	"encoding/hex"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

func strToUUID(s string) pgtype.UUID {
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

func uuidToStr(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	const hex = "0123456789abcdef"
	buf := make([]byte, 36)
	pos := 0
	for i := 0; i < 16; i++ {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			buf[pos] = '-'
			pos++
		}
		buf[pos] = hex[b[i]>>4]
		buf[pos+1] = hex[b[i]&0x0f]
		pos += 2
	}
	return string(buf)
}
