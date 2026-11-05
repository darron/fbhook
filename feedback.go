// Package fbhook prepares and submits feedback without depending on server,
// storage, or issue-tracker packages. Applications choose the context they send.
package fbhook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const Version = "0.2.0"
const MaxRequestBytes = 64 * 1024
const MaxIssueBytes = 60000

type Source struct {
	App         string `json:"app,omitempty"`
	Version     string `json:"version,omitempty"`
	Environment string `json:"environment,omitempty"`
	Actor       string `json:"actor,omitempty"`
}

type Feedback struct {
	Title    string          `json:"title,omitempty"`
	Feedback string          `json:"feedback"`
	Source   Source          `json:"source,omitempty"`
	Context  json.RawMessage `json:"context,omitempty"`
}

type Receipt struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// Parse validates a strict envelope while keeping context as JSON. Duplicate
// object keys are rejected rather than allowing different readers to disagree.
func Parse(body []byte) (Feedback, error) {
	var f Feedback
	if len(body) > MaxRequestBytes || !utf8.Valid(body) {
		return f, errors.New("feedback exceeds limit or is not UTF-8")
	}
	if len(bytes.TrimSpace(body)) == 0 || bytes.TrimSpace(body)[0] != '{' {
		return f, errors.New("feedback must be a JSON object")
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if err := uniqueJSON(d, 0); err != nil {
		return f, err
	}
	if _, err := d.Token(); err != io.EOF {
		return f, errors.New("expected one JSON object")
	}
	d = json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(&f); err != nil {
		return f, errors.New("invalid feedback envelope")
	}
	if strings.TrimSpace(f.Feedback) == "" {
		return f, errors.New("feedback is required")
	}
	if strings.TrimSpace(f.Title) == "" {
		for _, line := range strings.Split(f.Feedback, "\n") {
			if title := strings.TrimSpace(line); title != "" {
				r := []rune(title)
				if len(r) > 200 {
					r = r[:200]
				}
				f.Title = string(r)
				break
			}
		}
	}
	if len([]rune(f.Title)) > 200 || strings.ContainsAny(f.Title, "\r\n") {
		return f, errors.New("title must be one line of at most 200 characters")
	}
	return f, nil
}

func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("JSON nesting exceeds limit")
	}
	token, err := d.Token()
	if err != nil {
		return errors.New("invalid JSON")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return errors.New("invalid JSON object")
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return errors.New("duplicate or invalid JSON key")
			}
			seen[s] = true
			if e = uniqueJSON(d, depth+1); e != nil {
				return e
			}
		}
	case '[':
		for d.More() {
			if e := uniqueJSON(d, depth+1); e != nil {
				return e
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, err = d.Token()
	return err
}

// Prepare overlays non-empty submission fields onto application defaults. The
// context value replaces the default context and is copied, never mutated/aliased.
func Prepare(defaults, submission Feedback) Feedback {
	out := defaults
	if submission.Title != "" {
		out.Title = submission.Title
	}
	if submission.Feedback != "" {
		out.Feedback = submission.Feedback
	}
	if submission.Source.App != "" {
		out.Source.App = submission.Source.App
	}
	if submission.Source.Version != "" {
		out.Source.Version = submission.Source.Version
	}
	if submission.Source.Environment != "" {
		out.Source.Environment = submission.Source.Environment
	}
	if submission.Source.Actor != "" {
		out.Source.Actor = submission.Source.Actor
	}
	if len(submission.Context) > 0 {
		out.Context = submission.Context
	}
	out.Context = bytes.Clone(out.Context)
	return out
}

// IssueBody renders caller feedback and JSON inside fences longer than any
// backtick run in the content, keeping caller strings from changing formatting.
func IssueBody(f Feedback, id string) string {
	var b strings.Builder
	b.WriteString("### Feedback (supplied by caller)\n\n")
	b.WriteString(literalFence([]byte(f.Feedback), "text"))
	b.WriteString("\n\n### Source (supplied by caller)\n\n")
	source, _ := json.MarshalIndent(f.Source, "", "  ")
	b.WriteString(jsonFence(source))
	if len(f.Context) > 0 {
		var pretty bytes.Buffer
		if json.Indent(&pretty, f.Context, "", "  ") == nil {
			b.WriteString("\n\n### Context\n\n")
			b.WriteString(jsonFence(pretty.Bytes()))
		}
	}
	fmt.Fprintf(&b, "\n\n<!-- fbhook:%s -->", id)
	return b.String()
}

func jsonFence(body []byte) string { return literalFence(body, "json") }

func literalFence(body []byte, language string) string {
	longest, run := 0, 0
	for _, c := range body {
		if c == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", max(3, longest+1))
	return fence + language + "\n" + string(body) + "\n" + fence
}
