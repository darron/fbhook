package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/darron/fbhook/storage"
	"golang.org/x/oauth2"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type identityKey struct{}
type identity struct {
	ID, Login, CSRF string
	Expires         int64
}
type oauthState struct {
	State, Verifier string
	Expires         int64
}
type bindings struct {
	Schema int               `json:"schema"`
	Users  map[string]string `json:"users"`
}

func (s *Server) cookie(name, value string, maxAge int) *http.Cookie {
	// #nosec G124 -- Secure is disabled only for explicitly validated loopback HTTP development.
	return &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}
func (s *Server) sessionCookie(user identity) (*http.Cookie, error) {
	user.Expires = time.Now().Add(8 * time.Hour).Unix()
	encoded, e := s.codec.Encode("fbhook-session", user)
	if e != nil {
		return nil, e
	}
	return s.cookie("fbhook-session", encoded, 8*60*60), nil
}
func (s *Server) current(r *http.Request) (identity, bool) {
	var user identity
	c, e := r.Cookie("fbhook-session")
	if e != nil || s.codec.Decode("fbhook-session", c.Value, &user) != nil || user.Expires < time.Now().Unix() || user.ID == "" || user.CSRF == "" {
		return user, false
	}
	return user, s.authorized(r.Context(), user.ID)
}
func (s *Server) readBindings(ctx context.Context) (bindings, string, error) {
	o, e := s.service.Store().Get(ctx, "config/admin-identities.json")
	if errors.Is(e, storage.ErrNotFound) {
		return bindings{Schema: 1, Users: map[string]string{}}, "", nil
	}
	if e != nil {
		return bindings{}, "", e
	}
	var b bindings
	if json.Unmarshal(o.Body, &b) != nil || b.Schema != 1 || b.Users == nil {
		return b, "", errors.New("invalid identity bindings")
	}
	return b, o.Version, nil
}
func (s *Server) bind(ctx context.Context, login, id string) error {
	login = strings.ToLower(login)
	allowed := false
	for _, name := range s.allowed {
		if name == login {
			allowed = true
		}
	}
	if !allowed || id == "" {
		return errors.New("account not authorized")
	}
	for attempt := 0; attempt < 3; attempt++ {
		b, v, e := s.readBindings(ctx)
		if e != nil {
			return e
		}
		if existing := b.Users[login]; existing != "" {
			if existing != id {
				return errors.New("GitHub identity binding conflict")
			}
			return nil
		}
		b.Users[login] = id
		body, _ := json.Marshal(b)
		if v == "" {
			_, e = s.service.Store().Create(ctx, "config/admin-identities.json", body)
		} else {
			_, e = s.service.Store().Replace(ctx, "config/admin-identities.json", body, v)
		}
		if errors.Is(e, storage.ErrConflict) {
			continue
		}
		return e
	}
	return errors.New("identity binding conflict")
}
func (s *Server) authorized(ctx context.Context, id string) bool {
	b, _, e := s.readBindings(ctx)
	if e != nil {
		return false
	}
	for _, name := range s.allowed {
		if b.Users[name] == id {
			return true
		}
	}
	return false
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	state := oauthState{State: rand.Text(), Verifier: oauth2.GenerateVerifier(), Expires: time.Now().Add(10 * time.Minute).Unix()}
	value, e := s.codec.Encode("fbhook-oauth", state)
	if e != nil {
		http.Error(w, "login unavailable", http.StatusServiceUnavailable)
		return
	}
	http.SetCookie(w, s.cookie("fbhook-oauth", value, 600))
	http.Redirect(w, r, s.oauth.AuthCodeURL(state.State, oauth2.S256ChallengeOption(state.Verifier)), http.StatusFound)
}
func (s *Server) githubIdentity(ctx context.Context, token, path string) (string, string, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(s.options.GitHubAPI, "/")+path, nil)
	if e != nil {
		return "", "", errors.New("identity request unavailable")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	res, e := s.http.Do(req)
	if e != nil {
		return "", "", errors.New("identity request failed")
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 200 {
		return "", "", errors.New("identity rejected")
	}
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 8192)).Decode(&user) != nil || user.ID <= 0 || user.Login == "" {
		return "", "", errors.New("invalid identity response")
	}
	return strconv.FormatInt(user.ID, 10), user.Login, nil
}
func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, s.cookie("fbhook-oauth", "", -1))
	var state oauthState
	c, e := r.Cookie("fbhook-oauth")
	if e != nil || s.codec.Decode("fbhook-oauth", c.Value, &state) != nil || state.Expires < time.Now().Unix() || state.State == "" || subtle.ConstantTimeCompare([]byte(state.State), []byte(r.URL.Query().Get("state"))) != 1 || r.URL.Query().Get("code") == "" {
		http.Error(w, "invalid or expired login state", http.StatusBadRequest)
		return
	}
	ctx := context.WithValue(r.Context(), oauth2.HTTPClient, s.http)
	token, e := s.oauth.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(state.Verifier))
	if e != nil {
		http.Error(w, "GitHub login failed", http.StatusUnauthorized)
		return
	}
	id, login, e := s.githubIdentity(ctx, token.AccessToken, "/user")
	if e != nil {
		http.Error(w, "GitHub identity unavailable", http.StatusUnauthorized)
		return
	}
	if !s.authorized(ctx, id) {
		// Bind only after looking up the configured name using the authenticated API.
		bound := false
		for _, name := range s.allowed {
			if strings.EqualFold(name, login) {
				resolved, _, err := s.githubIdentity(ctx, token.AccessToken, "/users/"+name)
				if err == nil && resolved == id && s.bind(ctx, name, id) == nil {
					bound = true
				}
				break
			}
		}
		if !bound {
			http.Error(w, "GitHub account is not authorized or its binding conflicts", http.StatusForbidden)
			return
		}
	}
	cookie, e := s.sessionCookie(identity{ID: id, Login: login, CSRF: rand.Text()})
	if e != nil {
		http.Error(w, "session unavailable", http.StatusServiceUnavailable)
		return
	}
	http.SetCookie(w, cookie)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, s.cookie("fbhook-session", "", -1))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func requestIdentity(r *http.Request) identity {
	user, _ := r.Context().Value(identityKey{}).(identity)
	return user
}
