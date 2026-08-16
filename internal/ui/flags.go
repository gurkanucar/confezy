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
	Tags    []string
	Updated string
	Error   string
	// Filter is carried on the row so the per-row tag forms can preserve the
	// current search when they re-render the panel.
	Filter FilterState
}

// FlagsView backs flags.html and the "flags_panel" fragment.
type FlagsView struct {
	Layout
	Rows    []FlagRow
	AllTags []string
	Filter  FilterState
	Error   string
}

func (s *Server) flagsView(r *http.Request, scope envScope, filter FilterState, errMsg string) (FlagsView, error) {
	flags, err := s.db.ListFlagsTagged(r.Context(), scope.Env.ID, filter.toDB())
	if err != nil {
		return FlagsView{}, err
	}

	rows := make([]FlagRow, 0, len(flags))
	for _, f := range flags {
		rows = append(rows, FlagRow{
			Project: scope.Project.Slug,
			Env:     scope.Env.Slug,
			Flag:    f.Flag,
			Tags:    f.Tags,
			Updated: formatTime(f.UpdatedAt),
			Filter:  filter,
		})
	}

	tags, err := s.tagNames(r, scope)
	if err != nil {
		return FlagsView{}, err
	}

	project, env := scope.Project, scope.Env
	return FlagsView{
		Layout:  s.layoutFor(r, "Flags · "+project.Name, &project, &env, scope.Envs, "flags"),
		Rows:    rows,
		AllTags: tags,
		Filter:  filter,
		Error:   errMsg,
	}, nil
}

func (s *Server) flagsPage(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveEnv(w, r)
	if !ok {
		return
	}
	view, err := s.flagsView(r, scope, readFilter(r), "")
	if err != nil {
		internalError(w, "list flags", err)
		return
	}
	s.renderListing(w, r, "flags.html", "flags_panel", view)
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
		s.renderFlagRow(w, r, scope, flag, http.StatusConflict, staleMessage)
	case err != nil:
		internalError(w, "toggle flag", err)
	default:
		s.renderFlagRow(w, r, scope, flag, http.StatusOK, "")
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

// addFlagTag and removeFlagTag re-render the whole panel, because a tag change
// can move a row in or out of the active filter.
func (s *Server) addFlagTag(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveEnv(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	// The new tag is "name"; "tag" is the filter parameter.
	tag := strings.ToLower(strings.TrimSpace(r.FormValue("name")))
	var errMsg string
	if !model.ValidTag(tag) {
		errMsg = "Invalid tag: " + model.ErrInvalidTag.Error()
	} else if err := s.db.AttachFlagTag(r.Context(), scope.Env.ID, r.PathValue("key"), tag); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			errMsg = "Flag not found."
		} else {
			internalError(w, "attach flag tag", err)
			return
		}
	}

	status := http.StatusOK
	if errMsg != "" {
		status = http.StatusUnprocessableEntity
	}
	s.renderFlagsPanel(w, r, scope, errMsg, status)
}

func (s *Server) removeFlagTag(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveEnv(w, r)
	if !ok {
		return
	}
	err := s.db.DetachFlagTag(r.Context(), scope.Env.ID, r.PathValue("key"), r.PathValue("tag"))
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		internalError(w, "detach flag tag", err)
		return
	}
	s.renderFlagsPanel(w, r, scope, "", http.StatusOK)
}

func (s *Server) renderFlagRow(w http.ResponseWriter, r *http.Request, scope envScope, flag model.Flag, status int, errMsg string) {
	tags, err := s.db.ListFlagsTagged(r.Context(), scope.Env.ID, db.ListFilter{})
	if err != nil {
		internalError(w, "list flags", err)
		return
	}
	var attached []string
	for _, f := range tags {
		if f.ID == flag.ID {
			attached = f.Tags
			break
		}
	}

	s.renderFragment(w, status, "flag_row", FlagRow{
		Project: scope.Project.Slug,
		Env:     scope.Env.Slug,
		Flag:    flag,
		Tags:    attached,
		Updated: formatTime(flag.UpdatedAt),
		Error:   errMsg,
		Filter:  readFilter(r),
	})
}

func (s *Server) renderFlagsPanel(w http.ResponseWriter, r *http.Request, scope envScope, errMsg string, status int) {
	// Reload the environment so the panel reflects the write that just happened.
	if env, err := s.db.GetEnvironmentByID(r.Context(), scope.Env.ID); err == nil {
		scope.Env = env
	}
	view, err := s.flagsView(r, scope, readFilter(r), errMsg)
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
