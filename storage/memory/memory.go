// Package memory is a concurrency-safe in-memory store for tests and examples.
// It is deliberately not a persistent production storage adapter.
package memory

import (
	"bytes"
	"context"
	"github.com/darron/fbhook/storage"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type Store struct {
	mu      sync.Mutex
	objects map[string]storage.Object
	version uint64
}

func New() *Store { return &Store{objects: map[string]storage.Object{}} }
func (s *Store) Get(ctx context.Context, key string) (storage.Object, error) {
	if err := ctx.Err(); err != nil {
		return storage.Object{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[key]
	if !ok {
		return o, storage.ErrNotFound
	}
	o.Body = bytes.Clone(o.Body)
	return o, nil
}
func (s *Store) Create(ctx context.Context, key string, body []byte) (string, error) {
	return s.put(ctx, key, body, "", true)
}
func (s *Store) Replace(ctx context.Context, key string, body []byte, version string) (string, error) {
	return s.put(ctx, key, body, version, false)
}
func (s *Store) put(ctx context.Context, key string, body []byte, version string, create bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[key]
	if (create && ok) || (!create && (!ok || version == "" || o.Version != version)) {
		return "", storage.ErrConflict
	}
	s.version++
	v := strconv.FormatUint(s.version, 10)
	s.objects[key] = storage.Object{Body: bytes.Clone(body), Version: v}
	return v, nil
}
func (s *Store) List(ctx context.Context, prefix, cursor string, limit int) (storage.Page, error) {
	if err := ctx.Err(); err != nil {
		return storage.Page{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	keys := []string{}
	for key := range s.objects {
		if strings.HasPrefix(key, prefix) && key > cursor {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	p := storage.Page{Keys: keys}
	if len(keys) > limit {
		p.Keys = keys[:limit]
		p.Cursor = p.Keys[len(p.Keys)-1]
	}
	return p, nil
}
