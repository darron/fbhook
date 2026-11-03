package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/darron/fbhook/service"
	"github.com/darron/fbhook/storage/memory"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type browserProvider struct{}

func (browserProvider) Validate(context.Context, service.Destination, string) error { return nil }
func (browserProvider) Create(_ context.Context, _ service.Destination, _ string, i service.Issue) (service.IssueRef, error) {
	if strings.Contains(i.Title, "uncertain") {
		return service.IssueRef{}, &service.ProviderError{Kind: "transport", Uncertain: true}
	}
	return service.IssueRef{ID: "42", URL: "https://github.com/example/repo/issues/42"}, nil
}
func (browserProvider) Find(context.Context, service.Destination, string, service.Issue, string) (service.IssueRef, error) {
	return service.IssueRef{}, errors.New("not confirmed")
}

// TestBrowserSmoke supplies signed test sessions without adding any production
// authentication bypass. The fake provider is compiled only into this test.
func TestBrowserSmoke(t *testing.T) {
	if os.Getenv("FBHOOK_BROWSER") != "1" {
		t.Skip("opt-in Python Playwright + Chromium required")
	}
	ctx := context.Background()
	svc, e := service.New(memory.New(), service.Options{EncryptionKey: bytes.Repeat([]byte{1}, 32), Providers: map[string]service.Provider{"github": browserProvider{}, "linear": browserProvider{}}})
	if e != nil {
		t.Fatal(e)
	}
	d, e := svc.SaveDestination(ctx, service.DestinationInput{Name: "Product feedback", Provider: "github", Target: "example/product", Labels: []string{"feedback"}, Token: "test-only"})
	if e != nil {
		t.Fatal(e)
	}
	_, credential, e := svc.CreateEndpoint(ctx, "Shared application feedback", d.ID, nil)
	if e != nil {
		t.Fatal(e)
	}
	_, _ = svc.Accept(ctx, credential, []byte(`{"feedback":"Keep search filters when returning to results","source":{"app":"Archive","actor":"agent"},"context":{"page":"/search","filters":{"year":2026},"large_integer":9007199254740993,"large_exponent":1e10000,"literal":"<script>window.injected=true</script>"}}`), "one")
	_, _ = svc.Accept(ctx, credential, []byte(`{"feedback":"An uncertain export response","source":{"app":"Converge","actor":"agent"},"context":{"request_id":"req_example"}}`), "two")
	if e = svc.Scan(ctx, false); e != nil {
		t.Fatal(e)
	}
	_, _ = svc.Accept(ctx, credential, []byte(`{"feedback":"Make the date range easier to change","source":{"app":"Archive","actor":"human"}}`), "three")
	dev, _, e := svc.CreateEndpoint(ctx, "Development & experiments", d.ID, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = svc.UpdateEndpoint(ctx, dev.ID, dev.Name, d.ID, true); e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewUnstartedServer(nil)
	app, e := New(svc, Options{BaseURL: "http://" + srv.Listener.Addr().String(), OAuthClientID: "test", OAuthClientSecret: "test", AllowedUsers: []string{"darron"}, SessionKey: bytes.Repeat([]byte{2}, 64)})
	if e != nil {
		t.Fatal(e)
	}
	srv.Config.Handler = app
	srv.Start()
	defer srv.Close()
	if e = app.bind(ctx, "darron", "123"); e != nil {
		t.Fatal(e)
	}
	cookie, e := app.sessionCookie(identity{ID: "123", Login: "darron", CSRF: "test-csrf"})
	if e != nil {
		t.Fatal(e)
	}
	artifacts, _ := filepath.Abs("../artifacts")
	input, _ := json.Marshal(map[string]string{"url": srv.URL, "cookie": cookie.Value, "artifacts": artifacts})
	python := os.Getenv("FBHOOK_PYTHON")
	if python == "" {
		python = "python3"
	}
	runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	command := exec.CommandContext(runCtx, python, "../scripts/browser_smoke.py") // #nosec G204 G702 -- opt-in test runner chosen by the developer.
	command.Stdin = bytes.NewReader(input)
	output, e := command.CombinedOutput()
	if e != nil {
		t.Fatalf("browser smoke: %v\n%s", e, output)
	}
	t.Log(string(output))
}
