// Package api implements the JSON HTTP surface: the read-only client API and
// the management API. Both authenticate with an X-App-Key header and operate on
// the single environment that key is bound to.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"confezy/internal/db"
	"confezy/internal/httpx"
	"confezy/internal/model"
)

// Client serves the read-only endpoints under /v1.
type Client struct {
	DB *db.DB
}

// Register mounts the client endpoints, each wrapped in the read-scope
// middleware.
func (c *Client) Register(mux *http.ServeMux, requireRead func(http.Handler) http.Handler) {
	handle := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, requireRead(h))
	}
	handle("GET /v1/snapshot", c.snapshot)
	handle("GET /v1/flags", c.listFlags)
	handle("GET /v1/flags/{key}", c.getFlag)
	handle("GET /v1/configs", c.listConfigs)
	handle("GET /v1/configs/{key}", c.getConfig)
	handle("GET /v1/tags", c.listTags)
}

// SnapshotBody is the /v1/snapshot response shape.
type SnapshotBody struct {
	Flags   map[string]bool            `json:"flags"`
	Configs map[string]json.RawMessage `json:"configs"`
}

// BuildSnapshot assembles the snapshot for one environment, optionally narrowed
// to a single tag. The admin UI calls this too, so its snapshot panel shows
// exactly what a client would receive rather than a second rendering that could
// drift.
func BuildSnapshot(ctx context.Context, database *db.DB, envID int64, tag string) (SnapshotBody, error) {
	filter := db.ListFilter{Tag: tag}

	flags, err := database.ListFlagsTagged(ctx, envID, filter)
	if err != nil {
		return SnapshotBody{}, err
	}
	configs, err := database.ListConfigsTagged(ctx, envID, filter)
	if err != nil {
		return SnapshotBody{}, err
	}

	// Both maps are non-nil so they marshal to {} rather than null.
	body := SnapshotBody{
		Flags:   make(map[string]bool, len(flags)),
		Configs: make(map[string]json.RawMessage, len(configs)),
	}
	for _, f := range flags {
		body.Flags[f.Key] = f.Enabled
	}
	for _, cfg := range configs {
		body.Configs[cfg.Key] = json.RawMessage(cfg.Value)
	}
	return body, nil
}

// requestTag reads and validates the ?tag= filter. The value ends up inside the
// ETag header, which is the other reason the character set is restricted.
func requestTag(w http.ResponseWriter, r *http.Request) (string, bool) {
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))
	if tag == "" {
		return "", true
	}
	if !model.ValidTag(tag) {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidRequest, model.ErrInvalidTag.Error())
		return "", false
	}
	return tag, true
}

func (c *Client) snapshot(w http.ResponseWriter, r *http.Request) {
	ac, ok := requireAuth(w, r)
	if !ok {
		return
	}
	tag, ok := requestTag(w, r)
	if !ok {
		return
	}
	if writeETag(w, r, ac.Environment.UpdatedAt, tag) {
		return
	}

	body, err := BuildSnapshot(r.Context(), c.DB, ac.Environment.ID, tag)
	if err != nil {
		internalError(w, "build snapshot", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (c *Client) listFlags(w http.ResponseWriter, r *http.Request) {
	ac, ok := requireAuth(w, r)
	if !ok {
		return
	}
	tag, ok := requestTag(w, r)
	if !ok {
		return
	}
	if writeETag(w, r, ac.Environment.UpdatedAt, tag) {
		return
	}

	flags, err := c.DB.ListFlagsTagged(r.Context(), ac.Environment.ID, db.ListFilter{Tag: tag})
	if err != nil {
		internalError(w, "list flags", err)
		return
	}

	out := make(map[string]bool, len(flags))
	for _, f := range flags {
		out[f.Key] = f.Enabled
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"flags": out})
}

func (c *Client) getFlag(w http.ResponseWriter, r *http.Request) {
	ac, ok := requireAuth(w, r)
	if !ok {
		return
	}
	if writeETag(w, r, ac.Environment.UpdatedAt, "") {
		return
	}

	flag, err := c.DB.GetFlag(r.Context(), ac.Environment.ID, r.PathValue("key"))
	if errors.Is(err, db.ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "flag not found")
		return
	}
	if err != nil {
		internalError(w, "get flag", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, flagResponse(flag))
}

func (c *Client) listConfigs(w http.ResponseWriter, r *http.Request) {
	ac, ok := requireAuth(w, r)
	if !ok {
		return
	}
	tag, ok := requestTag(w, r)
	if !ok {
		return
	}
	if writeETag(w, r, ac.Environment.UpdatedAt, tag) {
		return
	}

	configs, err := c.DB.ListConfigsTagged(r.Context(), ac.Environment.ID, db.ListFilter{Tag: tag})
	if err != nil {
		internalError(w, "list configs", err)
		return
	}

	out := make(map[string]json.RawMessage, len(configs))
	for _, cfg := range configs {
		out[cfg.Key] = json.RawMessage(cfg.Value)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"configs": out})
}

func (c *Client) getConfig(w http.ResponseWriter, r *http.Request) {
	ac, ok := requireAuth(w, r)
	if !ok {
		return
	}
	if writeETag(w, r, ac.Environment.UpdatedAt, "") {
		return
	}

	cfg, err := c.DB.GetConfig(r.Context(), ac.Environment.ID, r.PathValue("key"))
	if errors.Is(err, db.ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "config not found")
		return
	}
	if err != nil {
		internalError(w, "get config", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, configResponse(cfg))
}

// listTags lets a client discover which tags it can filter by.
func (c *Client) listTags(w http.ResponseWriter, r *http.Request) {
	ac, ok := requireAuth(w, r)
	if !ok {
		return
	}
	if writeETag(w, r, ac.Environment.UpdatedAt, "") {
		return
	}

	tags, err := c.DB.ListTagsForEnvironment(r.Context(), ac.Environment.ID)
	if err != nil {
		internalError(w, "list tags", err)
		return
	}

	names := make([]string, 0, len(tags))
	for _, t := range tags {
		names = append(names, t.Name)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"tags": names})
}

func internalError(w http.ResponseWriter, what string, err error) {
	log.Printf("api: %s: %v", what, err)
	httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, "internal error")
}
