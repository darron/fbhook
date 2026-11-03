// Package contract supplies behavioral tests shared by storage adapters.
package contract

import (
	"context"
	"errors"
	"fmt"
	"github.com/darron/fbhook/storage"
	"sync"
	"testing"
)

func Run(t *testing.T, s storage.Store) {
	t.Helper()
	ctx := context.Background()
	if _, e := s.Get(ctx, "missing"); !errors.Is(e, storage.ErrNotFound) {
		t.Fatalf("missing: %v", e)
	}
	body := []byte("initial bytes")
	v, e := s.Create(ctx, "a", body)
	if e != nil || v == "" {
		t.Fatalf("create: %s %v", v, e)
	}
	body[0] = 'X'
	o, e := s.Get(ctx, "a")
	if e != nil || string(o.Body) != "initial bytes" {
		t.Fatalf("read/copy: %+v %v", o, e)
	}
	if _, e = s.Create(ctx, "a", []byte("replacement")); !errors.Is(e, storage.ErrConflict) {
		t.Fatalf("duplicate: %v", e)
	}
	next, e := s.Replace(ctx, "a", []byte("new"), v)
	if e != nil || next == v {
		t.Fatalf("replace: %v", e)
	}
	if _, e = s.Replace(ctx, "a", []byte("stale"), v); !errors.Is(e, storage.ErrConflict) {
		t.Fatalf("stale: %v", e)
	}
	for i := 0; i < 5; i++ {
		if _, e = s.Create(ctx, fmt.Sprintf("page/%d", i), []byte("{}")); e != nil {
			t.Fatal(e)
		}
	}
	keys := map[string]bool{}
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		p, e := s.List(ctx, "page/", cursor, 2)
		if e != nil {
			t.Fatal(e)
		}
		for _, k := range p.Keys {
			if keys[k] {
				t.Fatalf("duplicate key %s", k)
			}
			keys[k] = true
		}
		cursor = p.Cursor
		if cursor == "" {
			break
		}
	}
	if len(keys) != 5 || cursor != "" {
		t.Fatalf("pagination: %v cursor %q", keys, cursor)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.Create(ctx, "race", []byte("{}"))
			mu.Lock()
			defer mu.Unlock()
			if e == nil {
				wins++
			} else if !errors.Is(e, storage.ErrConflict) {
				t.Errorf("race: %v", e)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d concurrent winners", wins)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = s.Get(canceled, "a"); e == nil {
		t.Fatal("ignored cancellation")
	}
}
