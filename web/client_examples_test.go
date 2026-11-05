package web

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/darron/fbhook"
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

// Native client checks opt in because ordinary Go builds do not need these runtimes.
// This server uses memory storage and never starts a delivery worker.
func TestNativeClientExamples(t *testing.T) {
	if os.Getenv("FBHOOK_CLIENT_EXAMPLES") != "1" {
		t.Skip("run make client-examples after building the native packages")
	}
	svc, err := service.New(memory.New(), service.Options{
		EncryptionKey: []byte("01234567890123456789012345678901"),
		Providers:     map[string]service.Provider{"github": nativeExampleProvider{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(svc, Options{
		BaseURL: "http://127.0.0.1:8080", OAuthClientID: "test", OAuthClientSecret: "test",
		AllowedUsers: []string{"example"}, SessionKey: []byte(strings.Repeat("k", 64)),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args []string
	}{
		{"ruby", []string{"ruby", "-Iclients/ruby/lib", "clients/ruby/examples/submit.rb"}},
		{"rust", []string{filepath.Join(root, "clients/rust/target/debug/examples/submit")}},
		{"typescript", []string{"node", "clients/typescript/examples/submit.mjs"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			destination, err := svc.SaveDestination(t.Context(), service.DestinationInput{
				Name: tc.name, Provider: "github", Target: "example/repo", Token: "test-only",
			})
			if err != nil {
				t.Fatal(err)
			}
			endpoint, credential, err := svc.CreateEndpoint(t.Context(), tc.name, destination.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			var firstID string
			for attempt := 0; attempt < 2; attempt++ {
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				// #nosec G204 -- Every command and argument is fixed by the test cases above.
				cmd := exec.CommandContext(ctx, tc.args[0], tc.args[1:]...)
				cmd.Dir = root
				cmd.Env = append(os.Environ(), "FBHOOK_ENDPOINT="+srv.URL+"/api/v1/feedback/"+credential,
					"FBHOOK_IDEMPOTENCY_KEY="+tc.name+"-example-event")
				output, runErr := cmd.Output()
				cancel()
				if runErr != nil {
					t.Fatalf("example failed: %v", runErr)
				}
				var receipt fbhook.Receipt
				if json.Unmarshal(output, &receipt) != nil || receipt.ID == "" || receipt.Status != "accepted" {
					t.Fatal("example did not print a valid acceptance receipt")
				}
				if attempt == 0 {
					firstID = receipt.ID
				} else if receipt.ID != firstID {
					t.Fatal("example replay created a second submission")
				}
			}
			sub, _, err := svc.GetSubmission(t.Context(), endpoint.ID, firstID)
			if err != nil || sub.State != "pending" || sub.Attempts != 0 || sub.Reference != nil {
				t.Fatal("acceptance was not stored as pending without delivery")
			}
			if sub.Feedback.Source.App == "" || sub.Feedback.Source.Actor == "" || len(sub.Feedback.Context) == 0 {
				t.Fatal("example omitted source or JSON context")
			}
		})
	}
	items, cursor, err := svc.ListSubmissions(t.Context(), "")
	if err != nil || cursor != "" || len(items) != len(cases) {
		t.Fatal("example retries did not produce exactly one stored record per language")
	}
}

type nativeExampleProvider struct{}

func (nativeExampleProvider) Validate(context.Context, service.Destination, string) error {
	return nil
}
func (nativeExampleProvider) Create(context.Context, service.Destination, string, service.Issue) (service.IssueRef, error) {
	return service.IssueRef{}, errors.New("example tests must never deliver issues")
}
func (nativeExampleProvider) Find(context.Context, service.Destination, string, service.Issue, string) (service.IssueRef, error) {
	return service.IssueRef{}, errors.New("example tests must never query providers")
}
