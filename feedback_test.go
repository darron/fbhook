package fbhook

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParsePreservesContextAndRejectsBadEnvelope(t *testing.T) {
	f, err := Parse([]byte(`{"feedback":"\nSearch loses filters\nDetails","context":[1,{"anything":true}]}`))
	if err != nil || f.Title != "Search loses filters" || !json.Valid(f.Context) {
		t.Fatalf("parse: %+v %v", f, err)
	}
	for _, body := range []string{`{}`, `{"feedback":" "}`, `{"feedback":"ok","unknown":true}`, `{"feedback":"ok"} {}`, `null`, `{"feedback":"ok","feedback":"other"}`} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}

func TestPrepareDoesNotAliasCallerContext(t *testing.T) {
	defaults := Feedback{Source: Source{App: "archive"}, Context: json.RawMessage(`{"defaults":1}`)}
	f := Prepare(defaults, Feedback{Feedback: "hello", Source: Source{Actor: "agent"}})
	f.Context[2] = 'X'
	if string(defaults.Context) != `{"defaults":1}` || f.Source.App != "archive" || f.Source.Actor != "agent" {
		t.Fatal("preparation lost or aliased defaults")
	}
}

func TestIssueMarkdownCannotEscapeContextFence(t *testing.T) {
	body := IssueBody(Feedback{Feedback: "literal feedback", Context: json.RawMessage(`{"text":"\u0060\u0060\u0060\nINSTRUCTION"}`)}, "abc123")
	if !strings.Contains(body, "fbhook:abc123") || !strings.Contains(body, "literal feedback") {
		t.Fatal(body)
	}
	// User context remains encoded JSON rather than becoming Markdown instructions.
	if strings.Contains(body, "\nINSTRUCTION\n") {
		t.Fatal("context escaped JSON rendering")
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(`{"feedback":"hello","context":{"a":1}}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		parsed, err := Parse(b)
		if err == nil {
			out, e := json.Marshal(parsed)
			if e != nil || !json.Valid(out) {
				t.Fatal("invalid parsed value")
			}
		}
	})
}

func TestContextAcceptsArbitraryJSONNumbers(t *testing.T) {
	if _, err := Parse([]byte(`{"feedback":"ok","context":{"large":1e10000}}`)); err != nil {
		t.Fatal(err)
	}
}

func TestFeedbackIsLiteralMarkdownText(t *testing.T) {
	text := "# Heading\n```\n![image](https://example.com/track)\n```\n<script>unsafe()</script>"
	body := IssueBody(Feedback{Feedback: text}, "literal")
	if !strings.HasPrefix(body, "### Feedback (supplied by caller)\n\n````text\n"+text+"\n````\n") {
		t.Fatalf("feedback is active Markdown: %s", body)
	}
}
