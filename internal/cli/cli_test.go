package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendUsesClientAndCleanJSONOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Idempotency-Key") != "same" {
			t.Error("missing idempotency")
		}
		w.WriteHeader(202)
		_, _ = fmt.Fprint(w, `{"id":"receipt","status":"accepted"}`)
	}))
	defer srv.Close()
	t.Setenv("FBHOOK_ENDPOINT", srv.URL+"/api/v1/feedback/id.secret")
	var out, stderr bytes.Buffer
	e := Run(context.Background(), []string{"send", "--stdin", "--key", "same"}, strings.NewReader(`{"feedback":"keep context","source":{"app":"app"}}`), &out, &stderr, BuildInfo{})
	if e != nil {
		t.Fatal(e)
	}
	var receipt map[string]string
	if json.Unmarshal(out.Bytes(), &receipt) != nil || receipt["id"] != "receipt" || stderr.Len() != 0 {
		t.Fatalf("output %q stderr %q", out.String(), stderr.String())
	}
}
func TestVersionAndInvalidConfig(t *testing.T) {
	var out, errOut bytes.Buffer
	if e := Run(context.Background(), []string{"version"}, nil, &out, &errOut, BuildInfo{Revision: "abc", State: "clean"}); e != nil || !strings.Contains(out.String(), "sha=abc clean") {
		t.Fatalf("%v %s", e, out.String())
	}
	if e := Run(context.Background(), []string{"serve"}, nil, &out, &errOut, BuildInfo{}); e == nil {
		t.Fatal("started without required credentials")
	}
}
