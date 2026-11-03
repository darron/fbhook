package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/darron/fbhook"
	"github.com/darron/fbhook/storage"
	"strings"
)

func submissionKey(ep, id string) string { return "submissions/" + ep + "/" + id + ".json" }
func (s *Service) Accept(ctx context.Context, credential string, body []byte, key string) (fbhook.Receipt, error) {
	return s.AcceptWithAdmission(ctx, credential, func() ([]byte, error) { return body, nil }, key, nil)
}

// AcceptWithAdmission checks the credential before charging an optional intake
// budget or reading the body. The admission callback receives an authenticated
// endpoint ID, never caller input. Rejected requests never invoke readBody.
func (s *Service) AcceptWithAdmission(ctx context.Context, credential string, readBody func() ([]byte, error), key string, admit func(string) bool) (fbhook.Receipt, error) {
	var receipt fbhook.Receipt
	if readBody == nil {
		return receipt, problem(400, "feedback body reader required")
	}
	ep, e := s.authenticate(ctx, credential)
	if e != nil {
		return receipt, e
	}
	if admit != nil && !admit(ep.ID) {
		return receipt, problem(429, "feedback rate limit reached")
	}
	body, e := readBody()
	if e != nil {
		return receipt, e
	}
	f, e := fbhook.Parse(body)
	if e != nil {
		return receipt, problem(400, e.Error())
	}
	if len(key) > 200 || strings.ContainsAny(key, "\r\n") {
		return receipt, problem(400, "invalid idempotency key")
	}
	id := newID()
	if key != "" {
		h := sha256.Sum256([]byte(ep.ID + "\x00" + key))
		id = hex.EncodeToString(h[:])
	}
	digest := sha256.Sum256(body)
	payloadDigest := hex.EncodeToString(digest[:])
	path := submissionKey(ep.ID, id)
	// Resolve a retry before looking at changed destination configuration.
	if key != "" {
		old, _, err := readJSON[Submission](ctx, s.store, path)
		if err == nil {
			if old.PayloadDigest != payloadDigest {
				return receipt, problem(409, "idempotency key already has different content")
			}
			return fbhook.Receipt{ID: old.ID, Status: "accepted"}, nil
		}
		if !errors.Is(err, storage.ErrNotFound) {
			return receipt, err
		}
	}
	d, _, e := s.GetDestination(ctx, ep.DestinationID)
	if e != nil {
		return receipt, e
	}
	if d.Disabled {
		return receipt, problem(503, "destination disabled")
	}
	issue := Issue{ID: id, Title: f.Title, Body: fbhook.IssueBody(f, id)}
	if len(issue.Body) > fbhook.MaxIssueBytes {
		return receipt, problem(413, "feedback and rendered context exceed issue body limit")
	}
	now := s.now()
	sub := Submission{Schema: 1, ID: id, EndpointID: ep.ID, Feedback: f, PayloadDigest: payloadDigest, Destination: d, Issue: issue, State: "pending", CreatedAt: now, UpdatedAt: now, History: []Event{}}
	_, e = writeJSON(ctx, s.store, path, sub, "")
	if errors.Is(e, storage.ErrConflict) {
		existing, _, readErr := readJSON[Submission](ctx, s.store, path)
		if readErr != nil {
			return receipt, readErr
		}
		if existing.PayloadDigest != payloadDigest {
			return receipt, problem(409, "idempotency key already has different content")
		}
		e = nil
	}
	if e != nil {
		return receipt, e
	}
	return fbhook.Receipt{ID: id, Status: "accepted"}, nil
}

func (s *Service) GetSubmission(ctx context.Context, ep, id string) (Submission, string, error) {
	if !ValidID(ep) || !ValidID(id) {
		return Submission{}, "", storage.ErrNotFound
	}
	sub, version, err := readJSON[Submission](ctx, s.store, submissionKey(ep, id))
	// Older records lack the durable flag. Recover unresolved uncertainty from
	// retained history, stopping at a later attempt or acknowledged retry.
	if !sub.CreationUncertain && sub.State != "delivered" {
		if sub.State == "delivery_uncertain" {
			sub.CreationUncertain = true
		}
		for i := len(sub.History) - 1; !sub.CreationUncertain && i >= 0; i-- {
			event := sub.History[i]
			if event.To == "pending" || event.To == "delivering" || event.To == "delivered" {
				break
			}
			if event.To == "delivery_uncertain" || event.From == "delivery_uncertain" {
				sub.CreationUncertain = true
			}
		}
	}
	return sub, version, err
}
func (s *Service) ListSubmissions(ctx context.Context, cursor string) ([]Submission, string, error) {
	p, e := s.store.List(ctx, "submissions/", cursor, 50)
	if e != nil {
		return nil, "", e
	}
	out := []Submission{}
	for _, k := range p.Keys {
		sub, _, e := readJSON[Submission](ctx, s.store, k)
		if e != nil {
			return nil, "", e
		}
		out = append(out, sub)
	}
	return out, p.Cursor, nil
}
