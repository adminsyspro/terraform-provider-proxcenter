package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recordSleeps replaces the client's waits with an instant recorder.
func recordSleeps(c *Client) *[]time.Duration {
	var mu sync.Mutex
	got := []time.Duration{}
	c.sleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, d)
		return nil
	}
	return &got
}

func scripted(t *testing.T, answers ...func(w http.ResponseWriter)) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		i := int(n) - 1
		if i >= len(answers) {
			i = len(answers) - 1
		}
		answers[i](w)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func status(code int, body string, headers ...string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		if strings.HasPrefix(body, "{") {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

func TestAPIErrorKeepsMessageAndViolations(t *testing.T) {
	srv, _ := scripted(t, status(409, `{"error":"Quota exceeded","violations":["vCPU: 10 > 8","RAM: +8 GB exceeds quota"]}`))
	c := newTestClient(t, srv.URL)
	err := c.do(context.Background(), http.MethodPost, "/api/v1/x", nil, map[string]any{"a": 1}, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 409 {
		t.Fatalf("want an APIError 409, got %v", err)
	}
	for _, want := range []string{"Quota exceeded", "vCPU: 10 > 8", "RAM: +8 GB exceeds quota"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err.Error(), want)
		}
	}
}

func TestAPIErrorReadsMessageField(t *testing.T) {
	srv, _ := scripted(t, status(400, `{"message":"saveAsBlueprint is not available to API tokens"}`))
	err := newTestClient(t, srv.URL).do(context.Background(), http.MethodGet, "/api/v1/x", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "saveAsBlueprint is not available") {
		t.Fatalf("got %v", err)
	}
}

func TestAPIErrorNonJSONBody(t *testing.T) {
	srv, _ := scripted(t, status(500, "<html>"+strings.Repeat("x", 2000)+"</html>", "Content-Type", "text/html"))
	err := newTestClient(t, srv.URL).do(context.Background(), http.MethodPost, "/api/v1/x", nil, nil, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want APIError, got %v", err)
	}
	if !strings.Contains(apiErr.Message, "Internal Server Error") {
		t.Fatalf("message = %q", apiErr.Message)
	}
	if len(apiErr.Body) > 512 {
		t.Fatalf("body kept %d bytes", len(apiErr.Body))
	}
}

func TestIsNotFound(t *testing.T) {
	srv, _ := scripted(t, status(404, `{"error":"VM not found"}`))
	err := newTestClient(t, srv.URL).do(context.Background(), http.MethodGet, "/api/v1/x", nil, nil, nil)
	if !IsNotFound(err) {
		t.Fatalf("IsNotFound(%v) = false", err)
	}
	if IsNotFound(errors.New("other")) {
		t.Fatal("plain error taken as not found")
	}
}

func TestDecodesSuccessBody(t *testing.T) {
	srv, _ := scripted(t, status(200, `{"data":42}`))
	var out struct {
		Data int `json:"data"`
	}
	if err := newTestClient(t, srv.URL).do(context.Background(), http.MethodGet, "/api/v1/x", nil, nil, &out); err != nil || out.Data != 42 {
		t.Fatalf("got %d, %v", out.Data, err)
	}
}

func TestRetryAfterOn429(t *testing.T) {
	srv, calls := scripted(t, status(429, `{"error":"Too many requests"}`, "Retry-After", "2"), status(200, `{}`))
	c := newTestClient(t, srv.URL)
	sleeps := recordSleeps(c)
	if err := c.do(context.Background(), http.MethodPost, "/api/v1/x", nil, map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	if *calls != 2 || len(*sleeps) != 1 || (*sleeps)[0] != 2*time.Second {
		t.Fatalf("calls=%d sleeps=%v", *calls, *sleeps)
	}
}

func TestRetryAfterDefaultWhenAbsent(t *testing.T) {
	srv, _ := scripted(t, status(429, `{}`), status(200, `{}`))
	c := newTestClient(t, srv.URL)
	sleeps := recordSleeps(c)
	if err := c.do(context.Background(), http.MethodGet, "/api/v1/x", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(*sleeps) != 1 || (*sleeps)[0] != 5*time.Second {
		t.Fatalf("sleeps=%v", *sleeps)
	}
}

func TestRetriesGatewayErrorsOnGetOnly(t *testing.T) {
	srv, calls := scripted(t, status(503, `{}`), status(503, `{}`), status(200, `{}`))
	c := newTestClient(t, srv.URL)
	sleeps := recordSleeps(c)
	if err := c.do(context.Background(), http.MethodGet, "/api/v1/x", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if *calls != 3 || len(*sleeps) != 2 || (*sleeps)[0] != time.Second || (*sleeps)[1] != 2*time.Second {
		t.Fatalf("calls=%d sleeps=%v", *calls, *sleeps)
	}

	srv2, calls2 := scripted(t, status(503, `{"error":"cluster unreachable"}`), status(200, `{}`))
	c2 := newTestClient(t, srv2.URL)
	recordSleeps(c2)
	if err := c2.do(context.Background(), http.MethodPost, "/api/v1/x", nil, nil, nil); err == nil || *calls2 != 1 {
		t.Fatalf("POST retried: calls=%d err=%v", *calls2, err)
	}
}

func TestGatewayRetriesStopAfterThree(t *testing.T) {
	srv, calls := scripted(t, status(502, `{}`))
	c := newTestClient(t, srv.URL)
	recordSleeps(c)
	if err := c.do(context.Background(), http.MethodGet, "/api/v1/x", nil, nil, nil); err == nil {
		t.Fatal("want an error")
	}
	if *calls != 4 {
		t.Fatalf("calls=%d, want 1 + 3 retries", *calls)
	}
}

func TestConcurrencyIsBounded(t *testing.T) {
	var inFlight, peak int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, srv.URL)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.do(context.Background(), http.MethodGet, "/api/v1/x", nil, nil, nil)
		}()
	}
	wg.Wait()
	if peak > MaxInFlight || peak < 2 {
		t.Fatalf("peak in flight = %d, want 2..%d", peak, MaxInFlight)
	}
}

func TestTokenNeverInErrors(t *testing.T) {
	srv, _ := scripted(t, status(401, `{"error":"Invalid token pxc_x"}`))
	err := newTestClient(t, srv.URL).do(context.Background(), http.MethodGet, "/api/v1/x", nil, nil, nil)
	if err == nil || strings.Contains(err.Error(), "pxc_x") {
		t.Fatalf("error leaks the token: %v", err)
	}
}

func TestProxCenterJSON503IsNotRetried(t *testing.T) {
	srv, calls := scripted(t, status(503, `{"error":"Cannot verify VMID uniqueness across your clusters"}`), status(200, `{}`))
	c := newTestClient(t, srv.URL)
	recordSleeps(c)
	err := c.do(context.Background(), http.MethodGet, "/api/v1/x", nil, nil, nil)
	if err == nil || *calls != 1 || !strings.Contains(err.Error(), "Cannot verify VMID uniqueness") {
		t.Fatalf("calls=%d err=%v", *calls, err)
	}
}
