package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func taskServer(t *testing.T, answers []string, seenQuery *string) *httptest.Server {
	t.Helper()
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/tasks/conn1/pve2/") {
			http.NotFound(w, r)
			return
		}
		if seenQuery != nil {
			*seenQuery = r.URL.RawQuery
		}
		i := int(atomic.AddInt32(&n, 1)) - 1
		if i >= len(answers) {
			i = len(answers) - 1
		}
		_, _ = w.Write([]byte(answers[i]))
	}))
	t.Cleanup(srv.Close)
	return srv
}

const upid = "UPID:pve2:0001:0002:0003:qmclone:120:root@pam:"

func TestWaitTaskSuccess(t *testing.T) {
	var q string
	srv := taskServer(t, []string{`{"status":"running"}`, `{"status":"running"}`, `{"status":"stopped","exitstatus":"OK"}`}, &q)
	c := newTestClient(t, srv.URL)
	recordSleeps(c)
	if err := c.WaitTask(context.Background(), "conn1", "pve2", upid); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q, "log=0") {
		t.Fatalf("query %q does not ask for the outcome only", q)
	}
}

func TestWaitTaskFailure(t *testing.T) {
	srv := taskServer(t, []string{`{"status":"stopped","exitstatus":"command 'qm start' failed","failureReason":"no space left on device"}`}, nil)
	c := newTestClient(t, srv.URL)
	recordSleeps(c)
	err := c.WaitTask(context.Background(), "conn1", "pve2", upid)
	if err == nil || !strings.Contains(err.Error(), "no space left on device") {
		t.Fatalf("got %v", err)
	}
}

func TestWaitTaskFailureWithoutReason(t *testing.T) {
	srv := taskServer(t, []string{`{"status":"stopped","exitstatus":"unexpected status"}`}, nil)
	c := newTestClient(t, srv.URL)
	recordSleeps(c)
	if err := c.WaitTask(context.Background(), "conn1", "pve2", upid); err == nil || !strings.Contains(err.Error(), "unexpected status") {
		t.Fatalf("got %v", err)
	}
}

func TestWaitTaskBackoff(t *testing.T) {
	answers := make([]string, 12)
	for i := range answers {
		answers[i] = `{"status":"running"}`
	}
	answers[11] = `{"status":"stopped","exitstatus":"OK"}`
	srv := taskServer(t, answers, nil)
	c := newTestClient(t, srv.URL)
	sleeps := recordSleeps(c)
	if err := c.WaitTask(context.Background(), "conn1", "pve2", upid); err != nil {
		t.Fatal(err)
	}
	first := (*sleeps)[0]
	if first < 800*time.Millisecond || first > 1200*time.Millisecond {
		t.Fatalf("first wait %v, want about 1 s", first)
	}
	for _, d := range *sleeps {
		if d > 6*time.Second {
			t.Fatalf("wait %v exceeds the 5 s cap with jitter", d)
		}
	}
	if last := (*sleeps)[len(*sleeps)-1]; last < 4*time.Second {
		t.Fatalf("last wait %v, the interval does not grow to 5 s", last)
	}
}

func TestWaitTaskHonoursContext(t *testing.T) {
	srv := taskServer(t, []string{`{"status":"running"}`}, nil)
	c := newTestClient(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.WaitTask(ctx, "conn1", "pve2", upid); err == nil {
		t.Fatal("cancelled context ignored")
	}
}
