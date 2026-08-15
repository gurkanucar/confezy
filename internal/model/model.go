// Package model holds the domain structs and the shared validation rules that
// both the JSON APIs and the admin UI enforce.
package model

import (
	"errors"
	"regexp"
)

// Scope values for API keys, ordered from least to most privileged.
const (
	ScopeRead  = "read"
	ScopeWrite = "write"
	ScopeAdmin = "admin"
)

var scopeRank = map[string]int{
	ScopeRead:  1,
	ScopeWrite: 2,
	ScopeAdmin: 3,
}

// ValidScope reports whether s is one of the three known scopes.
func ValidScope(s string) bool {
	_, ok := scopeRank[s]
	return ok
}

// ScopeAllows reports whether a key carrying have is permitted to perform an
// action that requires want.
func ScopeAllows(have, want string) bool {
	h, ok := scopeRank[have]
	if !ok {
		return false
	}
	w, ok := scopeRank[want]
	if !ok {
		return false
	}
	return h >= w
}

var (
	keyRe  = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
)

// ErrInvalidKey and friends are returned by the validators below so callers can
// map them onto a 400 response.
var (
	ErrInvalidKey      = errors.New("key must match ^[a-z0-9_]{1,64}$")
	ErrInvalidSlug     = errors.New("slug must match ^[a-z0-9][a-z0-9_-]{0,62}$")
	ErrInvalidUsername = errors.New("username must be 3-64 characters")
	ErrInvalidPassword = errors.New("password must be at least 8 characters")
)

// ValidKey reports whether s is a legal flag or config key.
func ValidKey(s string) bool { return keyRe.MatchString(s) }

// ValidSlug reports whether s is a legal project or environment slug.
func ValidSlug(s string) bool { return slugRe.MatchString(s) }

// MinPasswordLen is the shortest admin password accepted.
const MinPasswordLen = 8

// ValidUsername reports whether s is an acceptable admin username.
func ValidUsername(s string) bool { return len(s) >= 3 && len(s) <= 64 }

// ValidPassword reports whether s is long enough to be accepted.
func ValidPassword(s string) bool { return len(s) >= MinPasswordLen }

// User is an admin panel account.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	CreatedAt    int64
}

// Session is a logged-in admin browser session.
type Session struct {
	ID        string
	UserID    int64
	ExpiresAt int64
}

// Project groups a set of environments.
type Project struct {
	ID        int64
	Slug      string
	Name      string
	CreatedAt int64
}

// Environment owns flags, configs and API keys. UpdatedAt is bumped on every
// write below it and is the sole input to the client API ETag.
type Environment struct {
	ID        int64
	ProjectID int64
	Slug      string
	UpdatedAt int64
	CreatedAt int64
}

// Flag is a boolean feature flag.
type Flag struct {
	ID            int64
	EnvironmentID int64
	Key           string
	Enabled       bool
	Description   string
	Version       int64
	UpdatedAt     int64
}

// Config is an arbitrary JSON document stored as a string. Value is always
// valid JSON: it is checked before every write.
type Config struct {
	ID            int64
	EnvironmentID int64
	Key           string
	Value         string
	Description   string
	Version       int64
	UpdatedAt     int64
}

// APIKey is the stored half of an issued key. The plaintext key is shown once
// at creation time and never persisted.
type APIKey struct {
	ID            int64
	EnvironmentID int64
	KeyHash       string
	KeyPrefix     string
	Scope         string
	Label         string
	CreatedAt     int64
	RevokedAt     *int64
}

// Active reports whether the key has not been revoked.
func (k APIKey) Active() bool { return k.RevokedAt == nil }
