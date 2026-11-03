// Package api provides bounded, non-redirecting HTTP calls for issue adapters.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/darron/fbhook/service"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func Client(c *http.Client) *http.Client {
	if c == nil {
		c = &http.Client{Timeout: 20 * time.Second}
	}
	out := *c
	out.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &out
}
func Call(ctx context.Context, client *http.Client, method, url, auth string, payload any, creation bool) ([]byte, error) {
	var body []byte
	var e error
	if payload != nil {
		body, e = json.Marshal(payload)
		if e != nil {
			return nil, errors.New("cannot encode provider request")
		}
	}
	req, e := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if e != nil {
		return nil, &service.ProviderError{Kind: "invalid_request"}
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "fbhook")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	res, e := client.Do(req)
	if e != nil {
		return nil, &service.ProviderError{Kind: "transport", Uncertain: creation}
	}
	defer func() { _ = res.Body.Close() }()
	data, e := io.ReadAll(io.LimitReader(res.Body, 2*1024*1024+1))
	if e != nil || len(data) > 2*1024*1024 {
		return nil, &service.ProviderError{Kind: "response_unreadable", Uncertain: creation}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		pe := &service.ProviderError{Kind: "request_rejected", Uncertain: creation && (res.StatusCode >= 500 || res.StatusCode == 408)}
		// GitHub secondary limits can omit Retry-After even while primary
		// quota remains. Inspect only the bounded JSON discriminator; never expose it.
		var rejection struct {
			Message string `json:"message"`
		}
		secondary := res.StatusCode == 403 && json.Unmarshal(data, &rejection) == nil && strings.Contains(strings.ToLower(rejection.Message), "secondary rate limit")
		if res.StatusCode == 404 {
			pe.Kind = "not_found"
		}
		if res.StatusCode == 429 || (res.StatusCode == 403 && (res.Header.Get("X-RateLimit-Remaining") == "0" || res.Header.Get("Retry-After") != "" || secondary)) {
			pe.Kind = "rate_limited"
			pe.Retryable = true
			pe.Uncertain = false
			pe.RetryAfter = retryAfter(res.Header)
			if secondary && res.Header.Get("Retry-After") == "" {
				pe.RetryAfter = max(pe.RetryAfter, time.Minute)
			}
		}
		if res.StatusCode == 401 || res.StatusCode == 403 && !pe.Retryable {
			pe.Kind = "permission_denied"
		}
		return nil, pe
	}
	return data, nil
}
func retryAfter(h http.Header) time.Duration {
	if n, e := strconv.ParseInt(h.Get("Retry-After"), 10, 32); e == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	if at, e := http.ParseTime(h.Get("Retry-After")); e == nil {
		return max(time.Until(at), 0)
	}
	if n, e := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); e == nil {
		return max(time.Until(time.Unix(n, 0)), 0)
	}
	return time.Minute
}
