// Package linear delivers feedback through Linear's GraphQL API.
package linear

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/darron/fbhook/issues/internal/api"
	"github.com/darron/fbhook/service"
	"net/http"
	"regexp"
	"strings"
)

type Provider struct {
	http     *http.Client
	endpoint string
}

func New(c *http.Client, endpoint string) *Provider {
	if endpoint == "" {
		endpoint = "https://api.linear.app/graphql"
	}
	return &Provider{http: api.Client(c), endpoint: endpoint}
}
func (p *Provider) query(ctx context.Context, token, q string, vars any, out any, creation bool) error {
	b, e := api.Call(ctx, p.http, "POST", p.endpoint, token, map[string]any{"query": q, "variables": vars}, creation)
	if e != nil {
		return e
	}
	var envelope struct {
		Data   json.RawMessage   `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(b, &envelope) != nil || len(envelope.Errors) > 0 || len(envelope.Data) == 0 || json.Unmarshal(envelope.Data, out) != nil {
		return &service.ProviderError{Kind: "graphql_error", Uncertain: creation}
	}
	return nil
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9-]{1,100}$`)

func (p *Provider) Validate(ctx context.Context, d service.Destination, token string) error {
	if !identifier.MatchString(d.Target) {
		return errors.New("linear team ID required")
	}
	var team struct {
		Team *struct {
			ID string `json:"id"`
		} `json:"team"`
	}
	if e := p.query(ctx, token, `query($id:String!){team(id:$id){id}}`, map[string]any{"id": d.Target}, &team, false); e != nil {
		return e
	}
	if team.Team == nil || team.Team.ID != d.Target {
		return errors.New("team not found; use its UUID")
	}
	for _, id := range d.Labels {
		if !identifier.MatchString(id) {
			return errors.New("invalid label ID")
		}
		var out struct {
			IssueLabel *struct {
				ID   string
				Team *struct{ ID string }
			}
		}
		if e := p.query(ctx, token, `query($id:String!){issueLabel(id:$id){id team{id}}}`, map[string]any{"id": id}, &out, false); e != nil {
			return e
		}
		if out.IssueLabel == nil || out.IssueLabel.ID != id || out.IssueLabel.Team != nil && out.IssueLabel.Team.ID != d.Target {
			return errors.New("label not available to team")
		}
	}
	if d.Project != "" {
		if !identifier.MatchString(d.Project) {
			return errors.New("invalid project ID")
		}
		var out struct {
			Project *struct {
				ID    string
				Teams struct{ Nodes []struct{ ID string } }
			}
		}
		if e := p.query(ctx, token, `query($id:String!){project(id:$id){id teams(first:250){nodes{id}}}}`, map[string]any{"id": d.Project}, &out, false); e != nil {
			return e
		}
		found := false
		if out.Project != nil && out.Project.ID == d.Project {
			for _, t := range out.Project.Teams.Nodes {
				if t.ID == d.Target {
					found = true
				}
			}
		}
		if !found {
			return errors.New("project not available to team")
		}
	}
	return nil
}
func (p *Provider) Create(ctx context.Context, d service.Destination, token string, issue service.Issue) (service.IssueRef, error) {
	input := map[string]any{"title": issue.Title, "description": issue.Body, "teamId": d.Target, "labelIds": d.Labels}
	if d.Project != "" {
		input["projectId"] = d.Project
	}
	var out struct {
		IssueCreate struct {
			Success bool
			Issue   *struct{ ID, URL string }
		}
	}
	e := p.query(ctx, token, `mutation($input:IssueCreateInput!){issueCreate(input:$input){success issue{id url}}}`, map[string]any{"input": input}, &out, true)
	if e != nil {
		return service.IssueRef{}, e
	}
	if !out.IssueCreate.Success || out.IssueCreate.Issue == nil || out.IssueCreate.Issue.ID == "" || out.IssueCreate.Issue.URL == "" {
		return service.IssueRef{}, &service.ProviderError{Kind: "invalid_create_response", Uncertain: true}
	}
	return service.IssueRef{ID: out.IssueCreate.Issue.ID, URL: out.IssueCreate.Issue.URL}, nil
}

type foundIssue struct {
	ID, URL, Description string
	Team                 struct{ ID string }
	Project              *struct{ ID string }
}

func (p *Provider) Find(ctx context.Context, d service.Destination, token string, issue service.Issue, reference string) (service.IssueRef, error) {
	verify := func(out foundIssue) (service.IssueRef, error) {
		if out.ID == "" || out.URL == "" || out.Team.ID != d.Target || (d.Project != "" && (out.Project == nil || out.Project.ID != d.Project)) || !strings.HasSuffix(strings.TrimSpace(out.Description), "<!-- fbhook:"+issue.ID+" -->") {
			return service.IssueRef{}, errors.New("issue target or marker mismatch")
		}
		return service.IssueRef{ID: out.ID, URL: out.URL}, nil
	}
	if reference != "" {
		if !identifier.MatchString(reference) {
			return service.IssueRef{}, errors.New("invalid issue ID")
		}
		var out struct{ Issue *foundIssue }
		if e := p.query(ctx, token, `query($id:String!){issue(id:$id){id url description team{id} project{id}}}`, map[string]any{"id": reference}, &out, false); e != nil {
			return service.IssueRef{}, e
		}
		if out.Issue == nil {
			return service.IssueRef{}, errors.New("issue not found")
		}
		return verify(*out.Issue)
	}
	var out struct{ Issues struct{ Nodes []foundIssue } }
	e := p.query(ctx, token, `query($team:ID!,$marker:String!){issues(first:50,filter:{team:{id:{eq:$team}},description:{contains:$marker}}){nodes{id url description team{id} project{id}}}}`, map[string]any{"team": d.Target, "marker": "fbhook:" + issue.ID}, &out, false)
	if e != nil {
		return service.IssueRef{}, e
	}
	for _, candidate := range out.Issues.Nodes {
		if ref, e := verify(candidate); e == nil {
			return ref, nil
		}
	}
	return service.IssueRef{}, errors.New("existing issue not confirmed")
}
