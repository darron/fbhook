// Package web serves the private Go-routed administration UI and feedback API.
package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/darron/fbhook"
	"github.com/darron/fbhook/service"
	"github.com/gorilla/securecookie"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Options struct {
	BaseURL, OAuthClientID, OAuthClientSecret string
	AllowedUsers                              []string
	SessionKey                                []byte
	HTTPClient                                *http.Client
	OAuthEndpoint                             oauth2.Endpoint
	GitHubAPI                                 string
}
type Server struct {
	service      *service.Service
	options      Options
	allowed      []string
	codec        *securecookie.SecureCookie
	oauth        oauth2.Config
	http         *http.Client
	secure       bool
	mux          *http.ServeMux
	limitMu      sync.Mutex
	window       time.Time
	global       int
	counts       map[string]int
	intakeSlots  chan struct{}
	readyMu      sync.Mutex
	readyChecked time.Time
	readyError   error
}

func New(s *service.Service, o Options) (*Server, error) {
	u, e := url.Parse(o.BaseURL)
	if e != nil {
		return nil, errors.New("invalid base URL")
	}
	loopback := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	schemeOK := u.Scheme == "https" || u.Scheme == "http" && loopback
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || !schemeOK {
		return nil, errors.New("base URL must be HTTPS (HTTP is allowed only on loopback)")
	}
	if s == nil || o.OAuthClientID == "" || o.OAuthClientSecret == "" || len(o.SessionKey) != 64 || len(o.AllowedUsers) == 0 {
		return nil, errors.New("service, OAuth credentials, authorized users and 64-byte session key required")
	}
	allowed := []string{}
	for _, login := range o.AllowedUsers {
		login = strings.ToLower(strings.TrimSpace(login))
		if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}$`).MatchString(login) {
			return nil, errors.New("invalid authorized GitHub username")
		}
		allowed = append(allowed, login)
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	hc := *o.HTTPClient
	hc.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	if o.OAuthEndpoint.AuthURL == "" {
		o.OAuthEndpoint = github.Endpoint
	}
	if o.GitHubAPI == "" {
		o.GitHubAPI = "https://api.github.com"
	}
	codec := securecookie.New(o.SessionKey[:32], o.SessionKey[32:])
	codec.MaxAge(8 * 60 * 60)
	codec.MaxLength(4096)
	app := &Server{service: s, options: o, allowed: allowed, codec: codec, http: &hc, secure: u.Scheme == "https", mux: http.NewServeMux(), counts: map[string]int{}, intakeSlots: make(chan struct{}, 16)}
	app.oauth = oauth2.Config{ClientID: o.OAuthClientID, ClientSecret: o.OAuthClientSecret, RedirectURL: strings.TrimRight(o.BaseURL, "/") + "/auth/callback", Scopes: []string{"read:user"}, Endpoint: o.OAuthEndpoint}
	app.routes()
	return app, nil
}
func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { respond(w, 200, map[string]string{"status": "alive"}) })
	s.mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if s.ready(r.Context()) != nil {
			respond(w, 503, map[string]string{"status": "unavailable"})
			return
		}
		respond(w, 200, map[string]string{"status": "ready"})
	})
	s.mux.HandleFunc("POST /api/v1/feedback/{credential}", s.intake)
	s.mux.HandleFunc("GET /auth/login", s.login)
	s.mux.HandleFunc("GET /auth/callback", s.callback)
	s.mux.Handle("POST /auth/logout", s.admin(http.HandlerFunc(s.logout)))
	s.mux.Handle("/api/v1/admin/", s.admin(http.HandlerFunc(s.adminAPI)))
	s.mux.Handle("GET /assets/", s.admin(http.StripPrefix("/assets/", http.FileServerFS(frontendAssets()))))
	for _, path := range []string{"GET /admin", "GET /admin/endpoints", "GET /admin/destinations", "GET /admin/submissions"} {
		s.mux.Handle(path, s.admin(http.HandlerFunc(s.shell)))
	}
	s.mux.HandleFunc("GET /login.css", loginCSS)
	s.mux.HandleFunc("GET /{$}", s.landing)
}

func (s *Server) ready(ctx context.Context) error {
	s.readyMu.Lock()
	defer s.readyMu.Unlock()
	if !s.readyChecked.IsZero() && time.Since(s.readyChecked) < 5*time.Second {
		return s.readyError
	}
	// A caller disconnect must not poison the shared result with its cancellation.
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	s.readyError = s.service.Ready(probeCtx)
	s.readyChecked = time.Now()
	return s.readyError
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	// Handlers never log RequestURI, URL credentials, payloads or upstream bodies.
	s.mux.ServeHTTP(w, r)
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func failure(w http.ResponseWriter, e error) {
	message := "operation unavailable"
	var p *service.Error
	if errors.As(e, &p) {
		message = p.Message
	}
	respond(w, service.ErrorStatus(e), map[string]string{"error": message})
}
func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		respond(w, 415, map[string]string{"error": "application/json required"})
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, fbhook.MaxRequestBytes))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		respond(w, 400, map[string]string{"error": "invalid JSON request"})
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		respond(w, 400, map[string]string{"error": "expected one JSON object"})
		return false
	}
	return true
}
func (s *Server) intake(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		respond(w, 415, map[string]string{"error": "application/json required"})
		return
	}
	credential := r.PathValue("credential")
	id, secret, ok := strings.Cut(credential, ".")
	if !ok || !service.ValidID(id) || len(secret) != 43 {
		respond(w, 404, map[string]string{"error": "feedback endpoint unavailable"})
		return
	}
	// Bound unauthenticated work without letting bad credentials spend a valid
	// endpoint's minute-long quota. No forwarded IP headers are trusted here.
	select {
	case s.intakeSlots <- struct{}{}:
		defer func() { <-s.intakeSlots }()
	default:
		w.Header().Set("Retry-After", "1")
		respond(w, 429, map[string]string{"error": "feedback intake busy"})
		return
	}
	body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, fbhook.MaxRequestBytes))
	if e != nil {
		respond(w, 413, map[string]string{"error": "feedback request exceeds 64 KiB"})
		return
	}
	receipt, e := s.service.AcceptWithAdmission(r.Context(), credential, body, r.Header.Get("Idempotency-Key"), s.admit)
	if e != nil {
		if service.ErrorStatus(e) == 429 {
			w.Header().Set("Retry-After", "60")
		}
		failure(w, e)
		return
	}
	respond(w, 202, receipt)
}
func (s *Server) admit(id string) bool {
	s.limitMu.Lock()
	defer s.limitMu.Unlock()
	now := time.Now()
	if now.Sub(s.window) >= time.Minute {
		s.window = now
		s.global = 0
		s.counts = map[string]int{}
	}
	if s.global >= 600 || s.counts[id] >= 120 {
		return false
	}
	s.global++
	s.counts[id]++
	return true
}
func (s *Server) admin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := s.current(r)
		if !ok {
			respond(w, 401, map[string]string{"error": "authorized GitHub login required"})
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			csrf := r.Header.Get("X-CSRF-Token")
			if r.URL.Path == "/auth/logout" {
				if e := r.ParseForm(); e == nil {
					csrf = r.Form.Get("csrf")
				}
			}
			if csrf == "" || subtle.ConstantTimeCompare([]byte(csrf), []byte(user.CSRF)) != 1 {
				respond(w, 403, map[string]string{"error": "CSRF token required"})
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, user)))
	})
}
