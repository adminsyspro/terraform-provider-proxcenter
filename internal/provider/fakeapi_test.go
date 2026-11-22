package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// fakeVM is a guest of the fake ProxCenter.
type fakeVM struct {
	Node    string
	Pool    string
	Status  string
	Config  map[string]any
	Pending map[string]any
	// Provisioning answers, one per config read, then nothing.
	Provisioning []string
	Failures     []string
}

// fakeAPI is an in-memory ProxCenter public API, enough for the provider.
type fakeAPI struct {
	t   *testing.T
	srv *httptest.Server

	mu          sync.Mutex
	version     string
	vdcs        []map[string]any
	vnets       map[string][]map[string]any
	storages    map[string][]map[string]any
	images      []map[string]any
	blueprints  []map[string]any
	vms         map[int]*fakeVM
	deployments map[string]map[string]any
	nextIDs     []int
	nextIDCalls int
	// cloneProvisioning and cloneFailures seed every new clone.
	cloneProvisioning []string
	cloneFailures     []string
	// pendingKeys land in Pending instead of Config while the VM runs.
	pendingKeys map[string]bool
	calls       []string
	bodies      map[string][]map[string]any
	// hooks answer a call before the default handler; key "METHOD /path".
	hooks map[string]func(w http.ResponseWriter, r *http.Request, body map[string]any) bool
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{
		t:           t,
		version:     "1.5.0",
		vnets:       map[string][]map[string]any{},
		storages:    map[string][]map[string]any{},
		vms:         map[int]*fakeVM{},
		deployments: map[string]map[string]any{},
		nextIDs:     []int{9105, 9106, 9107, 9108, 9109, 9110, 9111},
		pendingKeys: map[string]bool{},
		bodies:      map[string][]map[string]any{},
		hooks:       map[string]func(http.ResponseWriter, *http.Request, map[string]any) bool{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) providerFactories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"proxcenter": providerserver.NewProtocol6WithError(New("test")()),
	}
}

// config prefixes HCL with a provider block pointing at the fake.
func (f *fakeAPI) config(hcl string) string {
	return fmt.Sprintf("provider \"proxcenter\" {\n  endpoint = %q\n  token    = \"pxc_test\"\n}\n\n%s", f.srv.URL, hcl)
}

// hook sets (or, with nil, removes) the handler of one route.
func (f *fakeAPI) hook(method, path string, h func(w http.ResponseWriter, r *http.Request, body map[string]any) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if h == nil {
		delete(f.hooks, method+" "+path)
		return
	}
	f.hooks[method+" "+path] = h
}

// count returns how many calls matched "METHOD /path" (path may end with *).
func (f *fakeAPI) count(pattern string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == pattern || (strings.HasSuffix(pattern, "*") && strings.HasPrefix(c, strings.TrimSuffix(pattern, "*"))) {
			n++
		}
	}
	return n
}

// lastBody is the last JSON body sent to "METHOD /path".
func (f *fakeAPI) lastBody(key string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := f.bodies[key]
	if len(b) == 0 {
		return nil
	}
	return b[len(b)-1]
}

func (f *fakeAPI) allBodies(key string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.bodies[key]...)
}

// withTemplate seeds template 9000 on pve1, as on the lab.
func (f *fakeAPI) withTemplate() *fakeAPI {
	f.addVM(9000, &fakeVM{Node: "pve1", Status: "stopped", Config: map[string]any{
		"name": "tpl-debian", "template": "1", "cores": float64(1), "sockets": float64(1), "memory": "1024", "cpu": "host",
		"scsi0": "ceph-gold:base-9000-disk-0,size=8G", "net0": "virtio=BC:24:11:00:90:00,bridge=vmbr0,firewall=1",
		"ide2": "ceph-gold:vm-9000-cloudinit,media=cdrom", "ipconfig0": "ip=dhcp",
	}})
	return f
}

// noVMs is a CheckDestroy asserting the fake holds no VM but the seeded ones.
func (f *fakeAPI) noVMsBut(keep ...int) func(*terraform.State) error {
	return func(*terraform.State) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		for id := range f.vms {
			if !slices.Contains(keep, id) {
				return fmt.Errorf("VM %d still exists after destroy", id)
			}
		}
		return nil
	}
}

func (f *fakeAPI) vm(vmid int) *fakeVM {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.vms[vmid]
}

func (f *fakeAPI) addVM(vmid int, vm *fakeVM) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if vm.Pending == nil {
		vm.Pending = map[string]any{}
	}
	f.vms[vmid] = vm
}

// withVDC seeds the MSP vDC of the lab bench.
func (f *fakeAPI) withVDC() *fakeAPI {
	f.vdcs = append(f.vdcs, map[string]any{
		"id": "vdc1", "tenantId": "t1", "connectionId": "conn1", "name": "prod", "pvePoolName": "vdc-prod",
		"nodes": []string{"pve1", "pve2"}, "storages": []string{"ceph-gold", "ceph-silver"}, "primaryStorage": "ceph-gold",
		"quota": map[string]any{"maxVcpus": 32, "maxRamMb": 65536, "maxStorageMb": nil, "maxVms": 7},
		"usage": map[string]any{"usedVcpus": 4, "usedRamMb": 8192, "usedStorageMb": 65536, "usedVms": 1},
	})
	f.vnets["vdc1"] = []map[string]any{{"id": "vn1", "pveName": "v6bc065f", "displayName": "prod-lan", "tag": 120, "type": "vlan", "bridge": "vmbr0",
		"subnet": map[string]any{"cidr": "10.15.15.0/24", "gateway": "10.15.15.1", "ipamEnabled": true}}}
	f.storages["conn1"] = []map[string]any{
		{"storage": "ceph-gold", "type": "rbd", "shared": true, "nodes": []string{"pve1", "pve2"}, "content": []string{"images"}, "free": 100 << 30, "total": 200 << 30},
		{"storage": "ceph-silver", "type": "rbd", "shared": true, "nodes": []string{"pve1", "pve2"}, "content": []string{"images"}, "free": 50 << 30, "total": 100 << 30},
	}
	f.images = []map[string]any{
		{"slug": "ubuntu-24.04", "name": "Ubuntu 24.04", "vendor": "ubuntu", "version": "24.04", "format": "qcow2", "isCustom": false},
		{"slug": "debian-12", "name": "Debian 12", "vendor": "debian", "version": "12", "format": "qcow2", "isCustom": false},
	}
	f.blueprints = []map[string]any{{"id": "bp1", "name": "web", "imageSlug": "debian-12", "description": "Web tier"}}
	return f
}

var (
	reGuest   = regexp.MustCompile(`^/api/v1/connections/([^/]+)/guests/qemu/([^/]+)/(\d+)(/.*)?$`)
	reNextID  = regexp.MustCompile(`^/api/v1/connections/([^/]+)/cluster/nextid$`)
	reStorage = regexp.MustCompile(`^/api/v1/connections/([^/]+)/storage$`)
	reVNets   = regexp.MustCompile(`^/api/v1/vdcs/([^/]+)/vnets$`)
	reDeploy  = regexp.MustCompile(`^/api/v1/templates/deployments/([^/]+)$`)
	reBP      = regexp.MustCompile(`^/api/v1/templates/blueprints/([^/]+)$`)
)

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ProxCenter-Version", "1.5.0")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func errorReply(w http.ResponseWriter, code int, msg string) {
	reply(w, code, map[string]any{"error": msg})
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			f.t.Errorf("non JSON body for %s %s", r.Method, r.URL.Path)
		}
	}
	if r.Header.Get("Authorization") != "Bearer pxc_test" {
		errorReply(w, 401, "Unauthorized")
		return
	}
	key := r.Method + " " + r.URL.Path
	f.mu.Lock()
	f.calls = append(f.calls, key)
	if body != nil {
		f.bodies[key] = append(f.bodies[key], body)
	}
	h := f.hooks[key]
	f.mu.Unlock()
	if h != nil && h(w, r, body) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.route(w, r, body)
}

// route runs with f.mu held.
func (f *fakeAPI) route(w http.ResponseWriter, r *http.Request, body map[string]any) {
	p, q := r.URL.Path, r.URL.Query()
	switch {
	case p == "/api/v1/public/health":
		w.Header().Set("ProxCenter-Version", f.version)
		reply(w, 200, map[string]any{"data": map[string]any{"status": "ok"}})
	case strings.HasPrefix(p, "/api/v1/tasks/"):
		reply(w, 200, map[string]any{"status": "stopped", "exitstatus": "OK"})
	case p == "/api/v1/vdcs":
		reply(w, 200, map[string]any{"data": f.vdcs})
	case reVNets.MatchString(p):
		reply(w, 200, map[string]any{"data": f.vnets[reVNets.FindStringSubmatch(p)[1]]})
	case reStorage.MatchString(p):
		reply(w, 200, map[string]any{"data": f.storages[reStorage.FindStringSubmatch(p)[1]]})
	case p == "/api/v1/templates/catalog":
		reply(w, 200, map[string]any{"data": map[string]any{"images": f.images, "vendors": []any{}, "meta": map[string]any{}}})
	case p == "/api/v1/templates/blueprints":
		reply(w, 200, map[string]any{"data": f.blueprints})
	case reBP.MatchString(p):
		id := reBP.FindStringSubmatch(p)[1]
		for _, bp := range f.blueprints {
			if bp["id"] == id {
				reply(w, 200, map[string]any{"data": bp})
				return
			}
		}
		errorReply(w, 404, "Blueprint not found")
	case p == "/api/v1/vms":
		f.listVMs(w, q)
	case reNextID.MatchString(p):
		f.nextIDCalls++
		if len(f.nextIDs) == 0 {
			errorReply(w, 503, "no free VMID")
			return
		}
		id := f.nextIDs[0]
		f.nextIDs = f.nextIDs[1:]
		reply(w, 200, map[string]any{"data": id})
	case p == "/api/v1/templates/deploy" && r.Method == http.MethodPost:
		f.deploy(w, body)
	case reDeploy.MatchString(p):
		d, ok := f.deployments[reDeploy.FindStringSubmatch(p)[1]]
		if !ok {
			errorReply(w, 404, "Deployment not found")
			return
		}
		reply(w, 200, map[string]any{"data": d})
	case reGuest.MatchString(p):
		m := reGuest.FindStringSubmatch(p)
		vmid, _ := strconv.Atoi(m[3])
		f.guest(w, r, m[2], vmid, m[4], body)
	default:
		f.t.Errorf("fake API: unexpected %s %s", r.Method, r.URL.String())
		errorReply(w, 404, "no route")
	}
}

func (f *fakeAPI) listVMs(w http.ResponseWriter, q url.Values) {
	want := map[string]bool{}
	for _, id := range strings.Split(q.Get("vmids"), ",") {
		if id != "" {
			want[id] = true
		}
	}
	ids := make([]int, 0, len(f.vms))
	for id := range f.vms {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	out := []map[string]any{}
	for _, id := range ids {
		vm := f.vms[id]
		if len(want) > 0 && !want[strconv.Itoa(id)] {
			continue
		}
		name, _ := vm.Config["name"].(string)
		out = append(out, map[string]any{"connId": "conn1", "node": vm.Node, "vmid": strconv.Itoa(id), "name": name, "type": "qemu",
			"status": vm.Status, "template": vm.Config["template"] == "1"})
	}
	reply(w, 200, map[string]any{"data": map[string]any{"vms": out}})
}

func (f *fakeAPI) deploy(w http.ResponseWriter, body map[string]any) {
	vmid := int(body["vmid"].(float64))
	if _, taken := f.vms[vmid]; taken {
		errorReply(w, 409, fmt.Sprintf("VMID %d is already in use in your infrastructure", vmid))
		return
	}
	hw, _ := body["hardware"].(map[string]any)
	bus, _ := hw["diskBus"].(string)
	if bus == "" {
		bus = "scsi"
	}
	model, _ := hw["networkModel"].(string)
	if model == "" {
		model = "virtio"
	}
	net := fmt.Sprintf("%s=BC:24:11:00:%02X:%02X,bridge=%s", model, vmid/256%256, vmid%256, hw["networkBridge"])
	if tag, ok := hw["vlanTag"].(float64); ok {
		net += fmt.Sprintf(",tag=%d", int(tag))
	}
	name, _ := body["vmName"].(string)
	cfg := map[string]any{
		"name": name, "cores": hw["cores"], "sockets": hw["sockets"], "memory": fmt.Sprint(hw["memory"]), "cpu": "x86-64-v2-AES",
		bus + "0": fmt.Sprintf("%s:vm-%d-disk-0,size=%s", body["storage"], vmid, hw["diskSize"]),
		"net0":    net + ",firewall=1",
		"ide2":    fmt.Sprintf("%s:vm-%d-cloudinit,media=cdrom", body["storage"], vmid),
		"boot":    "order=" + bus + "0",
	}
	if ci, ok := body["cloudInit"].(map[string]any); ok {
		if v, ok := ci["ciuser"].(string); ok {
			cfg["ciuser"] = v
		}
		if v, ok := ci["sshKeys"].(string); ok {
			cfg["sshkeys"] = url.PathEscape(v)
		}
		ip, _ := ci["ipconfig0"].(string)
		if ip == "" {
			ip = "ip=dhcp"
		}
		cfg["ipconfig0"] = ip
		for _, k := range []string{"nameserver", "searchdomain"} {
			if v, ok := ci[k].(string); ok {
				cfg[k] = v
			}
		}
	}
	status := "stopped"
	if start, _ := body["start"].(bool); start {
		status = "running"
	}
	f.vms[vmid] = &fakeVM{Node: body["node"].(string), Status: status, Config: cfg, Pending: map[string]any{}}
	id := fmt.Sprintf("dep-%d", vmid)
	f.deployments[id] = map[string]any{"id": id, "status": "completed", "connectionId": body["connectionId"], "node": body["node"], "vmid": vmid}
	reply(w, 200, map[string]any{"data": map[string]any{"deploymentId": id, "status": "pending", "vmid": vmid}})
}

var diskSizeRe = regexp.MustCompile(`size=[0-9.]+[KMGT]?`)

func (f *fakeAPI) guest(w http.ResponseWriter, r *http.Request, node string, vmid int, rest string, body map[string]any) {
	vm, ok := f.vms[vmid]
	if !ok {
		errorReply(w, 404, fmt.Sprintf("VM %d not found", vmid))
		return
	}
	upid := map[string]any{"data": fmt.Sprintf("UPID:%s:%d", vm.Node, vmid), "node": vm.Node}
	switch {
	case rest == "/config" && r.Method == http.MethodGet:
		data := map[string]any{"digest": "d1"}
		for k, v := range vm.Config {
			data[k] = v
		}
		if len(vm.Pending) > 0 {
			data["pending"] = vm.Pending
		}
		out := map[string]any{"data": data}
		if vm.Node != node {
			out["movedTo"] = vm.Node
		}
		if len(vm.Provisioning) > 0 {
			out["provisioning"] = map[string]any{"state": vm.Provisioning[0], "failures": vm.Failures}
			if vm.Provisioning[0] == "running" {
				vm.Provisioning = vm.Provisioning[1:]
			}
		}
		reply(w, 200, out)
	case rest == "/config" && r.Method == http.MethodPut:
		for k, v := range body {
			switch {
			case k == "delete":
				for _, d := range strings.Split(fmt.Sprint(v), ",") {
					delete(vm.Config, d)
				}
			case k == "digest":
			case k == "sshkeys":
				vm.Config[k] = url.PathEscape(fmt.Sprint(v))
			case vm.Status == "running" && f.pendingKeys[k]:
				vm.Pending[k] = v
			default:
				vm.Config[k] = normalizeNewDisk(k, v, vmid)
			}
		}
		reply(w, 200, map[string]any{"data": nil, "success": true})
	case rest == "/disk/resize":
		disk := body["disk"].(string)
		cur := fmt.Sprint(vm.Config[disk])
		vm.Config[disk] = diskSizeRe.ReplaceAllString(cur, "size="+body["size"].(string))
		reply(w, 200, upid)
	case rest == "/disk/move":
		disk := body["disk"].(string)
		cur := fmt.Sprint(vm.Config[disk])
		vm.Config[disk] = body["storage"].(string) + cur[strings.Index(cur, ":"):]
		reply(w, 200, upid)
	case rest == "/clone":
		newid := int(body["newid"].(float64))
		if _, taken := f.vms[newid]; taken {
			errorReply(w, 500, fmt.Sprintf("unable to create VM %d - VM %d already exists on node '%s'", newid, newid, vm.Node))
			return
		}
		cfg := map[string]any{}
		for k, v := range vm.Config {
			if s, ok := v.(string); ok && strings.Contains(s, fmt.Sprintf("vm-%d-", vmid)) {
				v = strings.ReplaceAll(s, fmt.Sprintf("vm-%d-", vmid), fmt.Sprintf("vm-%d-", newid))
			}
			cfg[k] = v
		}
		delete(cfg, "template")
		if n, ok := body["name"].(string); ok {
			cfg["name"] = n
		}
		target := vm.Node
		if t, ok := body["target"].(string); ok && t != "" {
			target = t
		}
		f.vms[newid] = &fakeVM{Node: target, Status: "stopped", Config: cfg, Pending: map[string]any{},
			Provisioning: append([]string(nil), f.cloneProvisioning...), Failures: f.cloneFailures}
		reply(w, 200, upid)
	case rest == "" && r.Method == http.MethodDelete:
		if vm.Status == "running" {
			errorReply(w, 409, "VM is running")
			return
		}
		delete(f.vms, vmid)
		reply(w, 200, upid)
	case r.Method == http.MethodPost && (rest == "/start" || rest == "/stop" || rest == "/shutdown" || rest == "/reboot"):
		switch rest {
		case "/start":
			vm.Status = "running"
		case "/stop", "/shutdown":
			vm.Status = "stopped"
		}
		if rest == "/reboot" || rest == "/start" {
			for k, v := range vm.Pending {
				vm.Config[k] = v
			}
			vm.Pending = map[string]any{}
		}
		reply(w, 200, upid)
	default:
		f.t.Errorf("fake API: unexpected guest call %s %s", r.Method, r.URL.Path)
		errorReply(w, 404, "no route")
	}
}

// normalizeNewDisk turns a new disk "<storage>:<gb>" into what Proxmox stores.
func normalizeNewDisk(key string, v any, vmid int) any {
	s, ok := v.(string)
	if !ok || !regexp.MustCompile(`^(scsi|virtio|sata|ide)\d+$`).MatchString(key) {
		return v
	}
	if m := regexp.MustCompile(`^([^:,]+):(\d+)(,.*)?$`).FindStringSubmatch(s); m != nil {
		return fmt.Sprintf("%s:vm-%d-disk-%s,size=%sG%s", m[1], vmid, key, m[2], m[3])
	}
	return v
}
