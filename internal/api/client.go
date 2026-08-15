// Package api implements the JSON HTTP surface: the read-only client API and
// the management API. Both authenticate with an X-App-Key header and operate on
// the single environment that key is bound to.
package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"confezy/internal/auth"
	"confezy/internal/db"
	"confezy/internal/httpx"
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
}

type snapshotResponse struct {
	Flags   map[string]bool            `json:"flags"`
	Configs map[string]json.RawMessage `json:"configs"`
}

func (c *Client) snapshot(w http.ResponseWriter, r *http.Request) {
	ac, ok := auth.APIFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeUnauthorized, "not authenticated")
		return
	}
	if writeETag(w, r, ac.Environment.UpdatedAt) {
		return
	}

	flags, err := c.DB.ListFlags(r.Context(), ac.Environment.ID)
	if err != nil {
		internalError(w, "list flags", err)
		return
	}
	configs, err := c.DB.ListConfigs(r.Context(), ac.Environment.ID)
	if err != nil {
		internalError(w, "list configs", err)
		return
	}

	resp := snapshotResponse{
		Flags:   make(map[string]bool, len(flags)),
		Configs: make(map[string]json.RawMessage, len(configs)),
	}
	for _, f := range flags {
		resp.Flags[f.Key] = f.Enabled
	}
	for _, cfg := range configs {
		resp.Configs[cfg.Key] = json.RawMessage(cfg.Value)
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (c *Client) listFlags(w http.ResponseWriter, r *http.Request) {
	ac, ok := auth.APIFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeUnauthorized, "not authenticated")
		return
	}
	if writeETag(w, r, ac.Environment.UpdatedAt) {
		return
	}

	flags, err := c.DB.ListFlags(r.Context(), ac.Environment.ID)
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
	ac, ok := auth.APIFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeUnauthorized, "not authenticated")
		return
	}
	if writeETag(w, r, ac.Environment.UpdatedAt) {
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
	ac, ok := auth.APIFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeUnauthorized, "not authenticated")
		return
	}
	if writeETag(w, r, ac.Environment.UpdatedAt) {
		return
	}

	configs, err := c.DB.ListConfigs(r.Context(), ac.Environment.ID)
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
	ac, ok := auth.APIFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeUnauthorized, "not authenticated")
		return
	}
	if writeETag(w, r, ac.Environment.UpdatedAt) {
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

func internalError(w http.ResponseWriter, what string, err error) {
	log.Printf("api: %s: %v", what, err)
	httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, "internal error")
}
