// Package auth authenticates requests by API key.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

const (
	keyPrefix     = "gw_"
	keyEntropy    = 32                  // bytes of crypto/rand output
	keyEncodedLen = len(keyPrefix) + 43 // base64url(32 bytes), unpadded
)

type Status string

const (
	StatusActive  Status = "active"
	StatusRevoked Status = "revoked"
)

// APIKey is the authenticated key's metadata. It never holds the raw key.
type APIKey struct {
	ID            int64
	ApplicationID int64
	UserID        int64
	PlanID        int64
	// RequestsPerMinute is the plan's rate limit, loaded with the key so limiting needs no extra query.
	RequestsPerMinute int
	Status            Status
	Hash              []byte // SHA-256 of the raw key
}

// ErrKeyNotFound is returned by a KeyStore when no key matches the hash.
var ErrKeyNotFound = errors.New("api key not found")

// GenerateKey returns a new raw API key ("gw_" + 256 random bits, base64url) and its SHA-256 hash.
// The raw key must be shown to its owner once and never stored or logged.
func GenerateKey() (raw string, hash []byte, err error) {
	b := make([]byte, keyEntropy)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	raw = keyPrefix + base64.RawURLEncoding.EncodeToString(b)
	return raw, HashKey(raw), nil
}

// HashKey returns the SHA-256 digest stored for a raw key.
func HashKey(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// wellFormed rejects values that can't be keys without touching the database.
func wellFormed(raw string) bool {
	if len(raw) != keyEncodedLen || !strings.HasPrefix(raw, keyPrefix) {
		return false
	}
	_, err := base64.RawURLEncoding.Strict().DecodeString(raw[len(keyPrefix):])
	return err == nil
}
