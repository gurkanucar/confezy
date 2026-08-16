package ui

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"confezy/internal/db"
	"confezy/internal/model"
)

// staleMessage is shown when someone else changed the record first.
const staleMessage = "This page is out of date — reload it."

// FlagRow backs the "flag_row" fragment.
type FlagRow struct {
	Project string
	Env     string
	Flag    model.Flag
	Updated string
	Error   string
}

// FlagsView backs flags.html and the "flags_panel" fragment.
type FlagsView struct {
	Layout
	Rows  []FlagRow
	Error string
}

func (s *Server) flagsView(r *http.Request, scope envScope, errMsg string) (FlagsView, error) {
	flags, err := s.db.ListFlags(r.Context(), scope.Env.ID)
	if err != nil {
		return FlagsView{}, err
	}

	rows := make([]FlagRow, 0, len(flags))
	for _, f := range flags {
		rows = append(rows, FlagRow{
			Project: scope.Project.Slug,
			Env:     scope.Env.Slug,
			Flag:    f,
			Updated: formatTime(f.UpdatedAt),
		})
	}

	project, env := scope.Project, scope.Env
	return FlagsView{
		Layout: s.layoutFor(r, "Flags · "+project.Name, &project, &env, scope.Envs, "flags"),
		Rows:   rows,
		Error:  errMsg,
	}, nil
}

func (s *Server) flagsPage(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveEnv(w, r)
	if !ok {
		return
	}
	view, err := s.flagsView(r, scope, "")
	if err != nil {
		internalError(w, "list flags", err)
		return
	}
	s.renderPage(w, http.StatusOK, "flags.html", view)
}

func (s *Server) createFlag(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveEnv(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	key := strings.TrimSpace(r.FormValue("key"))
	description := strings.TrimSpace(r.FormValue("description"))
	enabled := r.FormValue("enabled") != ""

	var errMsg string
	if !model.ValidKey(key) {
		errMsg = "Invalid key: " + model.ErrInvalidKey.Error()
	} else {
		_, err := s.db.CreateFlag(r.Context(), scope.Env.ID, key, enabled, description)
		if errors.Is(err, db.ErrDuplicate) {
			errMsg = "This key already exists: " + key
		} else if err != nil {
			internalError(w, "create flag", err)
			return
		}
	}

	s.renderFlagsPanel(w, r, scope, errMsg, http.StatusOK)
}

func (s *Server) toggleFlag(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveEnv(w, r)
	if !ok {
		return
	}
	key := r.PathValue("key")

	expected, err := formVersion(r)
	if err != nil {
		http.Error(w, "expectedVersion is required", http.StatusBadRequest)
		return
	}

	current, err := s.db.GetFlag(r.Context(), scope.Env.ID, key)
	if errors.Is(err, db.ErrNotFound) {
		http.Error(w, "flag not found", http.StatusNotFound)
		return
	}
	if err != nil {
		internalError(w, "get flag", err)
		return
	}

	flag, err := s.db.UpdateFlag(r.Context(), scope.Env.ID, key, !current.Enabled, nil, expected)
	switch {
	case errors.Is(err, db.ErrNotFound):
		http.Error(w, "flag not found", http.StatusNotFound)
	case errors.Is(err, db.ErrVersionConflict):
		// Hand back the row as it actually stands, flagged as stale.
		s.renderFragment(w, http.StatusConflict, "flag_row", FlagRow{
			Project: scope.Project.Slug,
			Env:     scope.Env.Slug,
			Flag:    flag,
			Updated: formatTime(flag.UpdatedAt),
			Error:   staleMessage,
		})
	case err != nil:
		internalError(w, "toggle flag", err)
	default:
		s.renderFragment(w, http.StatusOK, "flag_row", FlagRow{
			Project: scope.Project.Slug,
			Env:     scope.Env.Slug,
			Flag:    flag,
			Updated: formatTime(flag.UpdatedAt),
		})
	}
}

func (s *Server) deleteFlag(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveEnv(w, r)
	if !ok {
		return
	}

	expected, err := formVersion(r)
	if err != nil {
		http.Error(w, "expectedVersion is required", http.StatusBadRequest)
		return
	}

	_, err = s.db.DeleteFlag(r.Context(), scope.Env.ID, r.PathValue("key"), expected)
	switch {
	case errors.Is(err, db.ErrNotFound):
		// Already gone: the refreshed panel is the right answer.
		s.renderFlagsPanel(w, r, scope, "", http.StatusOK)
	case errors.Is(err, db.ErrVersionConflict):
		s.renderFlagsPanel(w, r, scope, staleMessage, http.StatusConflict)
	case err != nil:
		internalError(w, "delete flag", err)
	default:
		s.renderFlagsPanel(w, r, scope, "", http.StatusOK)
	}
}

func (s *Server) renderFlagsPanel(w http.ResponseWriter, r *http.Request, scope envScope, errMsg string, status int) {
	// Reload the environment so the panel reflects the write that just happened.
	if env, err := s.db.GetEnvironmentByID(r.Context(), scope.Env.ID); err == nil {
		scope.Env = env
	}
	view, err := s.flagsView(r, scope, errMsg)
	if err != nil {
		internalError(w, "list flags", err)
		return
	}
	s.renderFragment(w, status, "flags_panel", view)
}

// formVersion reads expectedVersion from the query string (htmx sends DELETE
// parameters there) or the form body (PUT).
func formVersion(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.FormValue("expectedVersion"), 10, 64)
}
