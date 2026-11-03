package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/darron/fbhook/service"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestValidateCreateAndVerifyMarker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing credential")
		}
		switch {
		case r.Method == "POST":
			var in struct {
				Title, Body string
				Labels      []string
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.Title != "Issue" || len(in.Labels) != 1 || in.Body != "content\n<!-- fbhook:abc -->" {
				t.Errorf("payload %+v", in)
			}
			w.WriteHeader(201)
			_, _ = fmt.Fprint(w, `{"number":42,"html_url":"https://github.com/o/r/issues/42","labels":[{"name":"feedback"}]}`)
		case r.URL.Path == "/repos/o/r/issues/42":
			_, _ = fmt.Fprint(w, `{"number":42,"html_url":"https://github.com/o/r/issues/42","body":"content\n<!-- fbhook:abc -->","labels":[{"name":"feedback"}]}`)
		case r.URL.Path == "/repos/o/r":
			_, _ = fmt.Fprint(w, `{"has_issues":true,"permissions":{"push":true}}`)
		default:
			_, _ = fmt.Fprint(w, `{"name":"feedback"}`)
		}
	}))
	defer srv.Close()
	p := New(srv.Client(), srv.URL)
	d := service.Destination{Target: "o/r", Labels: []string{"feedback"}}
	if e := p.Validate(context.Background(), d, "token"); e != nil {
		t.Fatal(e)
	}
	issue := service.Issue{ID: "abc", Title: "Issue", Body: "content\n<!-- fbhook:abc -->"}
	ref, e := p.Create(context.Background(), d, "token", issue)
	if e != nil || ref.ID != "42" {
		t.Fatalf("create %+v %v", ref, e)
	}
	if _, e = p.Find(context.Background(), d, "token", issue, "42"); e != nil {
		t.Fatal(e)
	}
	issue.ID = "different"
	if _, e = p.Find(context.Background(), d, "token", issue, "42"); e == nil {
		t.Fatal("linked wrong marker")
	}
}
func TestServerFailureIsUncertain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(502) }))
	defer srv.Close()
	p := New(srv.Client(), srv.URL)
	_, e := p.Create(context.Background(), service.Destination{Target: "o/r"}, "token", service.Issue{})
	var pe *service.ProviderError
	if !errors.As(e, &pe) || !pe.Uncertain {
		t.Fatalf("unsafe error %v", e)
	}
}

func TestSecondaryRateLimitSchedulesRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.Header().Set("X-RateLimit-Remaining", "4000")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := New(srv.Client(), srv.URL).Create(t.Context(), service.Destination{Target: "o/r"}, "fixture", service.Issue{})
	var pe *service.ProviderError
	if !errors.As(err, &pe) || !pe.Retryable || pe.Uncertain || pe.RetryAfter != time.Minute {
		t.Fatalf("secondary rate limit became a permanent or uncertain failure: %v", err)
	}
}

func TestRejectsLabelsWithoutPushPermission(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/o/r" {
			_, _ = fmt.Fprint(w, `{"has_issues":true,"permissions":{"push":false}}`)
		} else {
			_, _ = fmt.Fprint(w, `{"name":"feedback"}`)
		}
	}))
	defer srv.Close()
	if err := New(srv.Client(), srv.URL).Validate(t.Context(), service.Destination{Target: "o/r", Labels: []string{"feedback"}}, "token"); err == nil {
		t.Fatal("accepted labels without permission to apply them")
	}
}
func TestDroppedLabelsPreserveReferenceAndFailVerification(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"number":42,"html_url":"https://github.com/o/r/issues/42","body":"<!-- fbhook:abc -->","labels":[]}`)
	}))
	defer srv.Close()
	p := New(srv.Client(), srv.URL)
	d := service.Destination{Target: "o/r", Labels: []string{"feedback"}}
	issue := service.Issue{ID: "abc"}
	ref, err := p.Create(t.Context(), d, "token", issue)
	var pe *service.ProviderError
	if ref.ID != "42" || !errors.As(err, &pe) || pe.Retryable || pe.Uncertain {
		t.Fatalf("lost defaults: ref=%+v error=%v", ref, err)
	}
	if _, err = p.Find(t.Context(), d, "token", issue, "42"); err == nil {
		t.Fatal("reconciliation ignored missing captured labels")
	}
}

func TestSecondaryRateLimitWithoutRetryHeader(t *testing.T) {
	for _, message := range []string{"You have exceeded a secondary rate limit. Please wait a few minutes before you try again.", "Resource not accessible by personal access token"} {
		t.Run(message, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("X-RateLimit-Remaining", "4000")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"message": message})
			}))
			defer srv.Close()
			_, err := New(srv.Client(), srv.URL).Create(t.Context(), service.Destination{Target: "o/r"}, "test", service.Issue{})
			var pe *service.ProviderError
			if !errors.As(err, &pe) {
				t.Fatalf("unclassified response: %v", err)
			}
			wantRetry := strings.Contains(message, "secondary rate limit")
			if pe.Retryable != wantRetry || pe.Uncertain || wantRetry && pe.RetryAfter < time.Minute {
				t.Fatalf("incorrect retry classification: %+v", pe)
			}
			if strings.Contains(err.Error(), message) {
				t.Fatal("upstream response leaked")
			}
		})
	}
}

func TestFindPropagatesCandidateOperationalErrors(t *testing.T) {
	for _, code := range []int{429, 403, 502} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			verifications := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/search/issues" {
					_, _ = fmt.Fprint(w, `{"items":[{"number":42},{"number":43}]}`)
					return
				}
				verifications++
				if code == 429 {
					w.Header().Set("Retry-After", "3600")
				}
				w.WriteHeader(code)
			}))
			defer srv.Close()
			_, err := New(srv.Client(), srv.URL).Find(t.Context(), service.Destination{Target: "o/r"}, "test", service.Issue{ID: "abc"}, "")
			var pe *service.ProviderError
			if !errors.As(err, &pe) || verifications != 1 {
				t.Fatalf("operational failure swallowed: %v requests=%d", err, verifications)
			}
			if code == 429 && (!pe.Retryable || pe.RetryAfter != time.Hour) {
				t.Fatalf("throttle hint lost: %+v", pe)
			}
		})
	}
}

func TestFindSkipsMissingAndMismatchedCandidates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/issues":
			_, _ = fmt.Fprint(w, `{"items":[{"number":40},{"number":41},{"number":42}]}`)
		case "/repos/o/r/issues/40":
			w.WriteHeader(404)
		case "/repos/o/r/issues/41":
			_, _ = fmt.Fprint(w, `{"number":41,"html_url":"https://github.com/o/r/issues/41","body":"wrong marker"}`)
		case "/repos/o/r/issues/42":
			_, _ = fmt.Fprint(w, `{"number":42,"html_url":"https://github.com/o/r/issues/42","body":"<!-- fbhook:abc -->"}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	ref, err := New(srv.Client(), srv.URL).Find(t.Context(), service.Destination{Target: "o/r"}, "test", service.Issue{ID: "abc"}, "")
	if err != nil || ref.ID != "42" {
		t.Fatalf("valid candidate not found: %+v %v", ref, err)
	}
}
