package web

import (
	"github.com/darron/fbhook/service"
	"net/http"
	"strings"
)

func (s *Server) adminAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/"), "/")
	parts := strings.Split(path, "/")
	ctx := r.Context()
	if r.Method == "GET" {
		cursor := r.URL.Query().Get("cursor")
		if len(cursor) > 4096 {
			respond(w, 400, map[string]string{"error": "cursor too long"})
			return
		}
		switch path {
		case "endpoints":
			items, next, e := s.service.ListEndpoints(ctx, cursor)
			if e != nil {
				failure(w, e)
				return
			}
			respond(w, 200, map[string]any{"items": items, "cursor": next})
		case "destinations":
			items, next, e := s.service.ListDestinations(ctx, cursor)
			if e != nil {
				failure(w, e)
				return
			}
			respond(w, 200, map[string]any{"items": items, "cursor": next})
		case "submissions":
			items, next, e := s.service.ListSubmissions(ctx, cursor)
			if e != nil {
				failure(w, e)
				return
			}
			respond(w, 200, map[string]any{"items": viewSubmissions(items), "cursor": next})
		case "status":
			respond(w, 200, s.service.Status())
		case "ip-blocklist":
			config, version, e := s.service.GetIPBlocklist(ctx)
			if e != nil {
				failure(w, e)
				return
			}
			respond(w, 200, map[string]any{"config": config, "version": version})
		default:
			if len(parts) == 3 && parts[0] == "submissions" {
				sub, _, e := s.service.GetSubmission(ctx, parts[1], parts[2])
				if e != nil {
					failure(w, e)
					return
				}
				respond(w, 200, viewSubmission(sub))
				return
			}
			http.NotFound(w, r)
		}
		return
	}
	if r.Method != "POST" {
		w.Header().Set("Allow", "GET, POST")
		respond(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	switch {
	case path == "ip-blocklist":
		var in struct {
			Rules   []service.IPRule `json:"rules"`
			Version string           `json:"version"`
		}
		if !decodeLimit(w, r, &in, 128*1024) {
			return
		}
		if in.Rules == nil {
			respond(w, 400, map[string]string{"error": "rules are required"})
			return
		}
		user := requestIdentity(r)
		actor := user.Login + " (" + user.ID + ")"
		config, version, e := s.service.SaveIPBlocklist(ctx, in.Rules, in.Version, actor)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, map[string]any{"config": config, "version": version})
	case path == "destinations":
		var in service.DestinationInput
		if !decode(w, r, &in) {
			return
		}
		d, e := s.service.SaveDestination(ctx, in)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, d)
	case path == "endpoints":
		var in struct {
			Name          string `json:"name"`
			DestinationID string `json:"destination_id"`
		}
		if !decode(w, r, &in) {
			return
		}
		ep, secret, e := s.service.CreateEndpoint(ctx, in.Name, in.DestinationID, nil)
		if e != nil {
			failure(w, e)
			return
		}
		ep.SecretDigest = ""
		respond(w, 201, map[string]any{"endpoint": ep, "url": strings.TrimRight(s.options.BaseURL, "/") + "/api/v1/feedback/" + secret})
	case len(parts) == 3 && parts[0] == "endpoints" && parts[2] == "rotate":
		var in struct{}
		if !decode(w, r, &in) {
			return
		}
		secret, e := s.service.RotateEndpoint(ctx, parts[1])
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, map[string]string{"url": strings.TrimRight(s.options.BaseURL, "/") + "/api/v1/feedback/" + secret})
	case len(parts) == 2 && parts[0] == "endpoints":
		var in struct {
			Name          string `json:"name"`
			DestinationID string `json:"destination_id"`
			Disabled      bool   `json:"disabled"`
		}
		if !decode(w, r, &in) {
			return
		}
		if e := s.service.UpdateEndpoint(ctx, parts[1], in.Name, in.DestinationID, in.Disabled); e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, map[string]bool{"updated": true})
	case len(parts) == 3 && parts[0] == "credentials" && parts[2] == "status":
		var in struct {
			Disabled bool `json:"disabled"`
		}
		if !decode(w, r, &in) {
			return
		}
		if e := s.service.SetCredentialDisabled(ctx, parts[1], in.Disabled); e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, map[string]bool{"updated": true})
	case len(parts) == 4 && parts[0] == "submissions" && parts[3] == "resolve":
		var in service.Resolution
		if !decode(w, r, &in) {
			return
		}
		in.Actor = requestIdentity(r).ID
		if e := s.service.Resolve(ctx, parts[1], parts[2], in); e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, map[string]bool{"updated": true})
	default:
		http.NotFound(w, r)
	}
}
