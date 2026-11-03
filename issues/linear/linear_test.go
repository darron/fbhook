package linear

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

func TestCreateAndGraphQLErrors(t *testing.T) {
	rejected := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token" {
			t.Error("API key auth")
		}
		var q struct {
			Query     string                     `json:"query"`
			Variables map[string]json.RawMessage `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		if rejected {
			_, _ = fmt.Fprint(w, `{"data":{"issueCreate":null},"errors":[{"message":"private sensitive message"}]}`)
			return
		}
		if !strings.Contains(q.Query, "issueCreate") {
			t.Error("missing mutation")
		}
		var input struct {
			TeamID      string `json:"teamId"`
			Description string `json:"description"`
		}
		_ = json.Unmarshal(q.Variables["input"], &input)
		if input.TeamID != "team-1" || !strings.Contains(input.Description, "fbhook:abc") {
			t.Errorf("input %+v", input)
		}
		_, _ = fmt.Fprint(w, `{"data":{"issueCreate":{"success":true,"issue":{"id":"uuid","url":"https://linear.app/team/issue/T-1"}}}}`)
	}))
	defer srv.Close()
	p := New(srv.Client(), srv.URL)
	d := service.Destination{Target: "team-1"}
	issue := service.Issue{ID: "abc", Title: "hello", Body: "<!-- fbhook:abc -->"}
	ref, e := p.Create(context.Background(), d, "token", issue)
	if e != nil || ref.ID != "uuid" {
		t.Fatalf("%+v %v", ref, e)
	}
	rejected = true
	_, e = p.Create(context.Background(), d, "token", issue)
	var pe *service.ProviderError
	if !errors.As(e, &pe) || !pe.Uncertain || strings.Contains(e.Error(), "sensitive") {
		t.Fatalf("GraphQL error %v", e)
	}
}

func TestFindRequiresCapturedLabels(t *testing.T) {
	for _, reference := range []string{"", "issue-1"} {
		for _, labels := range []string{`[]`, `[{"id":"required-label"}]`} {
			t.Run(fmt.Sprintf("reference=%s labels=%s", reference, labels), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var q struct{ Query string }
					_ = json.NewDecoder(r.Body).Decode(&q)
					if !strings.Contains(q.Query, "labels(") {
						t.Error("reconciliation did not request labels")
					}
					body := `{"id":"issue-1","url":"https://linear.app/t/issue/T-1","description":"<!-- fbhook:abc -->","team":{"id":"team-1"},"project":{"id":"project-1"},"labels":{"nodes":` + labels + `,"pageInfo":{"hasNextPage":false,"endCursor":""}}}`
					if strings.Contains(q.Query, "issues(first") {
						_, _ = fmt.Fprint(w, `{"data":{"issues":{"nodes":[`+body+`]}}}`)
					} else {
						_, _ = fmt.Fprint(w, `{"data":{"issue":`+body+`}}`)
					}
				}))
				defer srv.Close()
				ref, err := New(srv.Client(), srv.URL).Find(t.Context(), service.Destination{Target: "team-1", Project: "project-1", Labels: []string{"required-label"}}, "test", service.Issue{ID: "abc"}, reference)
				if labels == `[]` {
					if err == nil || ref.ID != "" {
						t.Fatalf("accepted issue missing captured label: %+v", ref)
					}
				} else if err != nil || ref.ID != "issue-1" {
					t.Fatalf("matching issue rejected: %+v %v", ref, err)
				}
			})
		}
	}
}

func TestFindPaginatesLabelsAndPreservesThrottle(t *testing.T) {
	for _, mode := range []string{"later-label", "throttle", "stuck-cursor"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var q struct {
					Query     string
					Variables map[string]any
				}
				_ = json.NewDecoder(r.Body).Decode(&q)
				if calls == 1 {
					body := `{"id":"issue-1","url":"https://linear.app/t/issue/T-1","description":"<!-- fbhook:abc -->","team":{"id":"team-1"},"labels":{"nodes":[{"id":"unrelated"}],"pageInfo":{"hasNextPage":true,"endCursor":"next"}}}`
					_, _ = fmt.Fprint(w, `{"data":{"issues":{"nodes":[`+body+`]}}}`)
					return
				}
				if q.Variables["after"] != "next" || q.Variables["id"] != "issue-1" || !strings.Contains(q.Query, "pageInfo") {
					t.Errorf("label pagination request: %+v", q)
				}
				if mode == "throttle" {
					w.Header().Set("Retry-After", "3600")
					w.WriteHeader(429)
					return
				}
				if mode == "stuck-cursor" {
					_, _ = fmt.Fprint(w, `{"data":{"issue":{"id":"issue-1","labels":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"next"}}}}}`)
					return
				}
				_, _ = fmt.Fprint(w, `{"data":{"issue":{"id":"issue-1","labels":{"nodes":[{"id":"required-label"}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`)
			}))
			defer srv.Close()
			ref, err := New(srv.Client(), srv.URL).Find(t.Context(), service.Destination{Target: "team-1", Labels: []string{"required-label"}}, "test", service.Issue{ID: "abc"}, "")
			if calls != 2 {
				t.Fatalf("label pagination made %d requests", calls)
			}
			switch mode {
			case "later-label":
				if err != nil || ref.ID != "issue-1" {
					t.Fatalf("later captured label rejected: %+v %v", ref, err)
				}
			case "throttle":
				var pe *service.ProviderError
				if !errors.As(err, &pe) || pe.RetryAfter != time.Hour {
					t.Fatalf("lost throttle hint: %v", err)
				}
			default:
				if err == nil {
					t.Fatal("nonadvancing pagination accepted")
				}
			}
		})
	}
}
