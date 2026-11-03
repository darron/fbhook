package web

import (
	"bytes"
	"encoding/json"
	"github.com/darron/fbhook/service"
	"strings"
	"time"
)

// The JSON text is rendered as text in the browser, avoiding JavaScript number
// parsing. UseNumber preserves each number's original JSON representation.
type submissionView struct {
	service.Submission
	ContextJSON string `json:"context_json"`
}

func viewSubmission(sub service.Submission) submissionView {
	var context any
	decoder := json.NewDecoder(bytes.NewReader(sub.Feedback.Context))
	decoder.UseNumber()
	_ = decoder.Decode(&context) // Accepted context has already been validated.
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(struct {
		Source  any `json:"source"`
		Context any `json:"context"`
	}{sub.Feedback.Source, context})
	return submissionView{Submission: sub, ContextJSON: strings.TrimSuffix(body.String(), "\n")}
}

type submissionSummary struct {
	ID         string `json:"id"`
	EndpointID string `json:"endpoint_id"`
	Feedback   struct {
		Title  string `json:"title"`
		Source struct {
			App string `json:"app"`
		} `json:"source"`
	} `json:"feedback"`
	Destination struct {
		Name string `json:"name"`
	} `json:"destination"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func viewSubmissions(items []service.Submission) []submissionSummary {
	out := make([]submissionSummary, 0, len(items))
	for _, item := range items {
		summary := submissionSummary{ID: item.ID, EndpointID: item.EndpointID, State: item.State, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}
		summary.Feedback.Title = item.Feedback.Title
		app := []rune(item.Feedback.Source.App)
		summary.Feedback.Source.App = string(app[:min(len(app), 200)])
		summary.Destination.Name = item.Destination.Name
		out = append(out, summary)
	}
	return out
}
