package api

import (
	"net/http"
	"strconv"
	"strings"
)

// EnvETag derives the client-facing ETag from an environment's updated_at.
// Any write anywhere under the environment bumps that column, so a stable
// ETag means "nothing you can see has changed".
func EnvETag(updatedAt int64) string {
	return `"` + strconv.FormatInt(updatedAt, 10) + `"`
}

// FilteredETag is EnvETag for a narrowed response. The filter has to be part of
// the validator: two different ?tag= values produce different bodies from the
// same environment, and sharing one ETag between them would let a client cache
// the wrong list behind a 304.
func FilteredETag(updatedAt int64, tag string) string {
	if tag == "" {
		return EnvETag(updatedAt)
	}
	return `"` + strconv.FormatInt(updatedAt, 10) + "." + tag + `"`
}

// etagMatches reports whether an If-None-Match header covers etag. It accepts
// the wildcard, comma-separated lists, and weak validators.
func etagMatches(ifNoneMatch, etag string) bool {
	ifNoneMatch = strings.TrimSpace(ifNoneMatch)
	if ifNoneMatch == "" {
		return false
	}
	if ifNoneMatch == "*" {
		return true
	}
	for _, candidate := range strings.Split(ifNoneMatch, ",") {
		if strings.TrimPrefix(strings.TrimSpace(candidate), "W/") == etag {
			return true
		}
	}
	return false
}

// writeETag sets the validator headers and reports whether the handler should
// stop early with a 304. tag is the ?tag= filter in effect, "" when none.
func writeETag(w http.ResponseWriter, r *http.Request, updatedAt int64, tag string) bool {
	etag := FilteredETag(updatedAt, tag)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")

	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}
