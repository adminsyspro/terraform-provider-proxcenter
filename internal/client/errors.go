package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// APIError is a non 2xx answer of the API, kept as ProxCenter wrote it.
type APIError struct {
	Status     int
	Message    string
	Violations []string
	// Body is the raw answer, truncated to 512 bytes, when it was not JSON.
	Body string
}

func (e *APIError) Error() string {
	var b strings.Builder
	b.WriteString(e.Message)
	for _, v := range e.Violations {
		b.WriteString("\n- ")
		b.WriteString(v)
	}
	return b.String()
}

// IsNotFound tells whether err is an API 404.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

func (c *Client) apiError(status int, raw []byte) *APIError {
	out := &APIError{Status: status}
	var body struct {
		Error      string   `json:"error"`
		Message    string   `json:"message"`
		Violations []string `json:"violations"`
	}
	if json.Unmarshal(raw, &body) == nil {
		out.Message = body.Error
		if out.Message == "" {
			out.Message = body.Message
		}
		out.Violations = body.Violations
	} else {
		out.Body = string(raw)
		if len(out.Body) > 512 {
			out.Body = out.Body[:512]
		}
	}
	if out.Message == "" {
		out.Message = http.StatusText(status)
	}
	out.Message = c.redact(out.Message)
	out.Body = c.redact(out.Body)
	for i, v := range out.Violations {
		out.Violations[i] = c.redact(v)
	}
	return out
}

// redact removes the token from any text that may reach a log or a diagnostic.
func (c *Client) redact(s string) string {
	if c.token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token, "pxc_***")
}
