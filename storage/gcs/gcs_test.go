package gcs

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/darron/fbhook/storage"
	"github.com/darron/fbhook/storage/contract"
	"google.golang.org/api/googleapi"
)

func TestFromEnv(t *testing.T) {
	t.Setenv("BUCKET_NAME", "bucket")
	t.Setenv("FBHOOK_STORAGE_PREFIX", "/pending//")
	c := FromEnv()
	if c.Bucket != "bucket" || c.Prefix != "/pending//" {
		t.Fatalf("FromEnv() = %+v", c)
	}
}

func TestNewRejectsEmptyBucket(t *testing.T) {
	if _, err := New(context.Background(), Config{}); err == nil {
		t.Fatal("New accepted an empty bucket")
	}
}

func TestStoreContractOverGCSWire(t *testing.T) {
	s, close := newFakeStore(t, "/pending//")
	defer close()
	contract.Run(t, s)
}

func TestGetRejectsBodyOverTwoMiB(t *testing.T) {
	s, close := newFakeStore(t, "")
	defer close()
	if _, err := s.Create(context.Background(), "large", make([]byte, 2*1024*1024+1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(context.Background(), "large"); err == nil || errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get() error = %v, want size-limit failure", err)
	}
}

func TestReplaceRejectsInvalidGenerationWithoutNetwork(t *testing.T) {
	s, close := newFakeStore(t, "")
	defer close()
	if _, err := s.Replace(context.Background(), "key", []byte("body"), "not-a-generation"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("Replace() error = %v, want ErrConflict", err)
	}
}

func TestCanceledRequestIsPreserved(t *testing.T) {
	s, close := newFakeStore(t, "")
	defer close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Get(ctx, "key"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get() error = %v, want context.Canceled", err)
	}
}

func TestListDoesNotSkipAfterShortServerPage(t *testing.T) {
	s, close := newFakeStoreWithShortFirstPage(t, "")
	defer close()
	for _, key := range []string{"page/a", "page/b", "page/c"} {
		if _, err := s.Create(context.Background(), key, []byte("{}")); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	cursor := ""
	for i := 0; i < 3; i++ {
		page, err := s.List(context.Background(), "page/", cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, page.Keys...)
		cursor = page.Cursor
		if cursor == "" {
			break
		}
	}
	if strings.Join(got, ",") != "page/a,page/b,page/c" || cursor != "" {
		t.Fatalf("List() pages = %q, cursor %q", got, cursor)
	}
}

func TestMapErrorRedactsProviderDetails(t *testing.T) {
	err := mapError(&googleapi.Error{Code: http.StatusInternalServerError, Message: "credential=secret object=private"})
	if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrConflict) {
		t.Fatalf("mapError() = %v, want generic error", err)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") {
		t.Fatalf("mapError() leaked provider detail: %q", err)
	}
}

func TestLiveContract(t *testing.T) {
	if os.Getenv("FBHOOK_TEST_GCS") != "1" {
		t.Skip("set FBHOOK_TEST_GCS=1 to run against GCS")
	}
	c := FromEnv()
	if c.Bucket == "" {
		t.Fatal("BUCKET_NAME is required for live GCS contract test")
	}
	c.Prefix = "fbhook-contract/" + strconv.FormatInt(time.Now().UnixNano(), 10)
	s, err := New(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	contract.Run(t, s)
}

type fakeGCS struct {
	mu             sync.Mutex
	objects        map[string]fakeObject
	nextGen        int64
	shortFirstPage bool
}

type fakeObject struct {
	body []byte
	gen  int64
}

func newFakeStore(t *testing.T, prefix string) (*Store, func()) {
	return newFakeStoreForPagination(t, prefix, false)
}

func newFakeStoreWithShortFirstPage(t *testing.T, prefix string) (*Store, func()) {
	return newFakeStoreForPagination(t, prefix, true)
}

func newFakeStoreForPagination(t *testing.T, prefix string, shortFirstPage bool) (*Store, func()) {
	t.Helper()
	fake := &fakeGCS{objects: map[string]fakeObject{}, nextGen: 1, shortFirstPage: shortFirstPage}
	server := httptest.NewServer(http.HandlerFunc(fake.serveHTTP))
	t.Setenv("STORAGE_EMULATOR_HOST", server.URL)
	s, err := New(context.Background(), Config{Bucket: "bucket", Prefix: prefix})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return s, func() { _ = s.Close(); server.Close() }
}

func (f *fakeGCS) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Context().Err() != nil {
		return
	}
	path, err := url.PathUnescape(r.URL.Path)
	if err != nil {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	const objectPath = "/storage/v1/b/bucket/o/"
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(path, objectPath) && r.URL.Query().Get("alt") == "media":
		f.get(w, strings.TrimPrefix(path, objectPath))
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/upload/storage/v1/b/bucket/o"):
		f.put(w, r)
	case r.Method == http.MethodGet && path == "/storage/v1/b/bucket/o":
		f.list(w, r)
	default:
		http.Error(w, "unexpected request "+r.Method+" "+r.URL.String(), http.StatusNotFound)
	}
}

func (f *fakeGCS) get(w http.ResponseWriter, key string) {
	f.mu.Lock()
	o, ok := f.objects[key]
	f.mu.Unlock()
	if !ok {
		writeGCSError(w, http.StatusNotFound, "notFound")
		return
	}
	w.Header().Set("X-Goog-Generation", strconv.FormatInt(o.gen, 10))
	w.Header().Set("Content-Length", strconv.Itoa(len(o.body)))
	_, _ = w.Write(o.body)
}

func (f *fakeGCS) put(w http.ResponseWriter, r *http.Request) {
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		writeGCSError(w, http.StatusBadRequest, "invalid")
		return
	}
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		writeGCSError(w, http.StatusBadRequest, "invalid")
		return
	}
	var metadata struct {
		Name string `json:"name"`
	}
	body := rawBody
	if strings.HasPrefix(mediaType, "multipart/") {
		mr := multipart.NewReader(strings.NewReader(string(rawBody)), params["boundary"])
		metadataPart, err := mr.NextPart()
		if err != nil {
			writeGCSError(w, http.StatusBadRequest, "invalid")
			return
		}
		metadataBody, err := io.ReadAll(metadataPart)
		if err != nil || json.Unmarshal(metadataBody, &metadata) != nil {
			writeGCSError(w, http.StatusBadRequest, "invalid")
			return
		}
		p, err := mr.NextPart()
		if err != nil {
			writeGCSError(w, http.StatusBadRequest, "invalid")
			return
		}
		body, err = io.ReadAll(p)
		if err != nil {
			writeGCSError(w, http.StatusBadRequest, "invalid")
			return
		}
	} else {
		metadata.Name = r.URL.Query().Get("name")
	}
	if metadata.Name == "" {
		writeGCSError(w, http.StatusBadRequest, "invalid")
		return
	}
	wantGeneration := r.URL.Query().Get("ifGenerationMatch")
	f.mu.Lock()
	old, exists := f.objects[metadata.Name]
	if wantGeneration == "0" && exists {
		f.mu.Unlock()
		writeGCSError(w, http.StatusPreconditionFailed, "conditionNotMet")
		return
	}
	if wantGeneration != "" && wantGeneration != "0" && (!exists || wantGeneration != strconv.FormatInt(old.gen, 10)) {
		f.mu.Unlock()
		writeGCSError(w, http.StatusPreconditionFailed, "conditionNotMet")
		return
	}
	gen := f.nextGen
	f.nextGen++
	f.objects[metadata.Name] = fakeObject{body: body, gen: gen}
	f.mu.Unlock()
	crc := crc32.Checksum(body, crc32.MakeTable(crc32.Castagnoli))
	checksum := make([]byte, 4)
	binary.BigEndian.PutUint32(checksum, crc)
	_ = json.NewEncoder(w).Encode(map[string]string{"name": metadata.Name, "generation": strconv.FormatInt(gen, 10), "crc32c": base64.StdEncoding.EncodeToString(checksum)})
}

func (f *fakeGCS) list(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	limit, _ := strconv.Atoi(r.URL.Query().Get("maxResults"))
	if limit < 1 {
		limit = 100
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("pageToken"))
	if f.shortFirstPage && offset == 0 {
		limit = 1
	}
	f.mu.Lock()
	keys := make([]string, 0, len(f.objects))
	for key := range f.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	f.mu.Unlock()
	sort.Strings(keys)
	end := offset + limit
	if end > len(keys) {
		end = len(keys)
	}
	items := make([]map[string]string, 0, end-offset)
	for _, key := range keys[offset:end] {
		items = append(items, map[string]string{"name": key})
	}
	response := map[string]any{"items": items}
	if end < len(keys) {
		response["nextPageToken"] = strconv.Itoa(end)
	}
	_ = json.NewEncoder(w).Encode(response)
}

func writeGCSError(w http.ResponseWriter, status int, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": status, "message": "storage error", "errors": []map[string]string{{"reason": reason}}}})
}
