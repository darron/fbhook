// Package service owns durable feedback acceptance and issue delivery. It is
// independent of HTTP routing, cloud SDKs, and concrete issue providers.
package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/darron/fbhook"
	"github.com/darron/fbhook/storage"
	"regexp"
	"sync"
	"time"
)

type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string                 { return e.Message }
func problem(status int, message string) error { return &Error{status, message} }
func ErrorStatus(e error) int {
	var p *Error
	if errors.As(e, &p) {
		return p.Status
	}
	if errors.Is(e, storage.ErrNotFound) {
		return 404
	}
	if errors.Is(e, storage.ErrConflict) {
		return 409
	}
	return 503
}

type Destination struct {
	Schema       int      `json:"schema"`
	ID           string   `json:"id"`
	Revision     string   `json:"revision"`
	Name         string   `json:"name"`
	Provider     string   `json:"provider"`
	Target       string   `json:"target"`
	Project      string   `json:"project,omitempty"`
	Labels       []string `json:"labels"`
	CredentialID string   `json:"credential_id"`
	Disabled     bool     `json:"disabled"`
}
type DestinationInput struct {
	ID       string   `json:"id,omitempty"`
	Name     string   `json:"name"`
	Provider string   `json:"provider"`
	Target   string   `json:"target"`
	Project  string   `json:"project,omitempty"`
	Labels   []string `json:"labels"`
	Token    string   `json:"token,omitempty"`
}
type credential struct {
	Schema     int    `json:"schema"`
	Ciphertext []byte `json:"ciphertext"`
	Disabled   bool   `json:"disabled"`
}
type Endpoint struct {
	Schema        int       `json:"schema"`
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	DestinationID string    `json:"destination_id"`
	Labels        []string  `json:"labels"`
	Disabled      bool      `json:"disabled"`
	SecretDigest  string    `json:"secret_digest,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}
type Issue struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
}
type IssueRef struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}
type Provider interface {
	Validate(context.Context, Destination, string) error
	Create(context.Context, Destination, string, Issue) (IssueRef, error)
	// Find searches by marker when reference is empty; otherwise it verifies a
	// specific provider issue ID. Only positive target/marker verification succeeds.
	Find(context.Context, Destination, string, Issue, string) (IssueRef, error)
}
type ProviderError struct {
	Kind                 string
	Retryable, Uncertain bool
	RetryAfter           time.Duration
}

func (e *ProviderError) Error() string { return "issue provider: " + e.Kind }

type Event struct {
	At     time.Time `json:"at"`
	From   string    `json:"from"`
	To     string    `json:"to"`
	Actor  string    `json:"actor,omitempty"`
	Reason string    `json:"reason,omitempty"`
}
type Submission struct {
	Schema            int             `json:"schema"`
	ID                string          `json:"id"`
	EndpointID        string          `json:"endpoint_id"`
	Feedback          fbhook.Feedback `json:"feedback"`
	PayloadDigest     string          `json:"payload_digest"`
	Destination       Destination     `json:"destination"`
	Issue             Issue           `json:"issue"`
	State             string          `json:"state"`
	CreationUncertain bool            `json:"creation_uncertain,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	NextAttempt       time.Time       `json:"next_attempt,omitempty"`
	Attempts          int             `json:"attempts"`
	TotalAttempts     int             `json:"total_attempts"`
	ReconcileAttempts int             `json:"reconcile_attempts,omitempty"`
	AttemptID         string          `json:"attempt_id,omitempty"`
	Error             string          `json:"error,omitempty"`
	Reference         *IssueRef       `json:"reference,omitempty"`
	History           []Event         `json:"history"`
	TotalEvents       int             `json:"total_events"`
}
type Options struct {
	EncryptionKey              []byte
	Providers                  map[string]Provider
	Clock                      func() time.Time
	PollInterval               time.Duration
	MaxAttempts                int
	InitialBackoff, MaxBackoff time.Duration
}
type WorkerStatus struct {
	LastScan      time.Time  `json:"last_scan"`
	LastError     string     `json:"last_error,omitempty"`
	Scanned       int        `json:"scanned"`
	Pending       int        `json:"pending"`
	Uncertain     int        `json:"uncertain"`
	OldestPending *time.Time `json:"oldest_pending,omitempty"`
}
type Service struct {
	store    storage.Store
	options  Options
	aead     cipher.AEAD
	scanMu   sync.Mutex
	statusMu sync.Mutex
	status   WorkerStatus
}

func New(store storage.Store, options Options) (*Service, error) {
	if store == nil || len(options.EncryptionKey) != 32 {
		return nil, errors.New("store and 32-byte encryption key required")
	}
	block, e := aes.NewCipher(options.EncryptionKey)
	if e != nil {
		return nil, e
	}
	aead, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 15 * time.Second
	}
	if options.MaxAttempts <= 0 {
		options.MaxAttempts = 10
	}
	if options.InitialBackoff <= 0 {
		options.InitialBackoff = 2 * time.Second
	}
	if options.MaxBackoff <= 0 {
		options.MaxBackoff = 15 * time.Minute
	}
	providers := map[string]Provider{}
	for k, v := range options.Providers {
		providers[k] = v
	}
	options.Providers = providers
	options.EncryptionKey = append([]byte(nil), options.EncryptionKey...)
	return &Service{store: store, options: options, aead: aead}, nil
}

var validID = regexp.MustCompile(`^[a-f0-9]{24,64}$`)

func ValidID(s string) bool { return validID.MatchString(s) }
func newID() string         { b := make([]byte, 12); _, _ = rand.Read(b); return hex.EncodeToString(b) }
func readJSON[T any](ctx context.Context, store storage.Store, key string) (T, string, error) {
	var out T
	o, e := store.Get(ctx, key)
	if e != nil {
		return out, "", e
	}
	if e = json.Unmarshal(o.Body, &out); e != nil {
		return out, "", errors.New("stored object is invalid")
	}
	var version struct {
		Schema int `json:"schema"`
	}
	if json.Unmarshal(o.Body, &version) != nil || version.Schema != 1 {
		return out, "", errors.New("unsupported stored schema")
	}
	return out, o.Version, nil
}
func writeJSON(ctx context.Context, store storage.Store, key string, value any, version string) (string, error) {
	b, e := json.Marshal(value)
	if e != nil {
		return "", errors.New("cannot encode stored object")
	}
	if len(b) > 2*1024*1024 {
		return "", problem(413, "stored record exceeds limit")
	}
	if version == "" {
		return store.Create(ctx, key, b)
	}
	return store.Replace(ctx, key, b, version)
}
func (s *Service) now() time.Time       { return s.options.Clock().UTC() }
func (s *Service) Store() storage.Store { return s.store }
func (s *Service) Status() WorkerStatus {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	return s.status
}
func (s *Service) Ready(ctx context.Context) error {
	_, e := s.store.List(ctx, "endpoints/", "", 1)
	return e
}
func (s *Service) event(sub *Submission, state, actor, reason string) {
	now := s.now()
	sub.History = append(sub.History, Event{At: now, From: sub.State, To: state, Actor: actor, Reason: reason})
	if len(sub.History) > 20 {
		sub.History = sub.History[len(sub.History)-20:]
	}
	sub.TotalEvents++
	switch state {
	case "delivery_uncertain":
		sub.CreationUncertain = true
	case "delivered":
		sub.CreationUncertain = false
	}
	sub.State = state
	sub.UpdatedAt = now
}
