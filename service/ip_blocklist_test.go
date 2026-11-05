package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/darron/fbhook/storage"
	"github.com/darron/fbhook/storage/memory"
)

func newBlocklistService(t *testing.T, store storage.Store, now *time.Time) *Service {
	t.Helper()
	svc, err := New(store, Options{
		EncryptionKey: []byte(strings.Repeat("b", 32)),
		Clock:         func() time.Time { return *now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestIPBlocklistSaveCanonicalizesAndUsesVersions(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	svc := newBlocklistService(t, memory.New(), &now)

	got, version, err := svc.SaveIPBlocklist(t.Context(), []IPRule{
		{CIDR: "192.0.2.9", Note: "single host"},
		{CIDR: "::ffff:198.51.100.0/120"},
		{CIDR: "2001:db8::/48"},
	}, "", "darron (123)")
	if err != nil {
		t.Fatal(err)
	}
	if version == "" || got.Schema != 1 || len(got.Rules) != 3 {
		t.Fatalf("saved blocklist = %+v version=%q", got, version)
	}
	want := []string{"192.0.2.9/32", "198.51.100.0/24", "2001:db8::/48"}
	for i := range want {
		if got.Rules[i].CIDR != want[i] {
			t.Fatalf("rule %d = %q, want %q", i, got.Rules[i].CIDR, want[i])
		}
	}
	if got.UpdatedAt != now.Format(time.RFC3339Nano) || got.UpdatedBy != "darron (123)" {
		t.Fatalf("audit fields = %s %q", got.UpdatedAt, got.UpdatedBy)
	}

	if _, _, err = svc.SaveIPBlocklist(t.Context(), nil, "stale", "darron (123)"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("stale save error = %v", err)
	}
	empty, next, err := svc.SaveIPBlocklist(t.Context(), []IPRule{}, version, "darron (123)")
	if err != nil || next == version || empty.Rules == nil || len(empty.Rules) != 0 {
		t.Fatalf("empty replacement = %+v version=%q err=%v", empty, next, err)
	}
}

func TestIPBlocklistRejectsAmbiguousOrInvalidRules(t *testing.T) {
	now := time.Now()
	svc := newBlocklistService(t, memory.New(), &now)
	cases := []struct {
		name  string
		rules []IPRule
	}{
		{"host bits", []IPRule{{CIDR: "192.0.2.9/24"}}},
		{"duplicate", []IPRule{{CIDR: "192.0.2.1"}, {CIDR: "::ffff:192.0.2.1"}}},
		{"mapped supernet", []IPRule{{CIDR: "::ffff:0:0/80"}}},
		{"zoned IPv6", []IPRule{{CIDR: "fe80::1%en0"}}},
		{"invalid UTF-8 CIDR", []IPRule{{CIDR: string([]byte{0xff})}}},
		{"invalid UTF-8 note", []IPRule{{CIDR: "192.0.2.1", Note: string([]byte{0xff})}}},
		{"long note", []IPRule{{CIDR: "192.0.2.1", Note: strings.Repeat("x", 201)}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := svc.SaveIPBlocklist(t.Context(), tc.rules, "", "actor"); ErrorStatus(err) != 400 {
				t.Fatalf("SaveIPBlocklist error = %v (status %d)", err, ErrorStatus(err))
			}
		})
	}

	many := make([]IPRule, 1025)
	for i := range many {
		many[i].CIDR = "192.0.2.1"
	}
	if _, _, err := svc.SaveIPBlocklist(t.Context(), many, "", "actor"); ErrorStatus(err) != 400 {
		t.Fatalf("too many rules error = %v", err)
	}

	largeSvc := newBlocklistService(t, memory.New(), &now)
	large := make([]IPRule, 300)
	for i := range large {
		large[i] = IPRule{CIDR: fmt.Sprintf("2001:db8::%x", i), Note: strings.Repeat("n", 200)}
	}
	if _, _, err := largeSvc.SaveIPBlocklist(t.Context(), large, "", "actor"); ErrorStatus(err) != 400 {
		t.Fatalf("oversized encoded policy error = %v", err)
	}
}

func TestIPBlocklistRejectsMalformedStoredConfig(t *testing.T) {
	now := time.Now()
	for _, body := range [][]byte{
		[]byte(`{"schema":1,"rules":[],"extra":true}`),
		[]byte(`{"schema":2,"rules":[]}`),
		[]byte(`{"schema":1,"rules":[{"cidr":"192.0.2.1/32","extra":true}]}`),
		[]byte(`{"schema":1,"rules":null}`),
		[]byte(`{"schema":1,"schema":1,"rules":[]}`),
		[]byte(`{"schema":1,"rules":[{"cidr":"192.0.2.1/32","cidr":"192.0.2.2/32"}]}`),
	} {
		store := memory.New()
		if _, err := store.Create(t.Context(), ipBlocklistKey, body); err != nil {
			t.Fatal(err)
		}
		svc := newBlocklistService(t, store, &now)
		if _, _, err := svc.GetIPBlocklist(t.Context()); err == nil {
			t.Fatalf("accepted malformed config %s", body)
		}
	}
}

type blocklistFaultStore struct {
	storage.Store
	mu      sync.Mutex
	getErr  error
	getHits int
}

func (s *blocklistFaultStore) Get(ctx context.Context, key string) (storage.Object, error) {
	if key == ipBlocklistKey {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.getHits++
		if s.getErr != nil {
			return storage.Object{}, s.getErr
		}
	}
	return s.Store.Get(ctx, key)
}

func TestIPBlockedRefreshesExternalUpdatesAndCachesFailures(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	base := memory.New()
	store := &blocklistFaultStore{Store: base}
	svc := newBlocklistService(t, store, &now)
	addr := netip.MustParseAddr("192.0.2.44")

	blocked, err := svc.IPBlocked(t.Context(), addr)
	if err != nil || blocked {
		t.Fatalf("missing config = %v %v", blocked, err)
	}
	if store.getHits != 1 {
		t.Fatalf("initial reads = %d", store.getHits)
	}

	body := []byte(`{"schema":1,"rules":[{"cidr":"192.0.2.0/24"}]}`)
	if _, err = base.Create(t.Context(), ipBlocklistKey, body); err != nil {
		t.Fatal(err)
	}
	blocked, err = svc.IPBlocked(t.Context(), addr)
	if err != nil || blocked || store.getHits != 1 {
		t.Fatalf("cache did not hold for TTL: blocked=%v err=%v reads=%d", blocked, err, store.getHits)
	}
	now = now.Add(5 * time.Second)
	blocked, err = svc.IPBlocked(t.Context(), addr)
	if err != nil || !blocked || store.getHits != 2 {
		t.Fatalf("external update not loaded: blocked=%v err=%v reads=%d", blocked, err, store.getHits)
	}

	store.mu.Lock()
	store.getErr = errors.New("bucket unavailable")
	store.mu.Unlock()
	now = now.Add(5 * time.Second)
	if _, err = svc.IPBlocked(t.Context(), addr); err == nil {
		t.Fatal("refresh failure did not fail closed")
	}
	if _, err = svc.IPBlocked(t.Context(), addr); err == nil || store.getHits != 3 {
		t.Fatalf("failure was not cached: err=%v reads=%d", err, store.getHits)
	}
	store.mu.Lock()
	store.getErr = nil
	store.mu.Unlock()
	now = now.Add(5 * time.Second)
	blocked, err = svc.IPBlocked(t.Context(), addr)
	if err != nil || !blocked || store.getHits != 4 {
		t.Fatalf("valid policy was lost after recovery: blocked=%v err=%v reads=%d", blocked, err, store.getHits)
	}
}

func TestSaveIPBlocklistUpdatesCacheImmediately(t *testing.T) {
	now := time.Now().UTC()
	store := &blocklistFaultStore{Store: memory.New()}
	svc := newBlocklistService(t, store, &now)
	if _, _, err := svc.SaveIPBlocklist(t.Context(), []IPRule{{CIDR: "2001:db8::/32"}}, "", "actor"); err != nil {
		t.Fatal(err)
	}
	blocked, err := svc.IPBlocked(t.Context(), netip.MustParseAddr("2001:db8::1"))
	if err != nil || !blocked || store.getHits != 0 {
		t.Fatalf("save cache = blocked=%v err=%v reads=%d", blocked, err, store.getHits)
	}
}

func TestIPBlockedLoadsPersistedPolicyAfterRestart(t *testing.T) {
	now := time.Now().UTC()
	store := memory.New()
	first := newBlocklistService(t, store, &now)
	if _, _, err := first.SaveIPBlocklist(t.Context(), []IPRule{{CIDR: "198.51.100.0/24"}}, "", "actor"); err != nil {
		t.Fatal(err)
	}
	restarted := newBlocklistService(t, store, &now)
	blocked, err := restarted.IPBlocked(t.Context(), netip.MustParseAddr("198.51.100.9"))
	if err != nil || !blocked {
		t.Fatalf("restarted service blocked=%v err=%v", blocked, err)
	}
}

func FuzzParseIPPrefix(f *testing.F) {
	for _, seed := range []string{"192.0.2.1", "192.0.2.0/24", "2001:db8::/32", "::ffff:192.0.2.0/120", "bad", "fe80::1%en0"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		prefix, err := parseIPPrefix(value)
		if err != nil {
			return
		}
		if !prefix.IsValid() || prefix != prefix.Masked() || prefix.Addr().Zone() != "" || prefix.Addr().Is4In6() {
			t.Fatalf("accepted non-canonical prefix %q as %v", value, prefix)
		}
		roundTrip, err := netip.ParsePrefix(prefix.String())
		if err != nil || roundTrip != prefix {
			t.Fatalf("prefix %v does not round trip: %v %v", prefix, roundTrip, err)
		}
	})
}

func TestAcceptFromStoresClientIPOutsideProviderPayload(t *testing.T) {
	svc, _, endpointID, credential := setup(t, memory.New())
	client := netip.MustParseAddr("::ffff:192.0.2.44")
	receipt, err := svc.AcceptFrom(t.Context(), credential, client, func() ([]byte, error) {
		return []byte(`{"feedback":"private feedback"}`), nil
	}, "client-ip", nil)
	if err != nil {
		t.Fatal(err)
	}
	sub, _, err := svc.GetSubmission(t.Context(), endpointID, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sub.ClientIP != "192.0.2.44" {
		t.Fatalf("client IP = %q", sub.ClientIP)
	}
	if strings.Contains(sub.Issue.Body, sub.ClientIP) {
		t.Fatal("client IP leaked into provider issue body")
	}
}

type delayedCreateStore struct {
	storage.Store
	created chan struct{}
	release chan struct{}
}

func (s *delayedCreateStore) Create(ctx context.Context, key string, body []byte) (string, error) {
	version, err := s.Store.Create(ctx, key, body)
	if key == ipBlocklistKey && err == nil {
		close(s.created)
		<-s.release
	}
	return version, err
}

func TestConcurrentSavesCannotLeaveStaleCache(t *testing.T) {
	now := time.Now().UTC()
	base := memory.New()
	store := &delayedCreateStore{Store: base, created: make(chan struct{}), release: make(chan struct{})}
	svc := newBlocklistService(t, store, &now)
	firstDone := make(chan error, 1)
	go func() {
		_, _, err := svc.SaveIPBlocklist(t.Context(), []IPRule{{CIDR: "192.0.2.1"}}, "", "first")
		firstDone <- err
	}()
	<-store.created
	_, version, err := svc.GetIPBlocklist(t.Context())
	if err != nil || version == "" {
		t.Fatalf("read committed first policy: version=%q err=%v", version, err)
	}
	secondDone := make(chan error, 1)
	go func() {
		_, _, err := svc.SaveIPBlocklist(t.Context(), []IPRule{{CIDR: "198.51.100.1"}}, version, "second")
		secondDone <- err
	}()
	close(store.release)
	if err = <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err = <-secondDone; err != nil {
		t.Fatal(err)
	}
	blocked, err := svc.IPBlocked(t.Context(), netip.MustParseAddr("198.51.100.1"))
	if err != nil || !blocked {
		t.Fatalf("newest policy absent from cache: blocked=%v err=%v", blocked, err)
	}
	blocked, err = svc.IPBlocked(t.Context(), netip.MustParseAddr("192.0.2.1"))
	if err != nil || blocked {
		t.Fatalf("stale policy remained in cache: blocked=%v err=%v", blocked, err)
	}
}
