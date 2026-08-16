package ui

import (
	"net/http"
	"net/url"
	"strings"

	"confezy/internal/db"
	"confezy/internal/model"
)

// FilterState is the search box and tag chip selection currently in effect.
// It travels with every fragment so a mutation re-renders the same view the
// user was looking at rather than resetting to the unfiltered list.
type FilterState struct {
	Tag   string
	Query string
}

// readFilter pulls the filter out of the request. htmx sends it as form values
// on POST/PUT and as query parameters on GET and DELETE, and FormValue covers
// both. An invalid tag is dropped rather than rejected: it only ever narrows a
// listing, so silently ignoring it is friendlier than an error page.
func readFilter(r *http.Request) FilterState {
	f := FilterState{
		Tag:   strings.TrimSpace(r.FormValue("tag")),
		Query: strings.TrimSpace(r.FormValue("q")),
	}
	if f.Tag != "" && !model.ValidTag(f.Tag) {
		f.Tag = ""
	}
	if len(f.Query) > 64 {
		f.Query = f.Query[:64]
	}
	return f
}

// toDB converts the UI filter into the storage-layer filter.
func (f FilterState) toDB() db.ListFilter {
	return db.ListFilter{Tag: f.Tag, Query: f.Query}
}

// Active reports whether anything is being filtered, so the template can offer
// a "clear" control only when there is something to clear.
func (f FilterState) Active() bool { return f.Tag != "" || f.Query != "" }

// QueryString renders the filter for use in a URL, including the leading "?".
// Empty when nothing is filtered.
func (f FilterState) QueryString() string {
	values := url.Values{}
	if f.Tag != "" {
		values.Set("tag", f.Tag)
	}
	if f.Query != "" {
		values.Set("q", f.Query)
	}
	if len(values) == 0 {
		return ""
	}
	return "?" + values.Encode()
}

// IsTag reports whether name is the currently selected tag chip.
func (f FilterState) IsTag(name string) bool { return f.Tag == name }

// WithTag renders the query string for selecting a tag chip while keeping the
// current search text. Built here rather than in the template so the values go
// through proper URL encoding.
func (f FilterState) WithTag(name string) string {
	return FilterState{Tag: name, Query: f.Query}.QueryString()
}

// WithoutTag renders the query string for the "All" chip: search text kept,
// tag selection cleared.
func (f FilterState) WithoutTag() string {
	return FilterState{Query: f.Query}.QueryString()
}

// tagNames lists the tag names defined on the scope's project, for the chip bar.
func (s *Server) tagNames(r *http.Request, scope envScope) ([]string, error) {
	tags, err := s.db.ListTags(r.Context(), scope.Project.ID)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(tags))
	for _, t := range tags {
		names = append(names, t.Name)
	}
	return names, nil
}

// The Base/Target pairs below are what the shared "filter_bar" and "tag_cell"
// fragments need: which URL their controls act on, and which element the
// response replaces. Methods rather than struct fields so they cannot fall out
// of sync with the routes.

// Base is the flags collection URL for this environment.
func (v FlagsView) Base() string {
	return "/ui/p/" + v.Project.Slug + "/" + v.Env.Slug + "/flags"
}

// Target is the element the flags panel replaces.
func (v FlagsView) Target() string { return "#flags-panel" }

// Base is the URL of this single flag.
func (r FlagRow) Base() string {
	return "/ui/p/" + r.Project + "/" + r.Env + "/flags/" + r.Flag.Key
}

// Target is the element a per-row tag change replaces: the whole panel, since
// tagging can move a row in or out of the active filter.
func (r FlagRow) Target() string { return "#flags-panel" }

// Base is the configs collection URL for this environment.
func (v ConfigsView) Base() string {
	return "/ui/p/" + v.Project.Slug + "/" + v.Env.Slug + "/configs"
}

// Target is the element the configs panel replaces.
func (v ConfigsView) Target() string { return "#configs-panel" }

// Base is the URL of this single config.
func (c ConfigCard) Base() string {
	return "/ui/p/" + c.Project + "/" + c.Env + "/configs/" + c.Config.Key
}

// Target is the element a per-card tag change replaces.
func (c ConfigCard) Target() string { return "#configs-panel" }
