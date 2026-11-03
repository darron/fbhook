package fbhook

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientSubmitAndRedactedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Idempotency-Key") != "retry-one" {
			t.Error("missing key")
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"id":"receipt-one","status":"accepted"}`)
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL+"/api/v1/feedback/id.secret", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Submit(context.Background(), Feedback{Feedback: "hello"}, "retry-one")
	if err != nil || got.ID != "receipt-one" {
		t.Fatalf("%+v %v", got, err)
	}
	srv.Close()
	_, err = c.Submit(context.Background(), Feedback{Feedback: "hello"}, "retry-one")
	if err == nil || strings.Contains(err.Error(), "id.secret") {
		t.Fatalf("error leaked endpoint: %v", err)
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type lostReceiptBody struct{}

func (lostReceiptBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (lostReceiptBody) Close() error             { return nil }
func TestClientRetriesLostAcceptanceReceiptWithIdenticalRequest(t *testing.T) {
	calls := 0
	var original []byte
	transport := transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if r.Header.Get("Idempotency-Key") != "stable-event" {
			t.Fatal("changed key")
		}
		if calls == 1 {
			original = body
			return &http.Response{StatusCode: http.StatusAccepted, Body: lostReceiptBody{}, Header: make(http.Header)}, nil
		}
		if !bytes.Equal(body, original) {
			t.Fatal("changed bytes")
		}
		return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(`{"id":"same-receipt","status":"accepted"}`)), Header: make(http.Header)}, nil
	})
	c, err := NewClient("https://example.com/api/v1/feedback/id.secret", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := c.Submit(t.Context(), Feedback{Feedback: "retry receipt"}, "stable-event")
	if err != nil || receipt.ID != "same-receipt" || calls != 2 {
		t.Fatalf("receipt=%+v error=%v calls=%d", receipt, err, calls)
	}
}
