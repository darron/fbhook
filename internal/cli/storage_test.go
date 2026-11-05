package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStorageBackendRejectsUnknownBeforeCredentials(t *testing.T) {
	t.Setenv("FBHOOK_STORAGE_BACKEND", "not-a-backend")
	t.Setenv("BUCKET_NAME", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	_, _, err := storageFromEnv(context.Background())
	if err == nil || !strings.Contains(err.Error(), "FBHOOK_STORAGE_BACKEND must be tigris or gcs") {
		t.Fatalf("unsupported backend: %v", err)
	}
}

func TestDefaultStorageKeepsTigrisConfiguration(t *testing.T) {
	t.Setenv("FBHOOK_STORAGE_BACKEND", "")
	t.Setenv("BUCKET_NAME", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	_, _, err := storageFromEnv(context.Background())
	if err == nil || !strings.Contains(err.Error(), "tigris endpoint, bucket and credentials") {
		t.Fatalf("default no longer uses Tigris configuration: %v", err)
	}
}

func TestGCSSelectionRequiresBucketInsteadOfAWSKeys(t *testing.T) {
	t.Setenv("FBHOOK_STORAGE_BACKEND", "gcs")
	t.Setenv("BUCKET_NAME", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	_, _, err := storageFromEnv(context.Background())
	if err == nil || !strings.Contains(err.Error(), "gcs bucket") {
		t.Fatalf("GCS requires its own configuration: %v", err)
	}
}

// cancelOnStart stops the real service after the listener and worker are started.
// This avoids a timing sleep or a production authentication exception.
type cancelOnStart struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancelOnStart) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	if strings.Contains(w.String(), "service started") {
		w.cancel()
	}
	return n, err
}

func TestServeGCSStartsWithoutAWSKeysAndStopsOnCancellation(t *testing.T) {
	emulator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/o") {
			_, _ = fmt.Fprint(w, `{"items":[]}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error":{"code":404,"message":"not found"}}`)
	}))
	defer emulator.Close()
	t.Setenv("FBHOOK_STORAGE_BACKEND", "gcs")
	t.Setenv("BUCKET_NAME", "test-bucket")
	t.Setenv("STORAGE_EMULATOR_HOST", emulator.URL)
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("FBHOOK_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	t.Setenv("FBHOOK_SESSION_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 64)))
	t.Setenv("FBHOOK_BASE_URL", "https://example.test")
	t.Setenv("GITHUB_CLIENT_ID", "test-client")
	t.Setenv("GITHUB_CLIENT_SECRET", "test-secret")
	t.Setenv("FBHOOK_GITHUB_USERS", "test-admin")
	t.Setenv("FBHOOK_POLL_INTERVAL", "1s")
	t.Setenv("FBHOOK_TRUSTED_PROXY_CIDRS", "")
	t.Setenv("FBHOOK_CLIENT_IP_HEADER", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out := &cancelOnStart{cancel: cancel}
	var stderr bytes.Buffer
	if err := Run(ctx, []string{"serve", "--listen", "127.0.0.1:0"}, nil, out, &stderr, BuildInfo{}); err != nil {
		t.Fatalf("serve GCS: %v", err)
	}
	if !strings.Contains(out.String(), "service started") || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", out.String(), stderr.String())
	}
}
