// Package client talks to the ProxCenter public API v1 with a pxc_ token.
package client

import (
	"crypto/tls"
	"errors"
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
	return &Client{base: base, token: cfg.Token, ua: ua, http: hc}, nil
}

func (c *Client) newRequest(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "application/json")
}
