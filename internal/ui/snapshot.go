package ui

import (
	"encoding/json"
	"net/http"

	"confezy/internal/api"
)

// SnapshotView backs snapshot.html and the "snapshot_panel" fragment. It shows
// the exact document a client receives from GET /v1/snapshot, so the panel is
// the place to confirm what is actually being served.
type SnapshotView struct {
	Layout
	JSON        string
	ETag        string
	FlagCount   int
	ConfigCount int
	Error       string
}

func (s *Server) snapshotView(r *http.Request, scope envScope) (SnapshotView, error) {
	body, err := api.BuildSnapshot(r.Context(), s.db, scope.Env.ID)
	if err != nil {
		return SnapshotView{}, err
	}

	pretty, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return SnapshotView{}, err
	}

	project, env := scope.Project, scope.Env
	return SnapshotView{
		Layout:      s.layoutFor(r, "Snapshot · "+project.Name, &project, &env, scope.Envs, "snapshot"),
		JSON:        string(pretty),
		ETag:        api.EnvETag(env.UpdatedAt),
		FlagCount:   len(body.Flags),
		ConfigCount: len(body.Configs),
	}, nil
}

func (s *Server) snapshotPage(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveEnv(w, r)
	if !ok {
		return
	}
	view, err := s.snapshotView(r, scope)
	if err != nil {
		internalError(w, "build snapshot", err)
		return
	}
	s.renderPage(w, http.StatusOK, "snapshot.html", view)
}

// refreshSnapshot re-renders just the panel, so the Refresh button does not
// reload the whole page.
func (s *Server) refreshSnapshot(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveEnv(w, r)
	if !ok {
		return
	}
	view, err := s.snapshotView(r, scope)
	if err != nil {
		internalError(w, "build snapshot", err)
		return
	}
	s.renderFragment(w, http.StatusOK, "snapshot_panel", view)
}
