package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"github.com/darron/fbhook/storage"
	"strings"
)

func (s *Service) encrypt(id, token string) []byte {
	nonce := make([]byte, s.aead.NonceSize())
	_, _ = rand.Read(nonce)
	return s.aead.Seal(nonce, nonce, []byte(token), []byte(id))
}
func (s *Service) token(ctx context.Context, id string) (string, error) {
	c, _, e := readJSON[credential](ctx, s.store, "credentials/"+id+".json")
	if e != nil || c.Disabled {
		return "", problem(503, "destination credential unavailable")
	}
	n := s.aead.NonceSize()
	if len(c.Ciphertext) < n {
		return "", problem(503, "destination credential unreadable")
	}
	b, e := s.aead.Open(nil, c.Ciphertext[:n], c.Ciphertext[n:], []byte(id))
	if e != nil {
		return "", problem(503, "destination credential unreadable")
	}
	return string(b), nil
}
func (s *Service) SaveDestination(ctx context.Context, in DestinationInput) (Destination, error) {
	var old Destination
	var version string
	var e error
	if len(strings.TrimSpace(in.Name)) == 0 || len(in.Name) > 200 || len(in.Labels) > 50 || len(in.Target) > 300 || len(in.Project) > 200 || len(in.Token) > 8192 {
		return old, problem(400, "invalid destination configuration")
	}
	p := s.options.Providers[in.Provider]
	if p == nil {
		return old, problem(400, "unsupported issue provider")
	}
	if in.ID == "" {
		in.ID = newID()
	} else {
		if !ValidID(in.ID) {
			return old, problem(400, "invalid destination ID")
		}
		old, version, e = s.GetDestination(ctx, in.ID)
		if e != nil {
			return old, e
		}
		if old.Provider != in.Provider {
			return old, problem(400, "destination provider cannot change")
		}
	}
	credentialID := in.ID
	token := in.Token
	if token == "" {
		token, e = s.token(ctx, credentialID)
		if e != nil {
			return old, problem(400, "credential is required for a new destination")
		}
	}
	d := Destination{Schema: 1, ID: in.ID, Revision: newID(), Name: strings.TrimSpace(in.Name), Provider: in.Provider, Target: in.Target, Project: in.Project, Labels: append([]string{}, in.Labels...), CredentialID: credentialID}
	if e = p.Validate(ctx, d, token); e != nil {
		return old, problem(400, "provider rejected destination or labels; check target and credential permissions")
	}
	if in.Token != "" {
		_, cv, ce := readJSON[credential](ctx, s.store, "credentials/"+credentialID+".json")
		if ce != nil && !errors.Is(ce, storage.ErrNotFound) {
			return old, ce
		}
		if _, e = writeJSON(ctx, s.store, "credentials/"+credentialID+".json", credential{Schema: 1, Ciphertext: s.encrypt(credentialID, token)}, cv); e != nil {
			return old, e
		}
	}
	if _, e = writeJSON(ctx, s.store, "destinations/"+d.ID+"/revisions/"+d.Revision+".json", d, ""); e != nil {
		return old, e
	}
	_, e = writeJSON(ctx, s.store, "destinations/"+d.ID+".json", d, version)
	return d, e
}
func (s *Service) GetDestination(ctx context.Context, id string) (Destination, string, error) {
	if !ValidID(id) {
		return Destination{}, "", storage.ErrNotFound
	}
	return readJSON[Destination](ctx, s.store, "destinations/"+id+".json")
}
func (s *Service) SetCredentialDisabled(ctx context.Context, id string, disabled bool) error {
	if !ValidID(id) {
		return storage.ErrNotFound
	}
	c, v, e := readJSON[credential](ctx, s.store, "credentials/"+id+".json")
	if e != nil {
		return e
	}
	c.Disabled = disabled
	_, e = writeJSON(ctx, s.store, "credentials/"+id+".json", c, v)
	return e
}
func (s *Service) ListDestinations(ctx context.Context, cursor string) ([]Destination, string, error) {
	p, e := s.store.List(ctx, "destinations/", cursor, 100)
	if e != nil {
		return nil, "", e
	}
	out := []Destination{}
	for _, k := range p.Keys {
		if strings.Count(strings.TrimPrefix(k, "destinations/"), "/") != 0 {
			continue
		}
		d, _, e := readJSON[Destination](ctx, s.store, k)
		if e != nil {
			return nil, "", e
		}
		out = append(out, d)
	}
	return out, p.Cursor, nil
}
func newSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
func secretDigest(secret string) string {
	d := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(d[:])
}
func (s *Service) CreateEndpoint(ctx context.Context, name, destinationID string, labels []string) (Endpoint, string, error) {
	var ep Endpoint
	if strings.TrimSpace(name) == "" || len(name) > 200 || len(labels) > 0 {
		return ep, "", problem(400, "name required; configure labels on the destination")
	}
	if _, _, e := s.GetDestination(ctx, destinationID); e != nil {
		return ep, "", e
	}
	secret := newSecret()
	ep = Endpoint{Schema: 1, ID: newID(), Name: strings.TrimSpace(name), DestinationID: destinationID, Labels: []string{}, SecretDigest: secretDigest(secret), CreatedAt: s.now()}
	_, e := writeJSON(ctx, s.store, "endpoints/"+ep.ID+".json", ep, "")
	return ep, ep.ID + "." + secret, e
}
func (s *Service) GetEndpoint(ctx context.Context, id string) (Endpoint, string, error) {
	if !ValidID(id) {
		return Endpoint{}, "", storage.ErrNotFound
	}
	return readJSON[Endpoint](ctx, s.store, "endpoints/"+id+".json")
}
func (s *Service) ListEndpoints(ctx context.Context, cursor string) ([]Endpoint, string, error) {
	p, e := s.store.List(ctx, "endpoints/", cursor, 100)
	if e != nil {
		return nil, "", e
	}
	out := []Endpoint{}
	for _, k := range p.Keys {
		ep, _, e := readJSON[Endpoint](ctx, s.store, k)
		if e != nil {
			return nil, "", e
		}
		ep.SecretDigest = ""
		out = append(out, ep)
	}
	return out, p.Cursor, nil
}
func (s *Service) RotateEndpoint(ctx context.Context, id string) (string, error) {
	ep, v, e := s.GetEndpoint(ctx, id)
	if e != nil {
		return "", e
	}
	secret := newSecret()
	ep.SecretDigest = secretDigest(secret)
	_, e = writeJSON(ctx, s.store, "endpoints/"+id+".json", ep, v)
	return id + "." + secret, e
}
func (s *Service) UpdateEndpoint(ctx context.Context, id, name, destinationID string, disabled bool) error {
	ep, v, e := s.GetEndpoint(ctx, id)
	if e != nil {
		return e
	}
	if strings.TrimSpace(name) == "" || len(name) > 200 {
		return problem(400, "endpoint name required")
	}
	if _, _, e = s.GetDestination(ctx, destinationID); e != nil {
		return e
	}
	ep.Name = strings.TrimSpace(name)
	ep.DestinationID = destinationID
	ep.Disabled = disabled
	_, e = writeJSON(ctx, s.store, "endpoints/"+id+".json", ep, v)
	return e
}
func (s *Service) authenticate(ctx context.Context, credential string) (Endpoint, error) {
	id, secret, ok := strings.Cut(credential, ".")
	if !ok || len(secret) != 43 || !ValidID(id) {
		return Endpoint{}, problem(404, "feedback endpoint unavailable")
	}
	ep, _, e := s.GetEndpoint(ctx, id)
	if e != nil {
		if errors.Is(e, storage.ErrNotFound) {
			return ep, problem(404, "feedback endpoint unavailable")
		}
		return ep, e
	}
	if ep.Disabled || subtle.ConstantTimeCompare([]byte(secretDigest(secret)), []byte(ep.SecretDigest)) != 1 {
		return Endpoint{}, problem(404, "feedback endpoint unavailable")
	}
	return ep, nil
}
