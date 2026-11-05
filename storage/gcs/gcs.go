// Package gcs implements the storage contract with Google Cloud Storage.
package gcs

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	cloudstorage "cloud.google.com/go/storage"
	"github.com/darron/fbhook/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
)

type Config struct {
	Bucket string
	Prefix string
}

func FromEnv() Config {
	return Config{Bucket: os.Getenv("BUCKET_NAME"), Prefix: os.Getenv("FBHOOK_STORAGE_PREFIX")}
}

type Store struct {
	client         *cloudstorage.Client
	bucket, prefix string
}

func New(ctx context.Context, c Config) (*Store, error) {
	if c.Bucket == "" {
		return nil, errors.New("gcs bucket is required")
	}
	client, err := cloudstorage.NewClient(ctx, cloudstorage.WithJSONReads())
	if err != nil {
		return nil, errors.New("initialize gcs client")
	}
	prefix := strings.Trim(c.Prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	return &Store{client: client, bucket: c.Bucket, prefix: prefix}, nil
}

func (s *Store) Close() error { return s.client.Close() }

func (s *Store) Get(ctx context.Context, key string) (storage.Object, error) {
	ctx, cancel := boundedContext(ctx)
	defer cancel()
	r, err := s.client.Bucket(s.bucket).Object(s.objectName(key)).NewReader(ctx)
	if err != nil {
		return storage.Object{}, mapError(err)
	}
	defer func() { _ = r.Close() }()
	if r.Attrs.Size > maxObjectBytes {
		return storage.Object{}, errors.New("gcs object read failed")
	}
	body, err := io.ReadAll(io.LimitReader(r, maxObjectBytes+1))
	if err != nil {
		return storage.Object{}, mapError(err)
	}
	if len(body) > maxObjectBytes || r.Attrs.Generation <= 0 {
		return storage.Object{}, errors.New("gcs object read failed")
	}
	return storage.Object{Body: body, Version: strconv.FormatInt(r.Attrs.Generation, 10)}, nil
}

func (s *Store) Create(ctx context.Context, key string, body []byte) (string, error) {
	ctx, cancel := boundedContext(ctx)
	defer cancel()
	return s.write(ctx, key, body, cloudstorage.Conditions{DoesNotExist: true})
}

func (s *Store) Replace(ctx context.Context, key string, body []byte, version string) (string, error) {
	generation, err := strconv.ParseInt(version, 10, 64)
	if err != nil || generation <= 0 {
		return "", storage.ErrConflict
	}
	ctx, cancel := boundedContext(ctx)
	defer cancel()
	return s.write(ctx, key, body, cloudstorage.Conditions{GenerationMatch: generation})
}

func (s *Store) write(ctx context.Context, key string, body []byte, conditions cloudstorage.Conditions) (string, error) {
	w := s.client.Bucket(s.bucket).Object(s.objectName(key)).If(conditions).NewWriter(ctx)
	w.ChunkSize = 0
	w.ContentType = "application/json"
	w.CacheControl = "no-store"
	if _, err := w.Write(body); err != nil {
		return "", mapError(err)
	}
	if err := w.Close(); err != nil {
		return "", mapError(err)
	}
	if w.Attrs().Generation <= 0 {
		return "", errors.New("gcs write returned no generation")
	}
	return strconv.FormatInt(w.Attrs().Generation, 10), nil
}

func (s *Store) List(ctx context.Context, prefix, cursor string, limit int) (storage.Page, error) {
	ctx, cancel := boundedContext(ctx)
	defer cancel()
	if limit <= 0 || limit > maxPageSize {
		limit = defaultPageSize
	}
	it := s.client.Bucket(s.bucket).Objects(ctx, &cloudstorage.Query{Prefix: s.objectName(prefix)})
	pager := iterator.NewPager(it, limit, cursor)
	var objects []*cloudstorage.ObjectAttrs
	next, err := pager.NextPage(&objects)
	if err != nil {
		return storage.Page{}, mapError(err)
	}
	page := storage.Page{Keys: []string{}}
	for _, attrs := range objects {
		if strings.HasPrefix(attrs.Name, s.prefix) {
			page.Keys = append(page.Keys, strings.TrimPrefix(attrs.Name, s.prefix))
		}
	}
	if next == cursor && next != "" {
		return storage.Page{}, errors.New("invalid gcs pagination")
	}
	page.Cursor = next
	return page, nil
}

func (s *Store) objectName(key string) string { return s.prefix + key }

func boundedContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, operationTimeout)
}

func mapError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, cloudstorage.ErrObjectNotExist) {
		return storage.ErrNotFound
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case 404:
			return storage.ErrNotFound
		case 409, 412:
			return storage.ErrConflict
		}
	}
	return errors.New("gcs object storage operation failed")
}

const (
	maxObjectBytes   = 2 * 1024 * 1024
	maxPageSize      = 1000
	defaultPageSize  = 100
	operationTimeout = 20 * time.Second
)

var _ storage.Store = (*Store)(nil)
