package web

import (
	"context"
	"github.com/darron/fbhook/service"
	"github.com/darron/fbhook/storage/memory"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func testServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s, e := service.New(memory.New(), service.Options{EncryptionKey: []byte("01234567890123456789012345678901")})
	if e != nil {
		t.Fatal(e)
	}
	app, e := New(s, Options{BaseURL: "http://127.0.0.1:8080", OAuthClientID: "id", OAuthClientSecret: "secret", AllowedUsers: []string{"darron"}, SessionKey: []byte(strings.Repeat("k", 64))})
	if e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return app, srv
}
func TestAnonymousBoundaryAndCSRFFailClosed(t *testing.T) {
	app, srv := testServer(t)
	for _, path := range []string{"/api/v1/admin/endpoints", "/api/v1/admin/submissions", "/admin", "/assets/missing.js"} {
		res, e := testGet(t, srv, path)
		if e != nil {
			t.Fatal(e)
		}
		_ = res.Body.Close()
		if res.StatusCode != 401 && res.StatusCode != 404 {
			t.Errorf("anonymous %s: %d", path, res.StatusCode)
		}
	}
	ctx := context.Background()
	if e := app.bind(ctx, "darron", "123"); e != nil {
		t.Fatal(e)
	}
	cookie, e := app.sessionCookie(identity{ID: "123", Login: "darron", CSRF: "csrf"})
	if e != nil {
		t.Fatal(e)
	}
	req, _ := http.NewRequestWithContext(t.Context(), "POST", srv.URL+"/api/v1/admin/endpoints", strings.NewReader(`{}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	res, e := srv.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 403 {
		t.Fatalf("missing CSRF accepted %d", res.StatusCode)
	}
}
func TestStableGitHubIdentityBindingAndRemoval(t *testing.T) {
	app, _ := testServer(t)
	ctx := context.Background()
	if e := app.bind(ctx, "darron", "123"); e != nil {
		t.Fatal(e)
	}
	if e := app.bind(ctx, "darron", "999"); e == nil {
		t.Fatal("reassigned username rebound")
	}
	if !app.authorized(ctx, "123") {
		t.Fatal("bound ID denied")
	}
	app.allowed = nil
	if app.authorized(ctx, "123") {
		t.Fatal("removed user remained authorized")
	}
}
func TestOAuthStateRequired(t *testing.T) {
	_, srv := testServer(t)
	res, e := testGet(t, srv, "/auth/callback?code=secret&state=madeup")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 400 {
		t.Fatal(res.StatusCode)
	}
}
func TestShellEscapesBootstrap(t *testing.T) {
	app, srv := testServer(t)
	if e := app.bind(context.Background(), "darron", "123"); e != nil {
		t.Fatal(e)
	}
	cookie, _ := app.sessionCookie(identity{ID: "123", Login: "</script><script>alert(1)</script>", CSRF: "csrf"})
	req, _ := http.NewRequestWithContext(t.Context(), "GET", srv.URL+"/admin", nil)
	req.AddCookie(cookie)
	res, e := srv.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	if strings.Contains(string(body), "</script><script>alert(1)") {
		t.Fatal("bootstrap escaped script element")
	}
	if res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
}

func testGet(t *testing.T, srv *httptest.Server, path string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
	if err != nil {
		return nil, err
	}
	return srv.Client().Do(req)
}

func TestOAuthCallbackBindsIdentityWithPKCEAndLogsOut(t *testing.T) {
	tokenCalls := 0
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("code_verifier") == "" || r.Form.Get("code") != "fixture-code" {
				t.Error("missing PKCE or authorization code")
			}
			tokenCalls++
			_, _ = io.WriteString(w, `{"access_token":"test-only","token_type":"bearer"}`)
		case "/user", "/users/darron":
			if r.Header.Get("Authorization") != "Bearer test-only" {
				t.Error("missing authenticated GitHub lookup")
			}
			_, _ = io.WriteString(w, `{"id":123,"login":"darron"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer github.Close()
	app, srv := testServer(t)
	app.options.GitHubAPI = github.URL
	app.oauth.Endpoint.AuthURL = github.URL + "/authorize"
	app.oauth.Endpoint.TokenURL = github.URL + "/token"
	client := *srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	request, _ := http.NewRequestWithContext(t.Context(), "GET", srv.URL+"/auth/login", nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	authURL, err := url.Parse(response.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if authURL.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("missing PKCE challenge")
	}
	request, _ = http.NewRequestWithContext(t.Context(), "GET", srv.URL+"/auth/callback?code=fixture-code&state="+url.QueryEscape(authURL.Query().Get("state")), nil)
	for _, c := range response.Cookies() {
		request.AddCookie(c)
	}
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || tokenCalls != 1 || !app.authorized(t.Context(), "123") {
		t.Fatalf("callback failed: %d token=%d", response.StatusCode, tokenCalls)
	}
	var session *http.Cookie
	for _, c := range response.Cookies() {
		if c.Name == "fbhook-session" {
			session = c
		}
	}
	if session == nil || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode {
		t.Fatal("missing safe session")
	}
	var user identity
	if err = app.codec.Decode(session.Name, session.Value, &user); err != nil {
		t.Fatal(err)
	}
	request, _ = http.NewRequestWithContext(t.Context(), "POST", srv.URL+"/auth/logout", strings.NewReader(url.Values{"csrf": {user.CSRF}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(session)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatal("logout failed")
	}
	for _, c := range response.Cookies() {
		if c.Name == session.Name && c.MaxAge < 0 {
			return
		}
	}
	t.Fatal("logout did not clear session")
}
