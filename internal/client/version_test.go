package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func healthServer(t *testing.T, version string, seen func(*http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			seen(r)
		}
		if r.URL.Path != "/api/v1/public/health" {
			http.NotFound(w, r)
			return
		}
		if version != "" {
			w.Header().Set("ProxCenter-Version", version)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"status":"ok"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestClient(t *testing.T, endpoint string) *Client {
	t.Helper()
	c, err := New(Config{Endpoint: endpoint, Token: "pxc_x", UserAgent: "terraform-provider-proxcenter/test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestCheckVersionAcceptsMinimum(t *testing.T) {
	srv := healthServer(t, "1.5.0", nil)
	got, err := newTestClient(t, srv.URL).CheckVersion(context.Background())
	if err != nil || got != "1.5.0" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestCheckVersionAcceptsPrerelease(t *testing.T) {
	srv := healthServer(t, "1.5.1-rc2", nil)
	if _, err := newTestClient(t, srv.URL).CheckVersion(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckVersionAcceptsPrereleaseOfMinimum(t *testing.T) {
	srv := healthServer(t, MinProxCenterVersion+"-rc1", nil)
	if _, err := newTestClient(t, srv.URL).CheckVersion(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckVersionRejectsOld(t *testing.T) {
	srv := healthServer(t, "1.4.13", nil)
	_, err := newTestClient(t, srv.URL).CheckVersion(context.Background())
	if err == nil || !strings.Contains(err.Error(), "1.4.13") || !strings.Contains(err.Error(), MinProxCenterVersion) {
		t.Fatalf("want an error naming both versions, got %v", err)
	}
}

func TestCheckVersionRejectsMissingHeader(t *testing.T) {
	srv := healthServer(t, "", nil)
	_, err := newTestClient(t, srv.URL).CheckVersion(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ProxCenter-Version") {
		t.Fatalf("want an error naming the header, got %v", err)
	}
}

func TestRequestsCarryBearerAndUserAgent(t *testing.T) {
	var auth, ua string
	srv := healthServer(t, "1.5.0", func(r *http.Request) {
		auth, ua = r.Header.Get("Authorization"), r.Header.Get("User-Agent")
	})
	if _, err := newTestClient(t, srv.URL+"/").CheckVersion(context.Background()); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer pxc_x" {
		t.Fatalf("Authorization = %q", auth)
	}
	if !strings.HasPrefix(ua, "terraform-provider-proxcenter/") {
		t.Fatalf("User-Agent = %q", ua)
	}
}

func TestNewRejectsEmptyEndpointOrToken(t *testing.T) {
	if _, err := New(Config{Token: "pxc_x"}); err == nil {
		t.Fatal("empty endpoint accepted")
	}
	if _, err := New(Config{Endpoint: "https://x"}); err == nil {
		t.Fatal("empty token accepted")
	}
}
