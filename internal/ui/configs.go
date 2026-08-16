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
	Pretty  string
	Updated string
	Error   string
}

// ConfigsView backs configs.html and the "configs_panel" fragment.
type ConfigsView struct {
	Layout
	Cards []ConfigCard
	Error string
}

func (s *Server) configsView(r *http.Request, scope envScope, errMsg string) (ConfigsView, error) {
	configs, err := s.db.ListConfigs(r.Context(), scope.Env.ID)
	if err != nil {
		return ConfigsView{}, err
	}

	cards := make([]ConfigCard, 0, len(configs))
	for _, c := range configs {
		cards = append(cards, ConfigCard{
			Project: scope.Project.Slug,
			Env:     scope.Env.Slug,
			Config:  c,
			Pretty:  prettyJSON(c.Value),
			Updated: formatTime(c.UpdatedAt),
		})
	}

	project, env := scope.Project, scope.Env
	return ConfigsView{
		Layout: s.layoutFor(r, "Configs · "+project.Name, &project, &env, scope.Envs, "configs"),
		Cards:  cards,
		Error:  errMsg,
	}, nil
}

func (s *Server) configsPage(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveEnv(w, r)
	if !ok {
		return
	}
	view, err := s.configsView(r, scope, "")
	if err != nil {
		internalError(w, "list configs", err)
		return
	}
	s.renderPage(w, http.StatusOK, "configs.html", view)
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
			Pretty:  pretty,
			Updated: formatTime(c.UpdatedAt),
			Error:   errMsg,
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
	view, err := s.configsView(r, scope, errMsg)
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
