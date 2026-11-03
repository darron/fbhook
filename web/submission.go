package web

import (
	"bytes"
	"encoding/json"
	"github.com/darron/fbhook/service"
	"strings"
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
func viewSubmissions(items []service.Submission) []submissionView {
	out := make([]submissionView, 0, len(items))
	for _, item := range items {
		out = append(out, viewSubmission(item))
	}
	return out
}
