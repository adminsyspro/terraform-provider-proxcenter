package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Every guest call names its type in the path: a container is /guests/lxc/.
func TestGuestPathsCarryTheType(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/tasks/"):
			write(w, 200, `{"status":"stopped","exitstatus":"OK"}`)
		case strings.HasSuffix(r.URL.Path, "/status"):
			write(w, 200, `{"data":{"status":"running"}}`)
		case strings.HasSuffix(r.URL.Path, "/config") && r.Method == http.MethodGet:
			write(w, 200, `{"data":{"hostname":"ct"}}`)
		case strings.HasSuffix(r.URL.Path, "/config"):
			write(w, 200, `{"data":null}`)
		default:
			write(w, 200, `{"data":"UPID:pve1:x"}`)
		}
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, srv.URL)
	recordSleeps(c)
	ctx := context.Background()
	_, _ = c.GetConfig(ctx, "conn1", "pve1", GuestLXC, 130)
	_, _ = c.GuestStatus(ctx, "conn1", "pve1", GuestLXC, 130)
	_ = c.UpdateConfig(ctx, "conn1", "pve1", GuestLXC, 130, map[string]any{"cores": 2})
	_ = c.ResizeDisk(ctx, "conn1", "pve1", GuestLXC, 130, "rootfs", "10G")
	_ = c.MoveDisk(ctx, "conn1", "pve1", GuestLXC, 130, "rootfs", "ceph")
	_ = c.Power(ctx, "conn1", "pve1", GuestLXC, 130, "start")
	_ = c.DeleteVM(ctx, "conn1", "pve1", GuestLXC, 130)
	_, _, _ = c.StartClone(ctx, "conn1", "pve1", GuestLXC, 9001, CloneRequest{NewID: 131})
	for _, p := range paths {
		if strings.Contains(p, "/guests/") && !strings.Contains(p, "/guests/lxc/pve1/") {
			t.Errorf("%s does not name the container type", p)
		}
	}
	if len(paths) < 8 {
		t.Fatalf("only %d calls seen", len(paths))
	}
}

func TestCreateGuestWaitsItsTask(t *testing.T) {
	var tasks int32
	var got map[string]any
	srv := taskAware(t, "pve1", &tasks, route{http.MethodPost, "/api/v1/connections/conn1/guests/lxc/pve1", func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		got = body
		write(w, 200, `{"data":"UPID:pve1:1:2:3:vzcreate:130:x:"}`)
	}})
	c := newTestClient(t, srv.URL)
	recordSleeps(c)
	err := c.CreateGuest(context.Background(), "conn1", "pve1", GuestLXC, map[string]any{"vmid": 130, "ostemplate": "local:vztmpl/alpine.tar.xz"})
	if err != nil || tasks != 1 || got["ostemplate"] != "local:vztmpl/alpine.tar.xz" {
		t.Fatalf("err=%v tasks=%d body=%v", err, tasks, got)
	}
}
