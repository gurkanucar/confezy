package ui

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"confezy/internal/db"
	"confezy/internal/model"
)

// ConfigCard backs the "config_card" fragment.
type ConfigCard struct {
	Project string
	Env     string
	Config  model.Config
	Tags    []string
	Pretty  string
	Updated string
	Error   string
	// Filter is carried on the card so its tag forms preserve the current
	// search when they re-render the panel.
	Filter FilterState
}

// ConfigsView backs configs.html and the "configs_panel" fragment.
type ConfigsView struct {
	Layout
	Cards   []ConfigCard
	AllTags []string
	Filter  FilterState
	Error   string
}

func (s *Server) configsView(r *http.Request, scope envScope, filter FilterState, errMsg string) (ConfigsView, error) {
	configs, err := s.db.ListConfigsTagged(r.Context(), scope.Env.ID, filter.toDB())
	if err != nil {
		return ConfigsView{}, err
	}

	cards := make([]ConfigCard, 0, len(configs))
	for _, c := range configs {
		cards = append(cards, ConfigCard{
			Project: scope.Project.Slug,
			Env:     scope.Env.Slug,
			Config:  c.Config,
			Tags:    c.Tags,
			Pretty:  prettyJSON(c.Value),
			Updated: formatTime(c.UpdatedAt),
			Filter:  filter,
		})
	}

	tags, err := s.tagNames(r, scope)
	if err != nil {
		return ConfigsView{}, err
	}

	project, env := scope.Project, scope.Env
	return ConfigsView{
		Layout:  s.layoutFor(r, "Configs · "+project.Name, &project, &env, scope.Envs, "configs"),
		Cards:   cards,
		AllTags: tags,
		Filter:  filter,
		Error:   errMsg,
	}, nil
}

func (s *Server) configsPage(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveEnv(w, r)
	if !ok {
		return
	}
	view, err := s.configsView(r, scope, readFilter(r), "")
	if err != nil {
		internalError(w, "list configs", err)
		return
	}
	s.renderListing(w, r, "configs.html", "configs_panel", view)
}

func (s *Server) createConfig(w http.ResponseWriter, r *http.Request) {
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
	raw := r.FormValue("value")

	status := http.StatusOK
	var errMsg string

	switch {
	case !model.ValidKey(key):
		errMsg = "Invalid key: " + model.ErrInvalidKey.Error()
		status = http.StatusUnprocessableEntity
	case !json.Valid([]byte(raw)):
		errMsg = "Invalid JSON."
		status = http.StatusUnprocessableEntity
	default:
		_, err := s.db.CreateConfig(r.Context(), scope.Env.ID, key, compactJSON(raw), description)
		if errors.Is(err, db.ErrDuplicate) {
			errMsg = "This key already exists: " + key
			status = http.StatusUnprocessableEntity
		} else if err != nil {
			internalError(w, "create config", err)
			return
		}
	}

	s.renderConfigsPanel(w, r, scope, errMsg, status)
}

func (s *Server) updateConfig(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveEnv(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	key := r.PathValue("key")
	description := strings.TrimSpace(r.FormValue("description"))
	raw := r.FormValue("value")

	expected, err := formVersion(r)
	if err != nil {
		http.Error(w, "expectedVersion is required", http.StatusBadRequest)
		return
	}

	card := func(c model.Config, pretty, errMsg string) ConfigCard {
		return ConfigCard{
			Project: scope.Project.Slug,
			Env:     scope.Env.Slug,
			Config:  c,
			Tags:    s.configTagNames(r, scope, c.ID),
			Pretty:  pretty,
			Updated: formatTime(c.UpdatedAt),
			Error:   errMsg,
			Filter:  readFilter(r),
		}
	}

	if !json.Valid([]byte(raw)) {
		// Keep what the user typed so the fix is one edit away.
		current, err := s.db.GetConfig(r.Context(), scope.Env.ID, key)
		if err != nil {
			internalError(w, "get config", err)
			return
		}
		s.renderFragment(w, http.StatusUnprocessableEntity, "config_card",
			card(current, raw, "Invalid JSON — nothing was saved."))
		return
	}

	cfg, err := s.db.UpdateConfig(r.Context(), scope.Env.ID, key, compactJSON(raw), &description, expected)
	switch {
	case errors.Is(err, db.ErrNotFound):
		http.Error(w, "config not found", http.StatusNotFound)
	case errors.Is(err, db.ErrVersionConflict):
		s.renderFragment(w, http.StatusConflict, "config_card",
			card(cfg, prettyJSON(cfg.Value), staleMessage))
	case err != nil:
		internalError(w, "update config", err)
	default:
		s.renderFragment(w, http.StatusOK, "config_card", card(cfg, prettyJSON(cfg.Value), ""))
	}
}

func (s *Server) deleteConfig(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveEnv(w, r)
	if !ok {
		return
	}

	expected, err := formVersion(r)
	if err != nil {
		http.Error(w, "expectedVersion is required", http.StatusBadRequest)
		return
	}

	_, err = s.db.DeleteConfig(r.Context(), scope.Env.ID, r.PathValue("key"), expected)
	switch {
	case errors.Is(err, db.ErrNotFound):
		s.renderConfigsPanel(w, r, scope, "", http.StatusOK)
	case errors.Is(err, db.ErrVersionConflict):
		s.renderConfigsPanel(w, r, scope, staleMessage, http.StatusConflict)
	case err != nil:
		internalError(w, "delete config", err)
	default:
		s.renderConfigsPanel(w, r, scope, "", http.StatusOK)
	}
}

func (s *Server) renderConfigsPanel(w http.ResponseWriter, r *http.Request, scope envScope, errMsg string, status int) {
	if env, err := s.db.GetEnvironmentByID(r.Context(), scope.Env.ID); err == nil {
		scope.Env = env
	}
	view, err := s.configsView(r, scope, readFilter(r), errMsg)
	if err != nil {
		internalError(w, "list configs", err)
		return
	}
	s.renderFragment(w, status, "configs_panel", view)
}

// prettyJSON indents a stored document for the textarea editor.
func prettyJSON(value string) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(value), "", "  "); err != nil {
		return value
	}
	return buf.String()
}

// compactJSON strips insignificant whitespace before storing.
func compactJSON(value string) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(value)); err != nil {
		return value
	}
	return buf.String()
}

// configTagNames looks up the tags attached to one config. Used when a single
// card is re-rendered and the full listing is not being rebuilt.
func (s *Server) configTagNames(r *http.Request, scope envScope, configID int64) []string {
	configs, err := s.db.ListConfigsTagged(r.Context(), scope.Env.ID, db.ListFilter{})
	if err != nil {
		// The card is still worth rendering without its tags.
		return nil
	}
	for _, c := range configs {
		if c.ID == configID {
			return c.Tags
		}
	}
	return nil
}

// addConfigTag and removeConfigTag re-render the whole panel, because a tag
// change can move a card in or out of the active filter.
func (s *Server) addConfigTag(w http.ResponseWriter, r *http.Request) {
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
	} else if err := s.db.AttachConfigTag(r.Context(), scope.Env.ID, r.PathValue("key"), tag); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			errMsg = "Config not found."
		} else {
			internalError(w, "attach config tag", err)
			return
		}
	}

	status := http.StatusOK
	if errMsg != "" {
		status = http.StatusUnprocessableEntity
	}
	s.renderConfigsPanel(w, r, scope, errMsg, status)
}

func (s *Server) removeConfigTag(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveEnv(w, r)
	if !ok {
		return
	}
	err := s.db.DetachConfigTag(r.Context(), scope.Env.ID, r.PathValue("key"), r.PathValue("tag"))
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		internalError(w, "detach config tag", err)
		return
	}
	s.renderConfigsPanel(w, r, scope, "", http.StatusOK)
}
