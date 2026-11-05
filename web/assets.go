package web

import (
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed ui/dist login.css
var assets embed.FS

func frontendAssets() fs.FS { sub, _ := fs.Sub(assets, "ui/dist"); return sub }

var shellTemplate = template.Must(template.New("shell").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light"><title>fbhook — Feedback operations</title><link rel="stylesheet" href="/assets/app.css"></head><body><div id="app"></div><script type="application/json" id="bootstrap">{{.Bootstrap}}</script><script type="module" src="/assets/app.js"></script></body></html>`))

type bootstrap struct {
	Page string `json:"page"`
	User struct {
		ID    string `json:"id"`
		Login string `json:"login"`
	} `json:"user"`
	CSRF string         `json:"csrfToken"`
	Data map[string]any `json:"data"`
}

func (s *Server) shell(w http.ResponseWriter, r *http.Request) {
	user := requestIdentity(r)
	b := bootstrap{Page: strings.TrimPrefix(r.URL.Path, "/admin"), CSRF: user.CSRF, Data: map[string]any{}}
	b.User.ID = user.ID
	b.User.Login = user.Login
	if b.Page == "" {
		b.Page = "/endpoints"
	}
	endpoints, nextEndpoints, err := s.service.ListEndpoints(r.Context(), "")
	if err != nil {
		http.Error(w, "configuration unavailable", http.StatusServiceUnavailable)
		return
	}
	destinations, nextDestinations, err := s.service.ListDestinations(r.Context(), "")
	if err != nil {
		http.Error(w, "configuration unavailable", http.StatusServiceUnavailable)
		return
	}
	b.Data["endpoints"] = endpoints
	b.Data["destinations"] = destinations
	b.Data["endpointCursor"] = nextEndpoints
	b.Data["destinationCursor"] = nextDestinations
	b.Data["status"] = s.service.Status()
	if b.Page == "/ip-blocklist" {
		config, version, err := s.service.GetIPBlocklist(r.Context())
		if err != nil {
			http.Error(w, "IP blocklist unavailable", http.StatusServiceUnavailable)
			return
		}
		b.Data["ipBlocklist"] = map[string]any{"config": config, "version": version}
	}
	if b.Page == "/submissions" {
		items, cursor, err := s.service.ListSubmissions(r.Context(), "")
		if err != nil {
			http.Error(w, "submissions unavailable", http.StatusServiceUnavailable)
			return
		}
		b.Data["submissions"] = viewSubmissions(items)
		b.Data["submissionCursor"] = cursor
	}
	data, e := json.Marshal(b)
	if e != nil {
		http.Error(w, "page unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Only json.Marshal output reaches this script context; it escapes <, > and &.
	_ = shellTemplate.Execute(w, struct{ Bootstrap template.JS }{Bootstrap: template.JS(data)}) // #nosec G203 -- encoding/json escapes HTML delimiters.
}

var landingTemplate = template.Must(template.New("login").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>fbhook</title><link rel="stylesheet" href="/login.css"></head><body><main><p class="eyebrow">FBHOOK / PRIVATE ADMINISTRATION</p><h1>A place for feedback to land.</h1><p>Connect your applications to the people and agents who improve them.</p><a href="/auth/login">Sign in with GitHub →</a></main></body></html>`))

func (s *Server) landing(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.current(r); ok {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = landingTemplate.Execute(w, nil)
}

func loginCSS(w http.ResponseWriter, _ *http.Request) {
	data, _ := assets.ReadFile("login.css")
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write(data)
}
