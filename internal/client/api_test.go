package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type route struct {
	method, path string
	handle       func(w http.ResponseWriter, r *http.Request, body map[string]any)
}

// apiServer answers the given routes; any other call fails the test.
func apiServer(t *testing.T, routes ...route) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, rt := range routes {
			if rt.method == r.Method && rt.path == r.URL.Path {
				var body map[string]any
				if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
					if err := json.Unmarshal(raw, &body); err != nil {
						t.Errorf("body of %s %s is not JSON: %s", r.Method, r.URL.Path, raw)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				rt.handle(w, r, body)
				return
			}
		}
		t.Errorf("unexpected call %s %s", r.Method, r.URL.String())
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func write(w http.ResponseWriter, code int, body string) {
	w.WriteHeader(code)
	_, _ = io.WriteString(w, body)
}

// taskAware answers every task status under /api/v1/tasks/conn1/<node>/ with OK.
func taskAware(t *testing.T, node string, seen *int32, routes ...route) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/tasks/conn1/"+node+"/") {
			atomic.AddInt32(seen, 1)
			write(w, 200, `{"status":"stopped","exitstatus":"OK"}`)
			return
		}
		for _, rt := range routes {
			if rt.method == r.Method && rt.path == r.URL.Path {
				var body map[string]any
				if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
					_ = json.Unmarshal(raw, &body)
				}
				rt.handle(w, r, body)
				return
			}
		}
		t.Errorf("unexpected call %s %s", r.Method, r.URL.String())
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNextID(t *testing.T) {
	srv := apiServer(t, route{http.MethodGet, "/api/v1/connections/conn1/cluster/nextid", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		write(w, 200, `{"data":9105}`)
	}})
	id, err := newTestClient(t, srv.URL).NextID(context.Background(), "conn1")
	if err != nil || id != 9105 {
		t.Fatalf("got %d, %v", id, err)
	}
}

func TestNextID503IsNotAClash(t *testing.T) {
	srv := apiServer(t, route{http.MethodGet, "/api/v1/connections/conn1/cluster/nextid", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		write(w, 503, `{"error":"Cannot verify VMID uniqueness across your clusters: connection(s) unreachable: B"}`)
	}})
	c := newTestClient(t, srv.URL)
	recordSleeps(c)
	_, err := c.NextID(context.Background(), "conn1")
	if err == nil || IsVMIDClash(err) || !strings.Contains(err.Error(), "Cannot verify VMID uniqueness") {
		t.Fatalf("got %v", err)
	}
}

func TestIsVMIDClash(t *testing.T) {
	for _, msg := range []string{"VMID 105 is already in use in your infrastructure", "unable to create VM 105 - VM 105 already exists on node 'pve1'"} {
		if !IsVMIDClash(&APIError{Status: 409, Message: msg}) {
			t.Fatalf("%q not seen as a clash", msg)
		}
		if !IsVMIDClashText(msg) {
			t.Fatalf("text %q not seen as a clash", msg)
		}
	}
	if IsVMIDClash(&APIError{Status: 409, Message: "Quota exceeded"}) {
		t.Fatal("quota refusal seen as a clash")
	}
}

func TestDeploySendsStartFalseAndHardware(t *testing.T) {
	var got map[string]any
	srv := apiServer(t, route{http.MethodPost, "/api/v1/templates/deploy", func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		got = body
		write(w, 200, `{"data":{"deploymentId":"dep1","status":"pending","vmid":9105}}`)
	}})
	vlan := int64(120)
	res, err := newTestClient(t, srv.URL).Deploy(context.Background(), DeployRequest{
		ConnectionID: "conn1", Node: "pve2", Storage: "ceph-gold", VMID: 9105, VMName: "web-01", ImageSlug: "ubuntu-24.04",
		Start:     false,
		Hardware:  DeployHardware{Cores: 4, Sockets: 1, Memory: 8192, DiskSize: "64G", DiskBus: "scsi", NetworkBridge: "vnet-prod", VLANTag: &vlan, NetworkModel: "virtio"},
		CloudInit: &DeployCloudInit{CIUser: "ops", SSHKeys: "ssh-ed25519 AAAA ops", IPConfig0: "ip=dhcp"},
	})
	if err != nil || res.DeploymentID != "dep1" || res.VMID != 9105 {
		t.Fatalf("got %+v, %v", res, err)
	}
	if got["start"] != false {
		t.Fatalf("start = %v, must be sent as false", got["start"])
	}
	hw := got["hardware"].(map[string]any)
	if hw["diskSize"] != "64G" || hw["vlanTag"] != float64(120) || hw["diskBus"] != "scsi" {
		t.Fatalf("hardware = %v", hw)
	}
	ci := got["cloudInit"].(map[string]any)
	if _, has := ci["cipassword"]; has {
		t.Fatal("empty password sent")
	}
}

func TestGetDeployment(t *testing.T) {
	srv := apiServer(t, route{http.MethodGet, "/api/v1/templates/deployments/dep1", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		write(w, 200, `{"data":{"id":"dep1","status":"failed","currentStep":"configuring","error":"boom","connectionId":"conn1","node":"pve2","vmid":9105}}`)
	}})
	d, err := newTestClient(t, srv.URL).GetDeployment(context.Background(), "dep1")
	if err != nil || d.Status != "failed" || d.Error != "boom" || d.VMID != 9105 || d.Node != "pve2" {
		t.Fatalf("got %+v, %v", d, err)
	}
}

func TestCloneWaitsItsTask(t *testing.T) {
	var tasks int32
	var got map[string]any
	srv := taskAware(t, "pve1", &tasks, route{http.MethodPost, "/api/v1/connections/conn1/guests/qemu/pve1/9000/clone", func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		got = body
		write(w, 200, `{"data":"UPID:pve1:1:2:3:qmclone:9000:x:"}`)
	}})
	c := newTestClient(t, srv.URL)
	recordSleeps(c)
	if err := c.Clone(context.Background(), "conn1", "pve1", GuestQEMU, 9000, CloneRequest{NewID: 9106, Name: "web-02", Full: true, Target: "pve2"}); err != nil {
		t.Fatal(err)
	}
	if tasks == 0 || got["newid"] != float64(9106) || got["full"] != true || got["target"] != "pve2" {
		t.Fatalf("tasks=%d body=%v", tasks, got)
	}
}

func TestGetConfigSplitsPendingAndProvisioning(t *testing.T) {
	srv := apiServer(t, route{http.MethodGet, "/api/v1/connections/conn1/guests/qemu/pve1/9106/config", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		write(w, 200, `{"data":{"cores":4,"memory":"8192","pending":{"cores":8}},"movedTo":"pve3","provisioning":{"state":"running","failures":[]}}`)
	}})
	cfg, err := newTestClient(t, srv.URL).GetConfig(context.Background(), "conn1", "pve1", GuestQEMU, 9106)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MovedTo != "pve3" || cfg.Provisioning == nil || cfg.Provisioning.State != "running" {
		t.Fatalf("got %+v", cfg)
	}
	if _, has := cfg.Data["pending"]; has || cfg.Pending["cores"] != float64(8) {
		t.Fatalf("pending not split: data=%v pending=%v", cfg.Data, cfg.Pending)
	}
}

func TestGetConfigNotFound(t *testing.T) {
	srv := apiServer(t, route{http.MethodGet, "/api/v1/connections/conn1/guests/qemu/pve1/9106/config", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		write(w, 404, `{"error":"VM not found"}`)
	}})
	_, err := newTestClient(t, srv.URL).GetConfig(context.Background(), "conn1", "pve1", GuestQEMU, 9106)
	if !IsNotFound(err) {
		t.Fatalf("got %v", err)
	}
}

func TestUpdateConfigWaits202Pending(t *testing.T) {
	var tasks int32
	srv := taskAware(t, "pve3", &tasks, route{http.MethodPut, "/api/v1/connections/conn1/guests/qemu/pve1/9106/config", func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		if body["cores"] != float64(8) {
			t.Errorf("body = %v", body)
		}
		write(w, 202, `{"data":"UPID:pve3:x","pending":true,"upid":"UPID:pve3:x","node":"pve3"}`)
	}})
	c := newTestClient(t, srv.URL)
	recordSleeps(c)
	if err := c.UpdateConfig(context.Background(), "conn1", "pve1", GuestQEMU, 9106, map[string]any{"cores": 8}); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 {
		t.Fatalf("task polled %d times on pve3", tasks)
	}
}

func TestUpdateConfig200NoTask(t *testing.T) {
	var tasks int32
	srv := taskAware(t, "pve1", &tasks, route{http.MethodPut, "/api/v1/connections/conn1/guests/qemu/pve1/9106/config", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		write(w, 200, `{"data":null,"success":true}`)
	}})
	if err := newTestClient(t, srv.URL).UpdateConfig(context.Background(), "conn1", "pve1", GuestQEMU, 9106, map[string]any{"name": "x"}); err != nil {
		t.Fatal(err)
	}
	if tasks != 0 {
		t.Fatal("polled a task that does not exist")
	}
}

func TestResizeMovePowerDeleteWait(t *testing.T) {
	var tasks int32
	var deleteQuery, resizeBody string
	base := "/api/v1/connections/conn1/guests/qemu/pve1/9106"
	srv := taskAware(t, "pve1", &tasks,
		route{http.MethodPost, base + "/disk/resize", func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
			resizeBody = body["disk"].(string) + "=" + body["size"].(string)
			write(w, 200, `{"data":"UPID:pve1:r"}`)
		}},
		route{http.MethodPost, base + "/disk/move", func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
			if body["storage"] != "ceph-silver" || body["deleteSource"] != true {
				t.Errorf("move body = %v", body)
			}
			write(w, 200, `{"data":"UPID:pve1:m"}`)
		}},
		route{http.MethodPost, base + "/start", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			write(w, 200, `{"data":"UPID:pve1:s","node":"pve1"}`)
		}},
		route{http.MethodDelete, base, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			deleteQuery = r.URL.RawQuery
			write(w, 200, `{"data":"UPID:pve1:d"}`)
		}},
	)
	c := newTestClient(t, srv.URL)
	recordSleeps(c)
	ctx := context.Background()
	if err := c.ResizeDisk(ctx, "conn1", "pve1", GuestQEMU, 9106, "scsi0", "80G"); err != nil {
		t.Fatal(err)
	}
	if err := c.MoveDisk(ctx, "conn1", "pve1", GuestQEMU, 9106, "scsi0", "ceph-silver"); err != nil {
		t.Fatal(err)
	}
	if err := c.Power(ctx, "conn1", "pve1", GuestQEMU, 9106, "start"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteVM(ctx, "conn1", "pve1", GuestQEMU, 9106); err != nil {
		t.Fatal(err)
	}
	if tasks != 4 || resizeBody != "scsi0=80G" {
		t.Fatalf("tasks=%d resize=%q", tasks, resizeBody)
	}
	if !strings.Contains(deleteQuery, "purge=1") || !strings.Contains(deleteQuery, "destroy-unreferenced-disks=1") {
		t.Fatalf("delete query = %q", deleteQuery)
	}
}

func vmListRoute(calls *int32, body string) route {
	return route{http.MethodGet, "/api/v1/vms", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		atomic.AddInt32(calls, 1)
		if r.URL.Query().Get("connId") != "conn1" {
			write(w, 400, `{"error":"connId"}`)
			return
		}
		write(w, 200, body)
	}}
}

const twoVMs = `{"data":{"vms":[{"connId":"conn1","node":"pve1","vmid":"9106","name":"web-02","type":"qemu","status":"running","template":false,"ips":["10.15.15.20"]},{"connId":"conn1","node":"pve2","vmid":9000,"name":"tpl","type":"qemu","status":"stopped","template":true}]}}`

func TestListVMsFiltersByVmids(t *testing.T) {
	var calls int32
	var query string
	srv := apiServer(t, route{http.MethodGet, "/api/v1/vms", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		atomic.AddInt32(&calls, 1)
		query = r.URL.RawQuery
		write(w, 200, twoVMs)
	}})
	vms, err := newTestClient(t, srv.URL).ListVMs(context.Background(), "conn1", []int{9000, 9106})
	if err != nil || len(vms) != 2 || vms[1].VMID != 9000 || !vms[1].Template || vms[0].Status != "running" {
		t.Fatalf("got %+v, %v", vms, err)
	}
	if !strings.Contains(query, "vmids=9000%2C9106") && !strings.Contains(query, "vmids=9000,9106") {
		t.Fatalf("query = %q", query)
	}
}

func TestCachedVMSharesOneList(t *testing.T) {
	var calls int32
	srv := apiServer(t, vmListRoute(&calls, twoVMs))
	c := newTestClient(t, srv.URL)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			vm, err := c.CachedVM(context.Background(), "conn1", 9106)
			if err != nil || vm == nil || vm.Name != "web-02" {
				t.Errorf("got %+v, %v", vm, err)
			}
		}()
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("list read %d times", calls)
	}
	if vm, err := c.CachedVM(context.Background(), "conn1", 4242); err != nil || vm != nil {
		t.Fatalf("absent VM: %+v, %v", vm, err)
	}
}

func TestWriteInvalidatesVMList(t *testing.T) {
	var calls, tasks int32
	srv := taskAware(t, "pve1", &tasks, vmListRoute(&calls, twoVMs),
		route{http.MethodPost, "/api/v1/connections/conn1/guests/qemu/pve1/9106/shutdown", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			write(w, 200, `{"data":"UPID:pve1:s"}`)
		}})
	c := newTestClient(t, srv.URL)
	recordSleeps(c)
	ctx := context.Background()
	_, _ = c.CachedVM(ctx, "conn1", 9106)
	if err := c.Power(ctx, "conn1", "pve1", GuestQEMU, 9106, "shutdown"); err != nil {
		t.Fatal(err)
	}
	_, _ = c.CachedVM(ctx, "conn1", 9106)
	if calls != 2 {
		t.Fatalf("list read %d times, want a fresh read after a write", calls)
	}
}

func TestCatalogBlueprintsVDCsStoragesVNets(t *testing.T) {
	srv := apiServer(t,
		route{http.MethodGet, "/api/v1/templates/catalog", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			write(w, 200, `{"data":{"images":[{"slug":"ubuntu-24.04","name":"Ubuntu 24.04","vendor":"ubuntu","version":"24.04","format":"qcow2","isCustom":false}],"vendors":[],"meta":{}}}`)
		}},
		route{http.MethodGet, "/api/v1/templates/blueprints", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			write(w, 200, `{"data":[{"id":"bp1","name":"web","imageSlug":"ubuntu-24.04","description":null}]}`)
		}},
		route{http.MethodGet, "/api/v1/templates/blueprints/bp1", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			write(w, 200, `{"data":{"id":"bp1","name":"web","imageSlug":"ubuntu-24.04"}}`)
		}},
		route{http.MethodGet, "/api/v1/vdcs", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			write(w, 200, `{"data":[{"id":"vdc1","tenantId":"t1","connectionId":"conn1","name":"prod","pvePoolName":"vdc-prod","nodes":["pve1","pve2"],"storages":["ceph-gold"],"primaryStorage":"ceph-gold","quota":{"maxVcpus":32,"maxRamMb":65536,"maxStorageMb":null,"maxVms":7},"usage":{"usedVcpus":4,"usedRamMb":8192,"usedStorageMb":65536,"usedVms":1}}]}`)
		}},
		route{http.MethodGet, "/api/v1/connections/conn1/storage", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			write(w, 200, `{"data":[{"storage":"ceph-gold","type":"rbd","shared":true,"nodes":["pve1","pve2"],"content":["images"],"free":100,"total":200}]}`)
		}},
		route{http.MethodGet, "/api/v1/vdcs/vdc1/vnets", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			write(w, 200, `{"data":[{"id":"v1","pveName":"v6bc065f","displayName":"prod-lan","tag":120,"type":"vlan","bridge":"vmbr0","subnet":{"cidr":"10.15.15.0/24","gateway":"10.15.15.1","ipamEnabled":true}}]}`)
		}},
	)
	c := newTestClient(t, srv.URL)
	ctx := context.Background()
	images, err := c.Catalog(ctx)
	if err != nil || len(images) != 1 || images[0].Slug != "ubuntu-24.04" {
		t.Fatalf("catalog %+v, %v", images, err)
	}
	bps, err := c.Blueprints(ctx)
	if err != nil || len(bps) != 1 || bps[0].ImageSlug != "ubuntu-24.04" {
		t.Fatalf("blueprints %+v, %v", bps, err)
	}
	if bp, err := c.Blueprint(ctx, "bp1"); err != nil || bp.ImageSlug != "ubuntu-24.04" {
		t.Fatalf("blueprint %+v, %v", bp, err)
	}
	vdcs, err := c.ListVDCs(ctx)
	if err != nil || len(vdcs) != 1 || vdcs[0].Quota == nil || vdcs[0].Quota.MaxVMs == nil || *vdcs[0].Quota.MaxVMs != 7 || vdcs[0].Quota.MaxStorageMB != nil || vdcs[0].Usage.UsedVMs != 1 {
		t.Fatalf("vdcs %+v, %v", vdcs, err)
	}
	st, err := c.Storages(ctx, "conn1")
	if err != nil || len(st) != 1 || !st[0].Shared || st[0].Free != 100 {
		t.Fatalf("storages %+v, %v", st, err)
	}
	vnets, err := c.VNets(ctx, "vdc1")
	if err != nil || len(vnets) != 1 || vnets[0].Subnet == nil || !vnets[0].Subnet.IPAMEnabled || vnets[0].DisplayName != "prod-lan" {
		t.Fatalf("vnets %+v, %v", vnets, err)
	}
}
