package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/darron/fbhook/service"
	"net/http"
	"net/http/httptest"
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
