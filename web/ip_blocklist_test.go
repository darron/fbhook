package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/darron/fbhook/service"
	"github.com/darron/fbhook/storage"
	"github.com/darron/fbhook/storage/memory"
)

func newIPTestServer(t *testing.T, store storage.Store, options Options) (*Server, *service.Service) {
	t.Helper()
	svc, err := service.New(store, service.Options{
		EncryptionKey: []byte(strings.Repeat("i", 32)),
		Providers:     map[string]service.Provider{"github": ipTestProvider{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.BaseURL == "" {
		options.BaseURL = "http://127.0.0.1:8080"
	}
	options.OAuthClientID = "id"
	options.OAuthClientSecret = "secret"
	options.AllowedUsers = []string{"darron"}
	options.SessionKey = []byte(strings.Repeat("s", 64))
	app, err := New(svc, options)
	if err != nil {
		t.Fatal(err)
	}
	return app, svc
}

type ipTestProvider struct{}

func (ipTestProvider) Validate(context.Context, service.Destination, string) error { return nil }
func (ipTestProvider) Create(context.Context, service.Destination, string, service.Issue) (service.IssueRef, error) {
	return service.IssueRef{ID: "1", URL: "https://example.invalid/1"}, nil
}
func (ipTestProvider) Find(context.Context, service.Destination, string, service.Issue, string) (service.IssueRef, error) {
	return service.IssueRef{}, errors.New("not found")
}

func createIPTestEndpoint(t *testing.T, svc *service.Service) (string, string) {
	t.Helper()
	destination, err := svc.SaveDestination(t.Context(), service.DestinationInput{
		Name: "Fixture", Provider: "github", Target: "example/repo", Token: "token",
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, credential, err := svc.CreateEndpoint(t.Context(), "Fixture", destination.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return endpoint.ID, credential
}

func TestNewValidatesClientIPOptions(t *testing.T) {
	store := memory.New()
	bad := []Options{
		{ClientIPHeader: "Forwarded"},
		{ClientIPHeader: "Fly-Client-IP", TrustedProxyCIDRs: []string{"not-a-prefix"}},
		{ClientIPHeader: "X-Forwarded-For", TrustedProxyCIDRs: []string{"192.0.2.1/24"}},
	}
	for _, options := range bad {
		svc, err := service.New(store, service.Options{EncryptionKey: []byte(strings.Repeat("i", 32))})
		if err != nil {
			t.Fatal(err)
		}
		options.BaseURL = "http://127.0.0.1:8080"
		options.OAuthClientID = "id"
		options.OAuthClientSecret = "secret"
		options.AllowedUsers = []string{"darron"}
		options.SessionKey = []byte(strings.Repeat("s", 64))
		if _, err = New(svc, options); err == nil {
			t.Fatalf("accepted invalid options: %+v", options)
		}
	}
}

func TestClientIPTrustBoundary(t *testing.T) {
	tests := []struct {
		name       string
		options    Options
		remoteAddr string
		headers    http.Header
		want       string
		wantErr    bool
	}{
		{
			name: "direct ignores spoofed headers", remoteAddr: "203.0.113.9:1234",
			headers: http.Header{"Fly-Client-Ip": {"198.51.100.1"}, "X-Forwarded-For": {"198.51.100.2"}},
			want:    "203.0.113.9",
		},
		{
			name:       "untrusted socket ignores configured header",
			options:    Options{ClientIPHeader: "Fly-Client-IP", TrustedProxyCIDRs: []string{"10.0.0.0/8"}},
			remoteAddr: "203.0.113.9:1234", headers: http.Header{"Fly-Client-Ip": {"198.51.100.1"}}, want: "203.0.113.9",
		},
		{
			name:       "fly header from trusted peer",
			options:    Options{ClientIPHeader: "Fly-Client-IP", TrustedProxyCIDRs: []string{"10.0.0.0/8"}},
			remoteAddr: "10.0.0.1:1234", headers: http.Header{"Fly-Client-Ip": {"::ffff:198.51.100.8"}}, want: "198.51.100.8",
		},
		{
			name:       "xff strips trusted proxies from right",
			options:    Options{ClientIPHeader: "X-Forwarded-For", TrustedProxyCIDRs: []string{"10.0.0.0/8"}},
			remoteAddr: "10.0.0.1:1234", headers: http.Header{"X-Forwarded-For": {"198.51.100.99, 203.0.113.7, 10.0.0.2"}}, want: "203.0.113.7",
		},
		{
			name: "trusted peer missing fly header", options: Options{ClientIPHeader: "Fly-Client-IP", TrustedProxyCIDRs: []string{"10.0.0.0/8"}},
			remoteAddr: "10.0.0.1:1234", wantErr: true,
		},
		{
			name: "trusted peer duplicate xff header", options: Options{ClientIPHeader: "X-Forwarded-For", TrustedProxyCIDRs: []string{"10.0.0.0/8"}},
			remoteAddr: "10.0.0.1:1234", headers: http.Header{"X-Forwarded-For": {"198.51.100.1", "198.51.100.2"}}, wantErr: true,
		},
		{
			name: "trusted peer malformed xff", options: Options{ClientIPHeader: "X-Forwarded-For", TrustedProxyCIDRs: []string{"10.0.0.0/8"}},
			remoteAddr: "10.0.0.1:1234", headers: http.Header{"X-Forwarded-For": {"198.51.100.1, nope"}}, wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app, _ := newIPTestServer(t, memory.New(), tc.options)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header = tc.headers
			got, err := app.clientIP(req)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("clientIP = %s, want error", got)
				}
				return
			}
			if err != nil || got.String() != tc.want {
				t.Fatalf("clientIP = %s, %v; want %s", got, err, tc.want)
			}
		})
	}
}

type observedStore struct {
	storage.Store
	mu   sync.Mutex
	gets []string
}

func (s *observedStore) Get(ctx context.Context, key string) (storage.Object, error) {
	s.mu.Lock()
	s.gets = append(s.gets, key)
	s.mu.Unlock()
	return s.Store.Get(ctx, key)
}

type observedBody struct{ reads int }

func (b *observedBody) Read([]byte) (int, error) { b.reads++; return 0, io.EOF }

type heldPolicyStore struct {
	storage.Store
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *heldPolicyStore) Get(ctx context.Context, key string) (storage.Object, error) {
	if key == "config/ip-blocklist.json" {
		s.once.Do(func() { close(s.started) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return storage.Object{}, ctx.Err()
		}
	}
	return s.Store.Get(ctx, key)
}

func TestPolicyRefreshConcurrencyIsBoundedBeforeStorage(t *testing.T) {
	store := &heldPolicyStore{Store: memory.New(), started: make(chan struct{}), release: make(chan struct{})}
	app, _ := newIPTestServer(t, store, Options{})
	var group sync.WaitGroup
	for i := 0; i < 16; i++ {
		group.Go(func() {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/feedback/malformed", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			app.ServeHTTP(httptest.NewRecorder(), req)
		})
	}
	<-store.started
	deadline := time.After(time.Second)
	for len(app.authSlots) < cap(app.authSlots) {
		select {
		case <-deadline:
			t.Fatal("policy refresh requests did not fill bounded authentication slots")
		default:
			runtime.Gosched()
		}
	}

	result := make(chan int, 1)
	go func() {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/feedback/malformed", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		app.ServeHTTP(w, req)
		result <- w.Code
	}()
	select {
	case code := <-result:
		if code != http.StatusTooManyRequests {
			t.Fatalf("overflow policy refresh response=%d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("overflow policy refresh was not rejected promptly")
	}
	close(store.release)
	group.Wait()
}

func TestBlockedIntakeStopsBeforeEndpointAuthOrBodyRead(t *testing.T) {
	store := &observedStore{Store: memory.New()}
	app, svc := newIPTestServer(t, store, Options{})
	_, credential := createIPTestEndpoint(t, svc)
	if _, _, err := svc.SaveIPBlocklist(t.Context(), []service.IPRule{{CIDR: "203.0.113.0/24"}}, "", "actor"); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.gets = nil
	store.mu.Unlock()
	body := &observedBody{}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/feedback/"+credential, body)
	req.RemoteAddr = "203.0.113.9:1234"
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || body.reads != 0 {
		t.Fatalf("blocked response=%d body reads=%d", w.Code, body.reads)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, key := range store.gets {
		if strings.HasPrefix(key, "endpoints/") {
			t.Fatalf("blocked request read endpoint authentication object: %v", store.gets)
		}
	}
}

func TestMalformedPolicyFailsIntakeClosedBeforeBodyRead(t *testing.T) {
	store := memory.New()
	if _, err := store.Create(t.Context(), "config/ip-blocklist.json", []byte(`{"schema":1,"rules":[{"cidr":"bad"}]}`)); err != nil {
		t.Fatal(err)
	}
	app, _ := newIPTestServer(t, store, Options{})
	body := &observedBody{}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/feedback/malformed", body)
	req.RemoteAddr = "203.0.113.9:1234"
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable || body.reads != 0 {
		t.Fatalf("malformed policy response=%d body reads=%d", w.Code, body.reads)
	}
}

func TestAllowedIntakeStoresResolvedClientIP(t *testing.T) {
	app, svc := newIPTestServer(t, memory.New(), Options{
		ClientIPHeader: "Fly-Client-IP", TrustedProxyCIDRs: []string{"10.0.0.0/8"},
	})
	endpointID, credential := createIPTestEndpoint(t, svc)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/feedback/"+credential, strings.NewReader(`{"feedback":"hello"}`))
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Fly-Client-IP", "198.51.100.8")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("intake response=%d body=%s", w.Code, w.Body.String())
	}
	var receipt struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	sub, _, err := svc.GetSubmission(t.Context(), endpointID, receipt.ID)
	if err != nil || sub.ClientIP != "198.51.100.8" || strings.Contains(sub.Issue.Body, "198.51.100.8") {
		t.Fatalf("submission client metadata = %q err=%v", sub.ClientIP, err)
	}
	views := viewSubmissions([]service.Submission{sub})
	if len(views) != 1 || views[0].ClientIP != "198.51.100.8" || viewSubmission(sub).ClientIP != "198.51.100.8" {
		t.Fatalf("admin views omitted client IP: %+v", views)
	}
}

func TestIPBlocklistAdminAuthCSRFAndVersions(t *testing.T) {
	app, svc := newIPTestServer(t, memory.New(), Options{})
	anonymous := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/admin/ip-blocklist", nil)
	anonymousResponse := httptest.NewRecorder()
	app.ServeHTTP(anonymousResponse, anonymous)
	if anonymousResponse.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous response=%d", anonymousResponse.Code)
	}
	if err := app.bind(t.Context(), "darron", "123"); err != nil {
		t.Fatal(err)
	}
	cookie, err := app.sessionCookie(identity{ID: "123", Login: "darron", CSRF: "csrf"})
	if err != nil {
		t.Fatal(err)
	}

	call := func(method, body, csrf string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), method, "/api/v1/admin/ip-blocklist", strings.NewReader(body))
		req.AddCookie(cookie)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		w := httptest.NewRecorder()
		app.ServeHTTP(w, req)
		return w
	}

	if w := call(http.MethodPost, `{"rules":[],"version":""}`, ""); w.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF response=%d", w.Code)
	}
	w := call(http.MethodGet, "", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"version":""`) || !strings.Contains(w.Body.String(), `"rules":[]`) {
		t.Fatalf("empty GET response=%d body=%s", w.Code, w.Body.String())
	}
	w = call(http.MethodPost, `{"rules":[{"cidr":"192.0.2.1","note":"spammer"}],"version":""}`, "csrf")
	if w.Code != http.StatusOK {
		t.Fatalf("save response=%d body=%s", w.Code, w.Body.String())
	}
	var saved struct {
		Config  service.IPBlocklist `json:"config"`
		Version string              `json:"version"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Version == "" || saved.Config.UpdatedBy != "darron (123)" || saved.Config.Rules[0].CIDR != "192.0.2.1/32" {
		t.Fatalf("saved response = %+v", saved)
	}
	if w = call(http.MethodPost, `{"rules":[],"version":"stale"}`, "csrf"); w.Code != http.StatusConflict {
		t.Fatalf("stale response=%d body=%s", w.Code, w.Body.String())
	}
	config, version, err := svc.GetIPBlocklist(t.Context())
	if err != nil || version != saved.Version || len(config.Rules) != 1 {
		t.Fatalf("stale write changed config=%+v version=%q err=%v", config, version, err)
	}
}

func TestMalformedSocketAddressFailsIntakeClosed(t *testing.T) {
	app, _ := newIPTestServer(t, memory.New(), Options{})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/feedback/malformed", strings.NewReader(`{}`))
	req.RemoteAddr = "not-an-address"
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("malformed socket response=%d", w.Code)
	}
}

func TestIPBlocklistCoversIPv4AndIPv6(t *testing.T) {
	app, svc := newIPTestServer(t, memory.New(), Options{})
	if _, _, err := svc.SaveIPBlocklist(t.Context(), []service.IPRule{{CIDR: "192.0.2.0/24"}, {CIDR: "2001:db8::/32"}}, "", "actor"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"192.0.2.3", "::ffff:192.0.2.3", "2001:db8::4"} {
		blocked, err := svc.IPBlocked(t.Context(), netip.MustParseAddr(value))
		if err != nil || !blocked {
			t.Fatalf("%s blocked=%v err=%v", value, blocked, err)
		}
	}
	_ = app
}
