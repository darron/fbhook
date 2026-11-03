package fbhook

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPError deliberately omits server bodies and credential-bearing URLs.
type HTTPError struct{ StatusCode int }

func (e *HTTPError) Error() string {
	return fmt.Sprintf("feedback service returned HTTP %d", e.StatusCode)
}

type Client struct {
	endpoint string
	http     *http.Client
}

func NewClient(endpoint string, client *http.Client) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, "/api/v1/feedback/") {
		return nil, errors.New("invalid feedback endpoint")
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{endpoint: endpoint, http: &copyClient}, nil
}

// Submit returns durable acceptance, not issue delivery. A supplied idempotency
// key can also be reused after a caller restart. Retries reuse exactly the same
// serialized bytes and key; cancellation stops the retry sequence.
func (c *Client) Submit(ctx context.Context, f Feedback, key string) (Receipt, error) {
	var receipt Receipt
	body, err := json.Marshal(f)
	if err != nil {
		return receipt, errors.New("context is not valid JSON")
	}
	if _, err = Parse(body); err != nil {
		return receipt, err
	}
	if key == "" {
		key = rand.Text()
	}
	if len(key) > 200 || strings.ContainsAny(key, "\r\n") {
		return receipt, errors.New("invalid idempotency key")
	}
	for attempt := 0; attempt < 3; attempt++ {
		req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
		if e != nil {
			return receipt, errors.New("cannot prepare feedback request")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		res, e := c.http.Do(req)
		retry := e != nil
		if e == nil {
			data, readErr := io.ReadAll(io.LimitReader(res.Body, 4097))
			_ = res.Body.Close()
			if res.StatusCode == http.StatusAccepted && readErr == nil && len(data) <= 4096 {
				if json.Unmarshal(data, &receipt) != nil || receipt.ID == "" || receipt.Status != "accepted" {
					return Receipt{}, errors.New("invalid acceptance receipt")
				}
				return receipt, nil
			}
			err = &HTTPError{StatusCode: res.StatusCode}
			retry = readErr != nil || res.StatusCode == 429 || res.StatusCode >= 500
		} else {
			err = errors.New("feedback request failed")
		}
		if ctx.Err() != nil {
			return Receipt{}, ctx.Err()
		}
		if !retry || attempt == 2 {
			return Receipt{}, err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Receipt{}, ctx.Err()
		case <-timer.C:
		}
	}
	return Receipt{}, err
}
