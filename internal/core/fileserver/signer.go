package fileserver

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Signer generates and validates signed download tokens.
type Signer struct {
	secret []byte
}

func NewSigner(secret string) *Signer {
	return &Signer{secret: []byte(secret)}
}

// Sign creates a token encoding job_id, file_path, user_id, and expiry.
func (s *Signer) Sign(jobID, filePath, userID string, expiry time.Time) string {
	payload := fmt.Sprintf("%s|%s|%s|%d", jobID, filePath, userID, expiry.Unix())
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(payload))
	sig := base64.URLEncoding.EncodeToString(mac.Sum(nil))
	return base64.URLEncoding.EncodeToString([]byte(payload)) + "." + sig
}

// Verify validates a token and returns the decoded fields.
func (s *Signer) Verify(token string) (jobID, filePath, userID string, err error) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", "", "", fmt.Errorf("invalid token format")
	}

	payloadBytes, err := base64.URLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", "", "", fmt.Errorf("invalid token encoding")
	}

	// Verify signature
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(payloadBytes)
	expectedSig := base64.URLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[1]), []byte(expectedSig)) {
		return "", "", "", fmt.Errorf("invalid signature")
	}

	// Parse payload
	fields := strings.SplitN(string(payloadBytes), "|", 4)
	if len(fields) != 4 {
		return "", "", "", fmt.Errorf("invalid payload")
	}

	// Check expiry
	expiryUnix, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid expiry")
	}
	if time.Now().Unix() > expiryUnix {
		return "", "", "", fmt.Errorf("token expired")
	}

	return fields[0], fields[1], fields[2], nil
}
