package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"confezy/internal/auth"
	"confezy/internal/db"
	"confezy/internal/httpx"
	"confezy/internal/model"
)

// maxBodyBytes caps request bodies; config documents live in here.
const maxBodyBytes = 1 << 20 // 1 MiB

// Manage serves the write endpoints under /v1/manage.
type Manage struct {
	DB *db.DB
}

// Register mounts the management endpoints behind the write-scope middleware.
func (m *Manage) Register(mux *http.ServeMux, requireWrite func(http.Handler) http.Handler) {
	handle := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, requireWrite(h))
	}
	handle("POST /v1/manage/flags", m.createFlag)
	handle("PUT /v1/manage/flags/{key}", m.updateFlag)
	handle("DELETE /v1/manage/flags/{key}", m.deleteFlag)
	handle("POST /v1/manage/configs", m.createConfig)
	handle("PUT /v1/manage/configs/{key}", m.updateConfig)
	handle("DELETE /v1/manage/configs/{key}", m.deleteConfig)

	// Tags. Attaching is metadata, not a value change, so it does not move the
	// record's version — but it does move the environment stamp, because it
	// changes what a ?tag= request returns.
	handle("POST /v1/manage/flags/{key}/tags", m.attachFlagTag)
	handle("DELETE /v1/manage/flags/{key}/tags/{tag}", m.detachFlagTag)
	handle("POST /v1/manage/configs/{key}/tags", m.attachConfigTag)
	handle("DELETE /v1/manage/configs/{key}/tags/{tag}", m.detachConfigTag)
	handle("DELETE /v1/manage/tags/{tag}", m.deleteTag)
}

type tagRequest struct {
	Tag string `json:"tag"`
}

// attachTagHandler is shared by the flag and config endpoints, which differ
// only in which db method they call.
func (m *Manage) attachTagHandler(w http.ResponseWriter, r *http.Request,
	attach func(ctx context.Context, envID int64, key, tag string) error, what string) {

	ac, ok := requireAuth(w, r)
	if !ok {
		return
	}
	var req tagRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	tag := strings.TrimSpace(req.Tag)
	if !model.ValidTag(tag) {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidRequest, model.ErrInvalidTag.Error())
		return
	}

	err := attach(r.Context(), ac.Environment.ID, r.PathValue("key"), tag)
	switch {
	case errors.Is(err, db.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, what+" not found")
	case err != nil:
		internalError(w, "attach tag", err)
	default:
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"key": r.PathValue("key"), "tag": tag})
	}
}

func (m *Manage) detachTagHandler(w http.ResponseWriter, r *http.Request,
	detach func(ctx context.Context, envID int64, key, tag string) error, what string) {

	ac, ok := requireAuth(w, r)
	if !ok {
		return
	}
	tag := r.PathValue("tag")
	if !model.ValidTag(tag) {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidRequest, model.ErrInvalidTag.Error())
		return
	}

	err := detach(r.Context(), ac.Environment.ID, r.PathValue("key"), tag)
	switch {
	case errors.Is(err, db.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, what+" or tag not found")
	case err != nil:
		internalError(w, "detach tag", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (m *Manage) attachFlagTag(w http.ResponseWriter, r *http.Request) {
	m.attachTagHandler(w, r, m.DB.AttachFlagTag, "flag")
}

func (m *Manage) detachFlagTag(w http.ResponseWriter, r *http.Request) {
	m.detachTagHandler(w, r, m.DB.DetachFlagTag, "flag")
}

func (m *Manage) attachConfigTag(w http.ResponseWriter, r *http.Request) {
	m.attachTagHandler(w, r, m.DB.AttachConfigTag, "config")
}

func (m *Manage) detachConfigTag(w http.ResponseWriter, r *http.Request) {
	m.detachTagHandler(w, r, m.DB.DetachConfigTag, "config")
}

// deleteTag removes a tag from the whole project, detaching it everywhere.
func (m *Manage) deleteTag(w http.ResponseWriter, r *http.Request) {
	ac, ok := requireAuth(w, r)
	if !ok {
		return
	}
	tag := r.PathValue("tag")
	if !model.ValidTag(tag) {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidRequest, model.ErrInvalidTag.Error())
		return
	}

	err := m.DB.DeleteTag(r.Context(), ac.Environment.ProjectID, tag)
	switch {
	case errors.Is(err, db.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "tag not found")
	case err != nil:
		internalError(w, "delete tag", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// Response shapes.

type flagBody struct {
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
	Version int64  `json:"version"`
}

type configBody struct {
	Key     string          `json:"key"`
	Value   json.RawMessage `json:"value"`
	Version int64           `json:"version"`
}

func flagResponse(f model.Flag) flagBody {
	return flagBody{Key: f.Key, Enabled: f.Enabled, Version: f.Version}
}

func configResponse(c model.Config) configBody {
	return configBody{Key: c.Key, Value: json.RawMessage(c.Value), Version: c.Version}
}

// Request shapes. Pointers distinguish "absent" from "zero value".

type createFlagRequest struct {
	Key         string  `json:"key"`
	Enabled     *bool   `json:"enabled"`
	Description *string `json:"description"`
}

type updateFlagRequest struct {
	Enabled         *bool   `json:"enabled"`
	Description     *string `json:"description"`
	ExpectedVersion *int64  `json:"expectedVersion"`
}

type createConfigRequest struct {
	Key         string          `json:"key"`
	Value       json.RawMessage `json:"value"`
	Description *string         `json:"description"`
}

type updateConfigRequest struct {
	Value           json.RawMessage `json:"value"`
	Description     *string         `json:"description"`
	ExpectedVersion *int64          `json:"expectedVersion"`
}

type deleteRequest struct {
	ExpectedVersion *int64 `json:"expectedVersion"`
}

// Flags.

func (m *Manage) createFlag(w http.ResponseWriter, r *http.Request) {
	ac, ok := requireAuth(w, r)
	if !ok {
		return
	}
	var req createFlagRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !model.ValidKey(req.Key) {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidRequest, model.ErrInvalidKey.Error())
		return
	}
	if req.Enabled == nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidRequest, "'enabled' is required")
		return
	}

	flag, err := m.DB.CreateFlag(r.Context(), ac.Environment.ID, req.Key, *req.Enabled, deref(req.Description))
	if errors.Is(err, db.ErrDuplicate) {
		httpx.WriteError(w, http.StatusConflict, httpx.CodeAlreadyExists,
			"a flag with this key already exists in this environment")
		return
	}
	if err != nil {
		internalError(w, "create flag", err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, flagResponse(flag))
}

func (m *Manage) updateFlag(w http.ResponseWriter, r *http.Request) {
	ac, ok := requireAuth(w, r)
	if !ok {
		return
	}
	var req updateFlagRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidRequest, "'enabled' is required")
		return
	}
	expected, ok := requireExpectedVersion(w, r, req.ExpectedVersion)
	if !ok {
		return
	}

	flag, err := m.DB.UpdateFlag(r.Context(), ac.Environment.ID, r.PathValue("key"),
		*req.Enabled, req.Description, expected)
	switch {
	case errors.Is(err, db.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "flag not found")
	case errors.Is(err, db.ErrVersionConflict):
		writeVersionConflict(w, flagResponse(flag), flag.Version, expected)
	case err != nil:
		internalError(w, "update flag", err)
	default:
		httpx.WriteJSON(w, http.StatusOK, flagResponse(flag))
	}
}

func (m *Manage) deleteFlag(w http.ResponseWriter, r *http.Request) {
	ac, ok := requireAuth(w, r)
	if !ok {
		return
	}
	expected, ok := decodeExpectedVersion(w, r)
	if !ok {
		return
	}

	flag, err := m.DB.DeleteFlag(r.Context(), ac.Environment.ID, r.PathValue("key"), expected)
	switch {
	case errors.Is(err, db.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "flag not found")
	case errors.Is(err, db.ErrVersionConflict):
		writeVersionConflict(w, flagResponse(flag), flag.Version, expected)
	case err != nil:
		internalError(w, "delete flag", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// Configs.

func (m *Manage) createConfig(w http.ResponseWriter, r *http.Request) {
	ac, ok := requireAuth(w, r)
	if !ok {
		return
	}
	var req createConfigRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !model.ValidKey(req.Key) {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidRequest, model.ErrInvalidKey.Error())
		return
	}
	value, ok := normaliseJSON(w, req.Value)
	if !ok {
		return
	}

	cfg, err := m.DB.CreateConfig(r.Context(), ac.Environment.ID, req.Key, value, deref(req.Description))
	if errors.Is(err, db.ErrDuplicate) {
		httpx.WriteError(w, http.StatusConflict, httpx.CodeAlreadyExists,
			"a config with this key already exists in this environment")
		return
	}
	if err != nil {
		internalError(w, "create config", err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, configResponse(cfg))
}

func (m *Manage) updateConfig(w http.ResponseWriter, r *http.Request) {
	ac, ok := requireAuth(w, r)
	if !ok {
		return
	}
	var req updateConfigRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	value, ok := normaliseJSON(w, req.Value)
	if !ok {
		return
	}
	expected, ok := requireExpectedVersion(w, r, req.ExpectedVersion)
	if !ok {
		return
	}

	cfg, err := m.DB.UpdateConfig(r.Context(), ac.Environment.ID, r.PathValue("key"),
		value, req.Description, expected)
	switch {
	case errors.Is(err, db.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "config not found")
	case errors.Is(err, db.ErrVersionConflict):
		writeVersionConflict(w, configResponse(cfg), cfg.Version, expected)
	case err != nil:
		internalError(w, "update config", err)
	default:
		httpx.WriteJSON(w, http.StatusOK, configResponse(cfg))
	}
}

func (m *Manage) deleteConfig(w http.ResponseWriter, r *http.Request) {
	ac, ok := requireAuth(w, r)
	if !ok {
		return
	}
	expected, ok := decodeExpectedVersion(w, r)
	if !ok {
		return
	}

	cfg, err := m.DB.DeleteConfig(r.Context(), ac.Environment.ID, r.PathValue("key"), expected)
	switch {
	case errors.Is(err, db.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "config not found")
	case errors.Is(err, db.ErrVersionConflict):
		writeVersionConflict(w, configResponse(cfg), cfg.Version, expected)
	case err != nil:
		internalError(w, "delete config", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// Shared helpers.

func requireAuth(w http.ResponseWriter, r *http.Request) (*auth.APIContext, bool) {
	ac, ok := auth.APIFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeUnauthorized, "not authenticated")
		return nil, false
	}
	return ac, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidRequest,
			"invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// normaliseJSON validates a config value and strips insignificant whitespace
// before it is stored.
func normaliseJSON(w http.ResponseWriter, raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidRequest, "'value' is required")
		return "", false
	}
	if !json.Valid(raw) {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidRequest, "'value' is not valid JSON")
		return "", false
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidRequest, "'value' is not valid JSON")
		return "", false
	}
	return buf.String(), true
}

// requireExpectedVersion enforces optimistic locking on PUT, falling back to
// the ?expectedVersion= query parameter when the field is absent from the body.
func requireExpectedVersion(w http.ResponseWriter, r *http.Request, fromBody *int64) (int64, bool) {
	if fromBody != nil {
		if *fromBody <= 0 {
			httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidRequest,
				"'expectedVersion' must be a positive integer")
			return 0, false
		}
		return *fromBody, true
	}
	if q := r.URL.Query().Get("expectedVersion"); q != "" {
		v, err := strconv.ParseInt(q, 10, 64)
		if err != nil || v <= 0 {
			httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidRequest,
				"'expectedVersion' must be a positive integer")
			return 0, false
		}
		return v, true
	}
	httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidRequest, "'expectedVersion' is required")
	return 0, false
}

// decodeExpectedVersion reads expectedVersion for DELETE, where the body may
// legitimately be empty and the query parameter is the usual carrier.
func decodeExpectedVersion(w http.ResponseWriter, r *http.Request) (int64, bool) {
	var req deleteRequest

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil && !errors.Is(err, io.EOF) {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidRequest,
			"invalid JSON body: "+err.Error())
		return 0, false
	}
	return requireExpectedVersion(w, r, req.ExpectedVersion)
}

func writeVersionConflict(w http.ResponseWriter, current any, actual, expected int64) {
	httpx.WriteErrorWithCurrent(w, http.StatusConflict, httpx.CodeVersionConflict,
		"expected version "+strconv.FormatInt(expected, 10)+
			" but the current version is "+strconv.FormatInt(actual, 10),
		current)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
