package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/darron/fbhook/storage"
	"github.com/darron/fbhook/storage/memory"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixtureProvider struct {
	mu      sync.Mutex
	calls   int
	tokens  []string
	targets []string
	fail    error
}

func (p *fixtureProvider) Validate(context.Context, Destination, string) error { return nil }
func (p *fixtureProvider) Create(_ context.Context, d Destination, token string, _ Issue) (IssueRef, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.tokens = append(p.tokens, token)
	p.targets = append(p.targets, d.Target)
	if p.fail != nil {
		return IssueRef{}, p.fail
	}
	return IssueRef{ID: "42", URL: "https://github.com/example/repo/issues/42"}, nil
}
func (p *fixtureProvider) Find(_ context.Context, _ Destination, _ string, _ Issue, _ string) (IssueRef, error) {
	return IssueRef{}, errors.New("not confirmed")
}

func setup(t *testing.T, store storage.Store) (*Service, *fixtureProvider, string, string) {
	t.Helper()
	p := &fixtureProvider{}
	s, e := New(store, Options{EncryptionKey: []byte("01234567890123456789012345678901"), Providers: map[string]Provider{"github": p}})
	if e != nil {
		t.Fatal(e)
	}
	d, e := s.SaveDestination(context.Background(), DestinationInput{Name: "Test", Provider: "github", Target: "example/repo", Token: "sensitive-token"})
	if e != nil {
		t.Fatal(e)
	}
	ep, url, e := s.CreateEndpoint(context.Background(), "Shared feedback", d.ID, nil)
	if e != nil {
		t.Fatal(e)
	}
	return s, p, ep.ID, url
}

func TestDurableAcceptanceIdempotencyAndSharedEndpoint(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	s, p, ep, secret := setup(t, store)
	a, e := s.Accept(ctx, secret, []byte(`{"feedback":"one","source":{"app":"archive"}}`), "once")
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.Accept(ctx, secret, []byte(`{"feedback":"one","source":{"app":"archive"}}`), "once")
	if e != nil || a.ID != b.ID {
		t.Fatalf("retry %+v %v", b, e)
	}
	if _, e = s.Accept(ctx, secret, []byte(`{"feedback":"different"}`), "once"); e == nil {
		t.Fatal("accepted conflicting content")
	}
	if _, e = s.Accept(ctx, secret, []byte(`{"feedback":"two","source":{"app":"converge"}}`), "twice"); e != nil {
		t.Fatal(e)
	}
	if p.calls != 0 {
		t.Fatal("delivered before worker")
	}
	if e = s.Scan(ctx, false); e != nil {
		t.Fatal(e)
	}
	if p.calls != 2 {
		t.Fatalf("deliveries %d", p.calls)
	}
	sub, _, e := s.GetSubmission(ctx, ep, a.ID)
	if e != nil || sub.State != "delivered" {
		t.Fatalf("state %+v %v", sub, e)
	}
	// A new process discovers the same stored state without delivering it again.
	next, e := New(store, s.options)
	if e != nil {
		t.Fatal(e)
	}
	if e = next.Scan(ctx, true); e != nil {
		t.Fatal(e)
	}
	if p.calls != 2 {
		t.Fatal("recreated delivered issues")
	}
}

func TestRotationRevocationAndCredentialSnapshot(t *testing.T) {
	ctx := context.Background()
	s, p, ep, secret := setup(t, memory.New())
	r, e := s.Accept(ctx, secret, []byte(`{"feedback":"captured"}`), "k")
	if e != nil {
		t.Fatal(e)
	}
	endpoint, _, _ := s.GetEndpoint(ctx, ep)
	old, _, _ := s.GetDestination(ctx, endpoint.DestinationID)
	if _, e = s.SaveDestination(ctx, DestinationInput{ID: old.ID, Name: old.Name, Provider: old.Provider, Target: "example/newrepo", Token: "rotated-token"}); e != nil {
		t.Fatal(e)
	}
	fresh, e := s.RotateEndpoint(ctx, ep)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Accept(ctx, secret, []byte(`{"feedback":"captured"}`), "k"); e == nil {
		t.Fatal("old credential accepted")
	}
	if _, e = s.Accept(ctx, fresh, []byte(`{"feedback":"new target"}`), "new"); e != nil {
		t.Fatal(e)
	}
	if e = s.Scan(ctx, false); e != nil {
		t.Fatal(e)
	}
	if p.calls != 2 || p.tokens[0] != "rotated-token" {
		t.Fatalf("credential rotation: %+v", p.tokens)
	}
	sub, _, _ := s.GetSubmission(ctx, ep, r.ID)
	if sub.Destination.Target != "example/repo" {
		t.Fatal("rerouted existing feedback")
	}
	foundOld := false
	for _, v := range p.targets {
		if v == "example/repo" {
			foundOld = true
		}
	}
	if !foundOld {
		t.Fatal("captured target not used")
	}
}

type faultStore struct {
	storage.Store
	failDelivered bool
}

func (s *faultStore) Replace(ctx context.Context, key string, b []byte, v string) (string, error) {
	if s.failDelivered && strings.Contains(string(b), `"state":"delivered"`) {
		return "", errors.New("write unavailable")
	}
	return s.Store.Replace(ctx, key, b, v)
}
func TestLostSuccessStateNeverBlindlyRecreates(t *testing.T) {
	ctx := context.Background()
	store := &faultStore{Store: memory.New()}
	s, p, ep, secret := setup(t, store)
	r, e := s.Accept(ctx, secret, []byte(`{"feedback":"hello"}`), "key")
	if e != nil {
		t.Fatal(e)
	}
	store.failDelivered = true
	_ = s.Scan(ctx, false)
	if p.calls != 1 {
		t.Fatal(p.calls)
	}
	store.failDelivered = false
	restarted, e := New(store, s.options)
	if e != nil {
		t.Fatal(e)
	}
	if e = restarted.Scan(ctx, true); e != nil {
		t.Fatal(e)
	}
	sub, _, _ := restarted.GetSubmission(ctx, ep, r.ID)
	if sub.State != "delivery_uncertain" || p.calls != 1 {
		t.Fatalf("duplicate or lost uncertainty: %s calls=%d", sub.State, p.calls)
	}
}

func TestRetryAndAdminResolution(t *testing.T) {
	ctx := context.Background()
	s, p, ep, secret := setup(t, memory.New())
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	s.options.Clock = func() time.Time { return now }
	p.fail = &ProviderError{Kind: "rate_limited", Retryable: true, RetryAfter: time.Minute}
	r, _ := s.Accept(ctx, secret, []byte(`{"feedback":"retry"}`), "key")
	if e := s.Scan(ctx, false); e != nil {
		t.Fatal(e)
	}
	sub, _, _ := s.GetSubmission(ctx, ep, r.ID)
	if sub.State != "retry_scheduled" || sub.NextAttempt.Before(now.Add(time.Minute)) {
		t.Fatalf("retry %+v", sub)
	}
	if e := s.Scan(ctx, false); e != nil {
		t.Fatal(e)
	}
	if p.calls != 1 {
		t.Fatal("retried too soon")
	}
	now = now.Add(2 * time.Minute)
	p.fail = &ProviderError{Kind: "timeout", Uncertain: true}
	_ = s.Scan(ctx, false)
	sub, _, _ = s.GetSubmission(ctx, ep, r.ID)
	if sub.State != "delivery_uncertain" {
		t.Fatal(sub.State)
	}
	if e := s.Resolve(ctx, ep, r.ID, Resolution{Action: "retry", Actor: "123", Reason: "checked"}); e == nil {
		t.Fatal("uncertain retry lacked acknowledgement")
	}
	if e := s.Resolve(ctx, ep, r.ID, Resolution{Action: "abandon", Actor: "123", Reason: "duplicate upstream"}); e != nil {
		t.Fatal(e)
	}
	sub, _, _ = s.GetSubmission(ctx, ep, r.ID)
	if sub.State != "failed" || sub.History[len(sub.History)-1].Actor != "123" {
		t.Fatalf("audit %+v", sub)
	}
}

type unavailableListStore struct{ storage.Store }

func (s unavailableListStore) List(context.Context, string, string, int) (storage.Page, error) {
	return storage.Page{}, errors.New("unavailable")
}
func TestWorkerRecordsListingFailure(t *testing.T) {
	s, _, _, _ := setup(t, memory.New())
	s.store = unavailableListStore{s.store}
	if s.Scan(t.Context(), false) == nil || s.Status().LastError == "" {
		t.Fatal("listing failure was not visible in worker status")
	}
}

type unavailableCreateStore struct{ storage.Store }

func (s unavailableCreateStore) Create(ctx context.Context, key string, body []byte) (string, error) {
	if strings.HasPrefix(key, "submissions/") {
		return "", errors.New("write unavailable")
	}
	return s.Store.Create(ctx, key, body)
}
func TestAcceptanceFailsWithoutDurableObject(t *testing.T) {
	s, p, _, secret := setup(t, memory.New())
	s.store = unavailableCreateStore{s.store}
	if _, err := s.Accept(t.Context(), secret, []byte(`{"feedback":"must persist first"}`), "same"); err == nil {
		t.Fatal("accepted unavailable storage")
	}
	if p.calls != 0 {
		t.Fatal("provider called without persistence")
	}
}
func TestPaginatedWorkerAndConcurrentIdempotency(t *testing.T) {
	store := memory.New()
	s, p, _, secret := setup(t, store)
	ctx := t.Context()
	var group sync.WaitGroup
	ids := make(chan string, 20)
	for i := 0; i < 20; i++ {
		group.Go(func() {
			r, err := s.Accept(ctx, secret, []byte(`{"feedback":"same event"}`), "shared")
			if err != nil {
				t.Error(err)
			}
			ids <- r.ID
		})
	}
	group.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("multiple receipts")
		}
	}
	for i := 0; i < 105; i++ {
		if _, err := s.Accept(ctx, secret, []byte(`{"feedback":"pagination"}`), fmt.Sprintf("event-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	group.Go(func() {
		if err := s.Scan(ctx, false); err != nil {
			t.Error(err)
		}
	})
	group.Go(func() {
		if err := s.Scan(ctx, false); err != nil {
			t.Error(err)
		}
	})
	group.Wait()
	if p.calls != 106 {
		t.Fatalf("delivery count %d", p.calls)
	}
	page, err := store.List(ctx, "credentials/", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range page.Keys {
		object, err := store.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(object.Body), "sensitive-token") {
			t.Fatal("plaintext credential persisted")
		}
	}
}

func TestRunningWorkerRecoversFailedSuccessPersistence(t *testing.T) {
	store := &faultStore{Store: memory.New(), failDelivered: true}
	s, p, ep, secret := setup(t, store)
	s.options.PollInterval = 2 * time.Millisecond
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { _ = s.Run(ctx); close(done) }()
	for s.Status().LastScan.IsZero() {
		select {
		case <-ctx.Done():
			t.Fatal("worker did not scan")
		case <-time.After(time.Millisecond):
		}
	}
	receipt, err := s.Accept(t.Context(), secret, []byte(`{"feedback":"recover in same process"}`), "one")
	if err != nil {
		t.Fatal(err)
	}
	<-done
	sub, _, err := s.GetSubmission(t.Context(), ep, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sub.State != "delivery_uncertain" || p.calls != 1 {
		t.Fatalf("state=%s calls=%d", sub.State, p.calls)
	}
}

type missingDefaultsProvider struct {
	fixtureProvider
	fixed bool
}

func (p *missingDefaultsProvider) Create(context.Context, Destination, string, Issue) (IssueRef, error) {
	return IssueRef{ID: "42", URL: "https://github.com/example/repo/issues/42"}, &ProviderError{Kind: "defaults_not_applied"}
}
func (p *missingDefaultsProvider) Find(context.Context, Destination, string, Issue, string) (IssueRef, error) {
	if !p.fixed {
		return IssueRef{}, errors.New("labels absent")
	}
	return IssueRef{ID: "42", URL: "https://github.com/example/repo/issues/42"}, nil
}
func TestCreatedIssueMissingDefaultsCannotBeRecreated(t *testing.T) {
	s, _, ep, secret := setup(t, memory.New())
	provider := &missingDefaultsProvider{}
	s.options.Providers["github"] = provider
	receipt, err := s.Accept(t.Context(), secret, []byte(`{"feedback":"capture partial success"}`), "one")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Scan(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	sub, _, err := s.GetSubmission(t.Context(), ep, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sub.State != "failed" || sub.Reference == nil || sub.Reference.ID != "42" {
		t.Fatalf("lost created issue: %+v", sub)
	}
	if err = s.Resolve(t.Context(), ep, receipt.ID, Resolution{Action: "retry", Actor: "123", Reason: "retry"}); err == nil {
		t.Fatal("retry would duplicate an existing issue")
	}
	provider.fixed = true
	if err = s.Resolve(t.Context(), ep, receipt.ID, Resolution{Action: "link", Reference: "42", Actor: "123", Reason: "labels corrected upstream"}); err != nil {
		t.Fatal(err)
	}
	sub, _, _ = s.GetSubmission(t.Context(), ep, receipt.ID)
	if sub.State != "delivered" {
		t.Fatal(sub.State)
	}
}

type reconcileProvider struct {
	fixtureProvider
	finds     int
	deadline  bool
	confirmed bool
}

func (p *reconcileProvider) Find(ctx context.Context, _ Destination, _ string, _ Issue, _ string) (IssueRef, error) {
	p.finds++
	_, p.deadline = ctx.Deadline()
	if p.confirmed {
		return IssueRef{ID: "42", URL: "https://github.com/example/repo/issues/42"}, nil
	}
	return IssueRef{}, &ProviderError{Kind: "rate_limited", Retryable: true, RetryAfter: time.Minute}
}

func TestUncertainReconciliationBacksOffAndBoundsProviderCall(t *testing.T) {
	store := memory.New()
	s, _, ep, credential := setup(t, store)
	p := &reconcileProvider{fixtureProvider: fixtureProvider{fail: &ProviderError{Kind: "transport", Uncertain: true}}}
	s.options.Providers["github"] = p
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	s.options.Clock = func() time.Time { return now }
	receipt, err := s.Accept(t.Context(), credential, []byte(`{"feedback":"uncertain"}`), "one")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := s.Scan(t.Context(), true); err != nil {
			t.Fatal(err)
		}
	}
	if p.finds != 1 || !p.deadline {
		t.Fatalf("unbounded or repeated reconciliation: finds=%d deadline=%v", p.finds, p.deadline)
	}
	sub, _, err := s.GetSubmission(t.Context(), ep, receipt.ID)
	if err != nil || sub.NextAttempt.Before(now.Add(time.Minute)) {
		t.Fatalf("provider retry hint not persisted: %+v %v", sub, err)
	}
	// A restarted worker must respect the persisted schedule too.
	restarted, err := New(store, s.options)
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.Scan(t.Context(), true); err != nil || p.finds != 1 {
		t.Fatalf("restart bypassed backoff: finds=%d %v", p.finds, err)
	}
	now = sub.NextAttempt.Add(time.Second)
	p.confirmed = true
	if err = restarted.Scan(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	sub, _, _ = s.GetSubmission(t.Context(), ep, receipt.ID)
	if sub.State != "delivered" || p.finds != 2 || p.calls != 1 {
		t.Fatalf("reconciliation lost or duplicated creation: state=%s finds=%d creates=%d", sub.State, p.finds, p.calls)
	}
}

type credentialReadFault struct {
	storage.Store
	failure error
}

func (s *credentialReadFault) Get(ctx context.Context, key string) (storage.Object, error) {
	if s.failure != nil && strings.HasPrefix(key, "credentials/") {
		return storage.Object{}, s.failure
	}
	return s.Store.Get(ctx, key)
}

func TestTransientCredentialReadDoesNotPermanentlyFailPendingDelivery(t *testing.T) {
	store := &credentialReadFault{Store: memory.New()}
	s, p, ep, credential := setup(t, store)
	receipt, err := s.Accept(t.Context(), credential, []byte(`{"feedback":"transient"}`), "one")
	if err != nil {
		t.Fatal(err)
	}
	store.failure = errors.New("temporary credential read failure")
	if err = s.Scan(t.Context(), true); err == nil {
		t.Fatal("transient storage outage not surfaced in worker status")
	}
	sub, _, _ := s.GetSubmission(t.Context(), ep, receipt.ID)
	if sub.State != "pending" || sub.Attempts != 0 || p.calls != 0 {
		t.Fatalf("transient read permanently failed delivery: state=%s attempts=%d calls=%d", sub.State, sub.Attempts, p.calls)
	}
	store.failure = nil
	if err = s.Scan(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	sub, _, _ = s.GetSubmission(t.Context(), ep, receipt.ID)
	if sub.State != "delivered" || p.calls != 1 {
		t.Fatalf("pending delivery did not recover: state=%s calls=%d", sub.State, p.calls)
	}
}

func TestDestinationListingSkipsLegacyRevisionPages(t *testing.T) {
	store := memory.New()
	s, _, _, _ := setup(t, store)
	destinations, _, err := s.ListDestinations(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	d := destinations[0]
	other, err := s.SaveDestination(t.Context(), DestinationInput{Name: "Other", Provider: "github", Target: "example/other", Token: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	first := min(d.ID, other.ID)
	for i := 0; i < 150; i++ {
		if _, err = store.Create(t.Context(), fmt.Sprintf("destinations/%s/revisions/%024x.json", first, i), []byte(`{"schema":1}`)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 150; i++ {
		if _, err = s.SaveDestination(t.Context(), DestinationInput{ID: d.ID, Name: "Edited", Provider: d.Provider, Target: d.Target}); err != nil {
			t.Fatal(err)
		}
	}
	items, cursor, err := s.ListDestinations(t.Context(), "")
	if err != nil || cursor != "" || len(items) != 2 {
		t.Fatalf("revisions crowded destinations out: items=%d cursor=%q err=%v", len(items), cursor, err)
	}
}

func TestMissingDisabledAndUnreadableCredentialsStillFailDelivery(t *testing.T) {
	for _, kind := range []string{"missing", "disabled", "invalid-json", "bad-ciphertext"} {
		t.Run(kind, func(t *testing.T) {
			store := &credentialReadFault{Store: memory.New()}
			s, p, ep, secret := setup(t, store)
			receipt, err := s.Accept(t.Context(), secret, []byte(`{"feedback":"credential unavailable"}`), "one")
			if err != nil {
				t.Fatal(err)
			}
			endpoint, _, _ := s.GetEndpoint(t.Context(), ep)
			switch kind {
			case "missing":
				store.failure = storage.ErrNotFound
			case "disabled":
				if err := s.SetCredentialDisabled(t.Context(), endpoint.DestinationID, true); err != nil {
					t.Fatal(err)
				}
			default:
				key := "credentials/" + endpoint.DestinationID + ".json"
				object, err := store.Get(t.Context(), key)
				if err != nil {
					t.Fatal(err)
				}
				body := []byte(`not JSON`)
				if kind == "bad-ciphertext" {
					body = []byte(`{"schema":1,"ciphertext":"AA=="}`)
				}
				if _, err := store.Replace(t.Context(), key, body, object.Version); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Scan(t.Context(), true); err != nil {
				t.Fatal(err)
			}
			sub, _, _ := s.GetSubmission(t.Context(), ep, receipt.ID)
			if sub.State != "failed" || sub.Attempts != 0 || p.calls != 0 {
				t.Fatalf("unavailable credential reached provider or remained pending: state=%s attempts=%d calls=%d", sub.State, sub.Attempts, p.calls)
			}
		})
	}
}

func TestUncertainReconciliationBacksOffWhenDependenciesUnavailable(t *testing.T) {
	for _, kind := range []string{"credential-store", "provider"} {
		t.Run(kind, func(t *testing.T) {
			store := &credentialReadFault{Store: memory.New()}
			s, _, ep, credential := setup(t, store)
			p := &reconcileProvider{fixtureProvider: fixtureProvider{fail: &ProviderError{Kind: "transport", Uncertain: true}}, confirmed: true}
			s.options.Providers["github"] = p
			now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
			s.options.Clock = func() time.Time { return now }
			receipt, err := s.Accept(t.Context(), credential, []byte(`{"feedback":"uncertain dependency"}`), "one")
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Scan(t.Context(), true); err != nil {
				t.Fatal(err)
			}
			if kind == "credential-store" {
				store.failure = errors.New("temporary store outage")
			} else {
				delete(s.options.Providers, "github")
			}
			if err = s.Scan(t.Context(), true); err != nil {
				t.Fatal(err)
			}
			sub, _, err := s.GetSubmission(t.Context(), ep, receipt.ID)
			if err != nil || sub.State != "delivery_uncertain" || !sub.NextAttempt.After(now) || p.finds != 0 {
				t.Fatalf("unavailable dependency bypassed schedule: state=%s next=%v finds=%d err=%v", sub.State, sub.NextAttempt, p.finds, err)
			}
			store.failure = nil
			s.options.Providers["github"] = p
			if err = s.Scan(t.Context(), true); err != nil || p.finds != 0 {
				t.Fatalf("dependency recovery bypassed backoff: finds=%d err=%v", p.finds, err)
			}
			now = sub.NextAttempt.Add(time.Second)
			if err = s.Scan(t.Context(), true); err != nil {
				t.Fatal(err)
			}
			sub, _, _ = s.GetSubmission(t.Context(), ep, receipt.ID)
			if sub.State != "delivered" || p.finds != 1 || p.calls != 1 {
				t.Fatalf("recovery duplicated creation or lost delivery: state=%s finds=%d creates=%d", sub.State, p.finds, p.calls)
			}
		})
	}
}
