package service

import (
	"context"
	"crypto/rand"
	"errors"
	"github.com/darron/fbhook/storage"
	"math/big"
	"time"
)

// Run owns one serial worker. Deployments must stop the old worker before
// starting another; conditional updates also guard concurrent admin operations.
// Every serial pass can safely recover unfinished attempts: no attempt from a
// previous pass is still active when Scan takes the lock.
func (s *Service) Run(ctx context.Context) error {
	for {
		_ = s.Scan(ctx, true)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		timer := time.NewTimer(s.options.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Scan completes a fair, paginated recovery pass. It does not assume the listing
// is a snapshot; repeated full scans discover concurrent inserts before a cursor.
func (s *Service) Scan(ctx context.Context, recoverAttempts bool) (scanErr error) {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	cursor := ""
	stats := WorkerStatus{LastScan: s.now()}
	var first error
	defer func() {
		if scanErr != nil {
			stats.LastError = "worker scan incomplete"
		}
		s.statusMu.Lock()
		s.status = stats
		s.statusMu.Unlock()
	}()
	for {
		page, e := s.store.List(ctx, "submissions/", cursor, 100)
		if e != nil {
			return e
		}
		for _, key := range page.Keys {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			sub, v, e := readJSON[Submission](ctx, s.store, key)
			if e != nil {
				if first == nil {
					first = e
				}
				continue
			}
			stats.Scanned++
			if sub.State == "pending" || sub.State == "retry_scheduled" {
				stats.Pending++
				if stats.OldestPending == nil || sub.CreatedAt.Before(*stats.OldestPending) {
					at := sub.CreatedAt
					stats.OldestPending = &at
				}
			}
			if sub.State == "delivery_uncertain" {
				stats.Uncertain++
			}
			if recoverAttempts && sub.State == "delivering" {
				stats.Uncertain++
				s.event(&sub, "delivery_uncertain", "worker", "unfinished external creation recovered")
				sub.Error = "external creation outcome unknown"
				if _, e = writeJSON(ctx, s.store, key, sub, v); e != nil {
					if first == nil {
						first = e
					}
					continue
				}
				continue
			}
			if sub.State == "delivery_uncertain" {
				// Positive reconciliation only. An absent search result never justifies a create.
				p := s.options.Providers[sub.Destination.Provider]
				token, e := s.token(ctx, sub.Destination.CredentialID)
				if e != nil || p == nil {
					continue
				}
				ref, e := p.Find(ctx, sub.Destination, token, sub.Issue, "")
				if e == nil && ref.ID != "" && ref.URL != "" {
					sub.Reference = &ref
					sub.Error = ""
					s.event(&sub, "delivered", "worker", "existing issue confirmed")
					_, e = writeJSON(ctx, s.store, key, sub, v)
					if e != nil && first == nil {
						first = e
					}
				}
				continue
			}
			if sub.State != "pending" && (sub.State != "retry_scheduled" || sub.NextAttempt.After(s.now())) {
				continue
			}
			if e = s.deliver(ctx, key, sub, v); e != nil && !errors.Is(e, storage.ErrConflict) && first == nil {
				first = e
			}
		}
		if page.Cursor == "" {
			break
		}
		if page.Cursor == cursor {
			return errors.New("storage pagination did not advance")
		}
		cursor = page.Cursor
	}
	return first
}
func (s *Service) deliver(ctx context.Context, key string, sub Submission, v string) error {
	p := s.options.Providers[sub.Destination.Provider]
	token, e := s.token(ctx, sub.Destination.CredentialID)
	if e != nil || p == nil {
		sub.Error = "destination credential or provider unavailable"
		s.event(&sub, "failed", "worker", sub.Error)
		_, e = writeJSON(ctx, s.store, key, sub, v)
		return e
	}
	sub.Attempts++
	sub.TotalAttempts++
	sub.AttemptID = newID()
	sub.Error = ""
	s.event(&sub, "delivering", "worker", "")
	v, e = writeJSON(ctx, s.store, key, sub, v)
	if e != nil {
		return e
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	ref, e := p.Create(callCtx, sub.Destination, token, sub.Issue)
	cancel()
	if e == nil && ref.ID != "" && ref.URL != "" {
		sub.Reference = &ref
		s.event(&sub, "delivered", "worker", "")
	} else {
		if ref.ID != "" && ref.URL != "" {
			sub.Reference = &ref
		}
		var pe *ProviderError
		if !errors.As(e, &pe) {
			pe = &ProviderError{Kind: "unclassified_creation", Uncertain: true}
		}
		sub.Error = pe.Error()
		switch {
		case sub.Reference != nil:
			s.event(&sub, "failed", "worker", sub.Error)
		case pe.Uncertain:
			s.event(&sub, "delivery_uncertain", "worker", sub.Error)
		case pe.Retryable && sub.Attempts < s.options.MaxAttempts:
			sub.NextAttempt = s.now().Add(s.backoff(sub.Attempts, pe.RetryAfter))
			s.event(&sub, "retry_scheduled", "worker", sub.Error)
		default:
			s.event(&sub, "failed", "worker", sub.Error)
		}
	}
	// If this write fails, the persisted state remains delivering. Recovery will
	// treat it as uncertain, never as pending, even if Create actually succeeded.
	_, e = writeJSON(ctx, s.store, key, sub, v)
	return e
}
func (s *Service) backoff(attempt int, hint time.Duration) time.Duration {
	delay := s.options.InitialBackoff
	for i := 1; i < attempt && delay < s.options.MaxBackoff; i++ {
		if delay > s.options.MaxBackoff/2 {
			delay = s.options.MaxBackoff
			break
		}
		delay *= 2
	}
	if delay > s.options.MaxBackoff {
		delay = s.options.MaxBackoff
	}
	jitter := time.Duration(0)
	if random, err := rand.Int(rand.Reader, big.NewInt(int64(max(delay/4, 1)))); err == nil {
		jitter = time.Duration(random.Int64())
	}
	delay = min(delay+jitter, s.options.MaxBackoff)
	// Never retry earlier than a provider's explicit hint, even if it exceeds
	// the cap applied to our own exponential schedule.
	return max(delay, hint)
}
