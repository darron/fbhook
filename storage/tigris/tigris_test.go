package tigris

import (
	"context"
	"errors"
	"fmt"
	"github.com/darron/fbhook/storage"
	"github.com/darron/fbhook/storage/contract"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestConditionalWireProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PUT":
			if r.Header.Get("If-None-Match") != "*" && r.Header.Get("If-Match") != "\"v1\"" {
				t.Error("missing conditional write")
			}
			w.Header().Set("ETag", "\"v2\"")
		case "GET":
			if r.URL.Query().Get("list-type") == "2" {
				w.Header().Set("Content-Type", "application/xml")
				_, _ = fmt.Fprint(w, `<ListBucketResult><IsTruncated>true</IsTruncated><Contents><Key>test/a</Key></Contents><NextContinuationToken>next</NextContinuationToken></ListBucketResult>`)
			} else {
				w.WriteHeader(412)
				_, _ = fmt.Fprint(w, `<Error><Code>PreconditionFailed</Code></Error>`)
			}
		}
	}))
	defer server.Close()
	s, err := New(context.Background(), Config{Endpoint: server.URL, Bucket: "bucket", Prefix: "test/", AccessKeyID: "key", SecretAccessKey: "secret", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Create(context.Background(), "a", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Replace(context.Background(), "a", []byte("y"), "\"v1\""); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(context.Background(), "a"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("error mapping %v", err)
	}
	p, err := s.List(context.Background(), "", "", 1)
	if err != nil || len(p.Keys) != 1 || p.Keys[0] != "a" || p.Cursor != "next" {
		t.Fatalf("list %+v %v", p, err)
	}
}

func TestLiveContract(t *testing.T) {
	if os.Getenv("FBHOOK_TEST_TIGRIS") != "1" {
		t.Skip("opt-in: existing test bucket and disposable prefix required")
	}
	c := FromEnv()
	c.Prefix = fmt.Sprintf("fbhook-contract/%d/", time.Now().UnixNano())
	s, e := New(context.Background(), c)
	if e != nil {
		t.Fatal(e)
	}
	t.Log("test objects retained under", c.Prefix)
	contract.Run(t, s)
}
