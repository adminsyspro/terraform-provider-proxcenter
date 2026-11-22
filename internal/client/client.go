// Package client talks to the ProxCenter public API v1 with a pxc_ token.
package client

import (
	"context"
	"crypto/tls"
	"errors"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

// Config holds what a Client needs to reach ProxCenter.
type Config struct {
	Endpoint   string
	Token      string
	Insecure   bool
	UserAgent  string
	HTTPClient *http.Client
}

// Client is safe for concurrent use.
type Client struct {
	base  string
	token string
	ua    string
	http  *http.Client
	sem   chan struct{}
	// sleep waits between tries; a test seam.
	sleep func(context.Context, time.Duration) error
	// jitter returns a factor in [0.8, 1.2]; a test seam.
	jitter  func() float64
	vmCache vmListCache
}

// New checks the configuration and builds a client. It makes no request.
func New(cfg Config) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if base == "" {
		return nil, errors.New("the ProxCenter endpoint is empty")
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("the ProxCenter API token is empty")
	}
	hc := cfg.HTTPClient
	if hc == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		if cfg.Insecure {
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in for self-signed lab certificates
		}
		hc = &http.Client{Transport: transport, Timeout: 2 * time.Minute}
	}
	ua := cfg.UserAgent
	if ua == "" {
		ua = "terraform-provider-proxcenter/dev"
	}
	return &Client{
		base: base, token: cfg.Token, ua: ua, http: hc,
		sem:    make(chan struct{}, MaxInFlight),
		sleep:  sleepCtx,
		jitter: func() float64 { return 0.8 + 0.4*rand.Float64() }, //nolint:gosec // jitter, not security
	}, nil
}

func (c *Client) newRequest(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "application/json")
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
