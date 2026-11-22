package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	// MaxInFlight bounds the requests one provider process sends at once.
	MaxInFlight = 10
	// maxThrottleRetries bounds the 429 retries of one call.
	maxThrottleRetries = 10
	defaultRetryAfter  = 5 * time.Second
)

// gatewayBackoff is the wait before each retry of a GET answered 502/503/504.
var gatewayBackoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// do sends one API call. body, when not nil, is sent as JSON; out, when not
// nil, receives the decoded 2xx body. A 429 is retried after Retry-After for
// any method; 502/503/504 only for GET. Any other non 2xx is an *APIError.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding the request to %s: %w", path, err)
		}
		payload = b
	}
	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	throttled, gateway := 0, 0
	for {
		res, raw, err := c.send(ctx, method, target, payload)
		if err != nil {
			return err
		}
		switch {
		case res.StatusCode == http.StatusTooManyRequests && throttled < maxThrottleRetries:
			throttled++
			if err := c.sleep(ctx, retryAfter(res)); err != nil {
				return err
			}
			continue
		case isGatewayError(res.StatusCode) && method == http.MethodGet && gateway < len(gatewayBackoff):
			wait := gatewayBackoff[gateway]
			gateway++
			if err := c.sleep(ctx, wait); err != nil {
				return err
			}
			continue
		case res.StatusCode < 200 || res.StatusCode > 299:
			return c.apiError(res.StatusCode, raw)
		}
		if out != nil && len(bytes.TrimSpace(raw)) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("decoding the answer of %s %s: %w", method, path, err)
			}
		}
		return nil
	}
}

func (c *Client) send(ctx context.Context, method, target string, payload []byte) (*answer, []byte, error) {
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	defer func() { <-c.sem }()

	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, nil, err
	}
	c.newRequest(req)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("calling ProxCenter: %s", c.redact(err.Error()))
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return nil, nil, fmt.Errorf("reading the ProxCenter answer: %w", err)
	}
	return &answer{StatusCode: res.StatusCode, Header: res.Header}, raw, nil
}

func isGatewayError(code int) bool {
	return code == http.StatusBadGateway || code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout
}

func retryAfter(res *answer) time.Duration {
	if s, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil && s >= 0 {
		return time.Duration(s) * time.Second
	}
	return defaultRetryAfter
}

// answer is what do needs of a response once its body is read and closed.
type answer struct {
	StatusCode int
	Header     http.Header
}
