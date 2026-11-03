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
