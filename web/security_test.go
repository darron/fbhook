package web

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/darron/fbhook/service"
	"github.com/darron/fbhook/storage"
	"github.com/darron/fbhook/storage/memory"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func intakeFixture(t *testing.T, app *Server) string {
	t.Helper()
	svc, err := service.New(memory.New(), service.Options{EncryptionKey: []byte(strings.Repeat("e", 32)), Providers: map[string]service.Provider{"github": browserProvider{}}})
	if err != nil {
		t.Fatal(err)
	}
	app.service = svc
	d, err := svc.SaveDestination(t.Context(), service.DestinationInput{Name: "Fixture", Provider: "github", Target: "example/repo", Token: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	_, credential, err := svc.CreateEndpoint(t.Context(), "Fixture", d.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return credential
}

func postFeedback(t *testing.T, app *Server, credential string) int {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/feedback/"+credential, strings.NewReader(`{"feedback":"fixture"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	return w.Code
}

func TestInvalidCredentialsCannotSpendAcceptanceQuota(t *testing.T) {
	app, _ := testServer(t)
	credential := intakeFixture(t, app)
	id, _, _ := strings.Cut(credential, ".")
	for i := 0; i < 650; i++ {
		invalid := "malformed"
		if i%2 == 0 {
			invalid = id + "." + strings.Repeat("x", 43)
		}
		if got := postFeedback(t, app, invalid); got != http.StatusNotFound {
			t.Fatalf("invalid credential returned %d at request %d", got, i)
		}
	}
	if got := postFeedback(t, app, credential); got != http.StatusAccepted {
		t.Fatalf("unauthenticated requests blocked valid feedback: %d", got)
	}
}

func TestAuthenticatedEndpointAndGlobalAcceptanceLimits(t *testing.T) {
	app, _ := testServer(t)
	credential := intakeFixture(t, app)
	epID, _, _ := strings.Cut(credential, ".")
	ep, _, err := app.service.GetEndpoint(t.Context(), epID)
	if err != nil {
		t.Fatal(err)
	}
	for endpoint := 0; endpoint < 5; endpoint++ {
		for i := 0; i < 120; i++ {
			if got := postFeedback(t, app, credential); got != http.StatusAccepted {
				t.Fatalf("valid intake %d/%d returned %d", endpoint, i, got)
			}
		}
		// These per-endpoint denials must not spend the global acceptance quota.
		for i := 0; i < 10; i++ {
			if got := postFeedback(t, app, credential); got != http.StatusTooManyRequests {
				t.Fatalf("endpoint limit not enforced: %d", got)
			}
		}
		_, credential, err = app.service.CreateEndpoint(t.Context(), "Another", ep.DestinationID, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := postFeedback(t, app, credential); got != http.StatusTooManyRequests {
		t.Fatalf("global limit not enforced: %d", got)
	}
}

type countingReadinessStore struct {
	storage.Store
	lists atomic.Int32
}

func (s *countingReadinessStore) List(ctx context.Context, prefix, cursor string, limit int) (storage.Page, error) {
	s.lists.Add(1)
	return s.Store.List(ctx, prefix, cursor, limit)
}

func TestReadinessBurstSharesStorageProbe(t *testing.T) {
	app, _ := testServer(t)
	store := &countingReadinessStore{Store: memory.New()}
	svc, err := service.New(store, service.Options{EncryptionKey: []byte(strings.Repeat("e", 32))})
	if err != nil {
		t.Fatal(err)
	}
	app.service = svc
	var group sync.WaitGroup
	for i := 0; i < 100; i++ {
		group.Go(func() {
			w := httptest.NewRecorder()
			app.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))
			if w.Code != http.StatusOK {
				t.Errorf("readiness: %d", w.Code)
			}
		})
	}
	group.Wait()
	if got := store.lists.Load(); got != 1 {
		t.Fatalf("public readiness burst made %d storage calls", got)
	}
}

func TestSubmissionListsOmitPayloadsButDetailsRetainThem(t *testing.T) {
	app, srv := testServer(t)
	credential := intakeFixture(t, app)
	if err := app.bind(t.Context(), "darron", "123"); err != nil {
		t.Fatal(err)
	}
	cookie, err := app.sessionCookie(identity{ID: "123", Login: "darron", CSRF: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"title":"Fixture title","feedback":"` + strings.Repeat("private-feedback-", 2400) + `","source":{"app":"` + strings.Repeat("app", 4000) + `"},"context":{"private":"private-context"}}`)
	var detail string
	for i := 0; i < 50; i++ {
		receipt, err := app.service.Accept(t.Context(), credential, payload, fmt.Sprintf("large-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		id, _, _ := strings.Cut(credential, ".")
		detail = "/api/v1/admin/submissions/" + id + "/" + receipt.ID
	}
	for _, path := range []string{"/api/v1/admin/submissions", "/admin/submissions", detail} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(cookie)
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if err != nil || res.StatusCode != http.StatusOK {
			t.Fatalf("%s: status=%d err=%v", path, res.StatusCode, err)
		}
		if path == detail {
			if !strings.Contains(string(body), "private-feedback-") || !strings.Contains(string(body), "private-context") || !strings.Contains(string(body), "context_json") {
				t.Fatal("detail lost original feedback or context")
			}
			continue
		}
		if len(body) > 100000 || strings.Contains(string(body), "private-feedback-") || strings.Contains(string(body), "private-context") || strings.Contains(string(body), "context_json") {
			t.Fatalf("list exposes full records or unbounded app names: %s (%d bytes)", path, len(body))
		}
		if path == "/api/v1/admin/submissions" {
			var list struct{ Items []map[string]json.RawMessage }
			if err := json.Unmarshal(body, &list); err != nil || len(list.Items) != 50 {
				t.Fatalf("incomplete list: %d %v", len(list.Items), err)
			}
			for _, item := range list.Items {
				if item["issue"] != nil || item["history"] != nil {
					t.Fatal("list includes issue body or history")
				}
			}
		}
	}
}

type waitingBody struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (b waitingBody) Read([]byte) (int, error) {
	b.started <- struct{}{}
	<-b.release
	return 0, io.EOF
}

func TestStalledInvalidBodiesDoNotBlockAuthenticatedIntake(t *testing.T) {
	app, _ := testServer(t)
	credential := intakeFixture(t, app)
	invalid := strings.Repeat("a", 24) + "." + strings.Repeat("x", 43)
	started, release := make(chan struct{}, 32), make(chan struct{})
	finished := make(chan int, 32)
	var group sync.WaitGroup
	defer group.Wait()
	defer close(release)
	for i := 0; i < 32; i++ {
		group.Go(func() {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/feedback/"+invalid, waitingBody{started: started, release: release})
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			app.ServeHTTP(w, r)
			finished <- w.Code
		})
	}
	reads := 0
	for i := 0; i < 32; i++ {
		select {
		case <-started:
			reads++
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("invalid credentials did not finish authentication")
		}
	}
	if got := postFeedback(t, app, credential); got != http.StatusAccepted {
		t.Fatalf("stalled unauthenticated bodies blocked valid feedback: %d (body reads=%d)", got, reads)
	}
	if reads != 0 {
		t.Fatalf("read %d unauthenticated bodies", reads)
	}
}

func TestStalledAuthenticatedEndpointLeavesCapacityForOtherEndpoints(t *testing.T) {
	app, _ := testServer(t)
	credential := intakeFixture(t, app)
	id, _, _ := strings.Cut(credential, ".")
	ep, _, err := app.service.GetEndpoint(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := app.service.CreateEndpoint(t.Context(), "Other app", ep.DestinationID, nil)
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}, 4), make(chan struct{})
	var group sync.WaitGroup
	defer group.Wait()
	defer close(release)
	for i := 0; i < 4; i++ {
		group.Go(func() {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/feedback/"+credential, waitingBody{started: started, release: release})
			r.Header.Set("Content-Type", "application/json")
			app.ServeHTTP(httptest.NewRecorder(), r)
		})
	}
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("authenticated requests did not reach body reads")
		}
	}
	if got := postFeedback(t, app, credential); got != http.StatusTooManyRequests {
		t.Errorf("stalled endpoint concurrency was not capped: %d", got)
	}
	if got := postFeedback(t, app, other); got != http.StatusAccepted {
		t.Errorf("one authenticated endpoint blocked another: %d", got)
	}
}
