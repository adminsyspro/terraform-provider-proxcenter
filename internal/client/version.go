package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// MinProxCenterVersion is the oldest ProxCenter this provider works with.
const MinProxCenterVersion = "1.5.0"

// CheckVersion reads the ProxCenter-Version header every answer to a token
// carries and refuses a server older than MinProxCenterVersion. A server
// without the header predates the routes the provider needs.
func (c *Client) CheckVersion(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v1/public/health", nil)
	if err != nil {
		return "", err
	}
	c.newRequest(req)
	res, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("reaching ProxCenter at %s: %w", c.base, err)
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, res.Body)
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("ProxCenter refused the API token (HTTP %d)", res.StatusCode)
	}
	version := strings.TrimSpace(res.Header.Get("ProxCenter-Version"))
	if version == "" {
		return "", fmt.Errorf("ProxCenter at %s sent no ProxCenter-Version header: it is older than %s, the oldest version this provider supports", c.base, MinProxCenterVersion)
	}
	if compareVersions(version, MinProxCenterVersion) < 0 {
		return "", fmt.Errorf("ProxCenter %s is older than %s, the oldest version this provider supports", version, MinProxCenterVersion)
	}
	return version, nil
}

// compareVersions compares MAJOR.MINOR.PATCH, ignoring any -pre or +build
// suffix: a release candidate of a version counts as that version.
func compareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := range pa {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionParts(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out [3]int
	for i, part := range strings.SplitN(v, ".", 3) {
		n, err := strconv.Atoi(part)
		if err != nil {
			return [3]int{}
		}
		out[i] = n
	}
	return out
}
