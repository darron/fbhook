package service

import (
	"context"
	"strings"
)

type Resolution struct {
	Action               string `json:"action"`
	Actor                string `json:"-"`
	Reason               string `json:"reason"`
	Reference            string `json:"reference,omitempty"`
	AcknowledgeDuplicate bool   `json:"acknowledge_duplicate"`
}

func (s *Service) Resolve(ctx context.Context, ep, id string, in Resolution) error {
	sub, v, e := s.GetSubmission(ctx, ep, id)
	if e != nil {
		return e
	}
	if in.Actor == "" || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 1000 {
		return problem(400, "resolution requires an actor and a reason of at most 1000 characters")
	}
	if sub.State == "delivering" || sub.State == "delivered" {
		return problem(409, "submission cannot be changed in its current state")
	}
	switch in.Action {
	case "retry":
		if sub.Reference != nil {
			return problem(409, "an issue already exists; correct its defaults upstream and verify it instead of creating another")
		}
		if sub.State != "failed" && sub.State != "delivery_uncertain" && sub.State != "retry_scheduled" {
			return problem(409, "submission is not eligible for retry")
		}
		if sub.State == "delivery_uncertain" && !in.AcknowledgeDuplicate {
			return problem(400, "uncertain retry requires acknowledgement of possible duplication")
		}
		sub.Attempts = 0
		sub.NextAttempt = s.now()
		sub.Error = ""
		s.event(&sub, "pending", in.Actor, in.Reason)
	case "abandon":
		if sub.State != "delivery_uncertain" && sub.State != "failed" && sub.State != "retry_scheduled" && sub.State != "pending" {
			return problem(409, "submission cannot be abandoned")
		}
		s.event(&sub, "failed", in.Actor, in.Reason)
		sub.Error = "delivery abandoned by administrator"
	case "link":
		canLink := sub.State == "delivery_uncertain" || sub.State == "failed" && sub.Reference != nil
		if !canLink || in.Reference == "" {
			return problem(400, "link requires an uncertain submission and provider issue ID")
		}
		if sub.Reference != nil && in.Reference != sub.Reference.ID {
			return problem(400, "verify the issue already recorded for this submission")
		}
		p := s.options.Providers[sub.Destination.Provider]
		token, e := s.token(ctx, sub.Destination.CredentialID)
		if e != nil || p == nil {
			return problem(503, "provider unavailable")
		}
		ref, e := p.Find(ctx, sub.Destination, token, sub.Issue, in.Reference)
		if e != nil || ref.ID == "" || ref.URL == "" {
			return problem(400, "issue identity, target or submission marker could not be verified")
		}
		sub.Reference = &ref
		sub.Error = ""
		s.event(&sub, "delivered", in.Actor, in.Reason)
	default:
		return problem(400, "unsupported resolution action")
	}
	_, e = writeJSON(ctx, s.store, submissionKey(ep, id), sub, v)
	return e
}
