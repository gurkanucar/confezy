package ui

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"confezy/internal/db"
	"confezy/internal/model"
	"confezy/internal/webhook"
)

// WebhookRow is one row of the webhook table.
type WebhookRow struct {
	Project string
	Env     string
	Hook    model.Webhook
	// HeaderList is the header map flattened and sorted, so the table renders
	// in a stable order instead of Go's randomised map order.
	HeaderList []string
	Created    string
	LastFired  string
}

// WebhooksView backs webhooks.html and the "webhooks_panel" fragment.
type WebhooksView struct {
	Layout
	Rows    []WebhookRow
	Methods []string
	Error   string
	Notice  string
}

func (s *Server) webhooksView(r *http.Request, scope envScope, errMsg, notice string) (WebhooksView, error) {
	hooks, err := s.db.ListWebhooks(r.Context(), scope.Env.ID)
	if err != nil {
		return WebhooksView{}, err
	}

	rows := make([]WebhookRow, 0, len(hooks))
	for _, h := range hooks {
		names := make([]string, 0, len(h.Headers))
		for name := range h.Headers {
			names = append(names, name)
		}
		sort.Strings(names)

		list := make([]string, 0, len(names))
		for _, name := range names {
			list = append(list, name+": "+h.Headers[name])
		}

		row := WebhookRow{
			Project:    scope.Project.Slug,
			Env:        scope.Env.Slug,
			Hook:       h,
			HeaderList: list,
			Created:    formatTime(h.CreatedAt),
		}
		if h.LastFiredAt != nil {
			row.LastFired = formatTime(*h.LastFiredAt)
		}
		rows = append(rows, row)
	}

	project, env := scope.Project, scope.Env
	return WebhooksView{
		Layout:  s.layoutFor(r, "Webhooks · "+project.Name, &project, &env, scope.Envs, "webhooks"),
		Rows:    rows,
		Methods: model.WebhookMethods,
		Error:   errMsg,
		Notice:  notice,
	}, nil
}

func (s *Server) webhooksPage(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveEnv(w, r)
	if !ok {
		return
	}
	view, err := s.webhooksView(r, scope, "", "")
	if err != nil {
		internalError(w, "list webhooks", err)
		return
	}
	s.renderPage(w, http.StatusOK, "webhooks.html", view)
}

func (s *Server) createWebhook(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.resolveEnv(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	target := strings.TrimSpace(r.FormValue("url"))
	method := strings.ToUpper(strings.TrimSpace(r.FormValue("method")))
	label := strings.TrimSpace(r.FormValue("label"))

	if method == "" {
		method = "PATCH"
	}

	var errMsg string
	switch {
	case webhook.ValidateURL(target) != nil:
		errMsg = webhook.ErrInvalidURL.Error()
	case !model.ValidWebhookMethod(method):
		errMsg = "Method must be one of " + strings.Join(model.WebhookMethods, ", ") + "."
	default:
		headers, err := parseHeaderLines(r.FormValue("headers"))
		if err != nil {
			errMsg = err.Error()
			break
		}
		if _, err := s.db.CreateWebhook(r.Context(), scope.Env.ID, target, method, headers, label); err != nil {
			internalError(w, "create webhook", err)
			return
		}
	}

	status := http.StatusOK
	if errMsg != "" {
		status = http.StatusUnprocessableEntity
	}
	s.renderWebhooksPanel(w, r, scope, errMsg, "", status)
}

func (s *Server) toggleWebhook(w http.ResponseWriter, r *http.Request) {
	scope, id, ok := s.resolveWebhook(w, r)
	if !ok {
		return
	}

	hook, err := s.db.GetWebhook(r.Context(), id, scope.Env.ID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "webhook not found", http.StatusNotFound)
			return
		}
		internalError(w, "get webhook", err)
		return
	}

	if err := s.db.SetWebhookEnabled(r.Context(), id, scope.Env.ID, !hook.Enabled); err != nil {
		internalError(w, "toggle webhook", err)
		return
	}
	s.renderWebhooksPanel(w, r, scope, "", "", http.StatusOK)
}

func (s *Server) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	scope, id, ok := s.resolveWebhook(w, r)
	if !ok {
		return
	}
	if err := s.db.DeleteWebhook(r.Context(), id, scope.Env.ID); err != nil && !errors.Is(err, db.ErrNotFound) {
		internalError(w, "delete webhook", err)
		return
	}
	s.renderWebhooksPanel(w, r, scope, "", "", http.StatusOK)
}

// testWebhook fires one delivery immediately, through the same code path the
// automatic deliveries use, so a green result here means the real thing works.
func (s *Server) testWebhook(w http.ResponseWriter, r *http.Request) {
	scope, id, ok := s.resolveWebhook(w, r)
	if !ok {
		return
	}

	hook, err := s.db.GetWebhook(r.Context(), id, scope.Env.ID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "webhook not found", http.StatusNotFound)
			return
		}
		internalError(w, "get webhook", err)
		return
	}

	var errMsg, notice string
	if s.webhooks == nil {
		errMsg = "Webhook delivery is not running."
	} else if err := s.webhooks.Deliver(r.Context(), hook); err != nil {
		errMsg = "Delivery failed: " + err.Error()
	} else {
		notice = "Delivered to " + hook.URL
	}

	status := http.StatusOK
	if errMsg != "" {
		status = http.StatusUnprocessableEntity
	}
	s.renderWebhooksPanel(w, r, scope, errMsg, notice, status)
}

func (s *Server) resolveWebhook(w http.ResponseWriter, r *http.Request) (envScope, int64, bool) {
	scope, ok := s.resolveEnv(w, r)
	if !ok {
		return envScope{}, 0, false
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid webhook id", http.StatusBadRequest)
		return envScope{}, 0, false
	}
	return scope, id, true
}

func (s *Server) renderWebhooksPanel(w http.ResponseWriter, r *http.Request, scope envScope, errMsg, notice string, status int) {
	view, err := s.webhooksView(r, scope, errMsg, notice)
	if err != nil {
		internalError(w, "list webhooks", err)
		return
	}
	s.renderFragment(w, status, "webhooks_panel", view)
}

// parseHeaderLines turns a textarea of "Name: value" lines into a header map.
// Blank lines and # comments are ignored so a pasted block stays usable.
func parseHeaderLines(raw string) (map[string]string, error) {
	headers := map[string]string{}

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		name, value, found := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if !found || name == "" {
			return nil, errors.New("each header line must look like 'Name: value' — could not read " + strconv.Quote(line))
		}
		// A newline in either half would let one field forge extra headers.
		if strings.ContainsAny(name, "\r\n") || strings.ContainsAny(value, "\r\n") {
			return nil, errors.New("header names and values cannot contain line breaks")
		}
		headers[name] = value
	}
	return headers, nil
}
