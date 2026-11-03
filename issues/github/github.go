// Package github delivers feedback as GitHub issues using a scoped API token.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/darron/fbhook/issues/internal/api"
	"github.com/darron/fbhook/service"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type Provider struct {
	http *http.Client
	base string
}

func New(c *http.Client, base string) *Provider {
	if base == "" {
		base = "https://api.github.com"
	}
	return &Provider{http: api.Client(c), base: strings.TrimRight(base, "/")}
}

var targetPattern = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*/[A-Za-z0-9_-][A-Za-z0-9_.-]*$`)

func (p *Provider) call(ctx context.Context, method, path, token string, in, out any, creation bool) error {
	b, e := api.Call(ctx, p.http, method, p.base+path, "Bearer "+token, in, creation)
	if e != nil {
		return e
	}
	if json.Unmarshal(b, out) != nil {
		return &service.ProviderError{Kind: "invalid_response", Uncertain: creation}
	}
	return nil
}
func (p *Provider) Validate(ctx context.Context, d service.Destination, token string) error {
	if !targetPattern.MatchString(d.Target) || d.Project != "" {
		return errors.New("GitHub destination requires owner/repository")
	}
	var repo struct {
		HasIssues          bool `json:"has_issues"`
		Archived, Disabled bool
		Permissions        struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	if e := p.call(ctx, "GET", "/repos/"+d.Target, token, nil, &repo, false); e != nil {
		return e
	}
	if !repo.HasIssues || repo.Archived || repo.Disabled {
		return errors.New("repository cannot accept issues")
	}
	if len(d.Labels) > 0 && !repo.Permissions.Push {
		return errors.New("configured labels require repository push permission")
	}
	for _, label := range d.Labels {
		if label == "" || len(label) > 100 {
			return errors.New("invalid label")
		}
		var out struct{ Name string }
		if e := p.call(ctx, "GET", "/repos/"+d.Target+"/labels/"+url.PathEscape(label), token, nil, &out, false); e != nil {
			return e
		}
	}
	return nil
}

type issueResponse struct {
	Number      int             `json:"number"`
	URL         string          `json:"html_url"`
	Body        string          `json:"body"`
	PullRequest json.RawMessage `json:"pull_request"`
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (p *Provider) Create(ctx context.Context, d service.Destination, token string, issue service.Issue) (service.IssueRef, error) {
	if !targetPattern.MatchString(d.Target) {
		return service.IssueRef{}, &service.ProviderError{Kind: "invalid_target"}
	}
	payload := struct {
		Title  string   `json:"title"`
		Body   string   `json:"body"`
		Labels []string `json:"labels"`
	}{issue.Title, issue.Body, d.Labels}
	var out issueResponse
	if e := p.call(ctx, "POST", "/repos/"+d.Target+"/issues", token, payload, &out, true); e != nil {
		return service.IssueRef{}, e
	}
	if out.Number <= 0 || out.URL == "" {
		return service.IssueRef{}, &service.ProviderError{Kind: "invalid_create_response", Uncertain: true}
	}
	ref := service.IssueRef{ID: strconv.Itoa(out.Number), URL: out.URL}
	if !hasLabels(out, d.Labels) {
		return ref, &service.ProviderError{Kind: "defaults_not_applied"}
	}
	return ref, nil
}
func (p *Provider) Find(ctx context.Context, d service.Destination, token string, issue service.Issue, reference string) (service.IssueRef, error) {
	if !targetPattern.MatchString(d.Target) {
		return service.IssueRef{}, errors.New("invalid target")
	}
	if reference != "" {
		n, e := strconv.Atoi(reference)
		if e != nil || n <= 0 {
			return service.IssueRef{}, errors.New("expected issue number")
		}
		var out issueResponse
		if e = p.call(ctx, "GET", fmt.Sprintf("/repos/%s/issues/%d", d.Target, n), token, nil, &out, false); e != nil {
			return service.IssueRef{}, e
		}
		if out.Number != n || out.URL == "" || !hasLabels(out, d.Labels) || len(out.PullRequest) > 0 || !strings.HasSuffix(strings.TrimSpace(out.Body), "<!-- fbhook:"+issue.ID+" -->") {
			return service.IssueRef{}, errors.New("issue marker or identity mismatch")
		}
		return service.IssueRef{ID: strconv.Itoa(n), URL: out.URL}, nil
	}
	var result struct {
		Items []struct {
			Number int `json:"number"`
		} `json:"items"`
	}
	q := url.Values{"q": {`repo:` + d.Target + ` "fbhook:` + issue.ID + `" in:body is:issue`}, "per_page": {"20"}}
	if e := p.call(ctx, "GET", "/search/issues?"+q.Encode(), token, nil, &result, false); e != nil {
		return service.IssueRef{}, e
	}
	for _, candidate := range result.Items {
		if ref, e := p.Find(ctx, d, token, issue, strconv.Itoa(candidate.Number)); e == nil {
			return ref, nil
		}
	}
	return service.IssueRef{}, errors.New("existing issue not confirmed")
}

func hasLabels(issue issueResponse, expected []string) bool {
	for _, wanted := range expected {
		found := false
		for _, actual := range issue.Labels {
			if strings.EqualFold(actual.Name, wanted) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
