package provider

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// stillThere asserts a VM the provider must never touch survived.
func (f *fakeAPI) stillThere(vmid int, name string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		vm := f.vm(vmid)
		if vm == nil || vm.Config["name"] != name {
			return fmt.Errorf("VM %d (%s) was destroyed", vmid, name)
		}
		return nil
	}
}

func someoneElses(f *fakeAPI, vmid int) {
	f.addVM(vmid, &fakeVM{Node: "pve1", Status: "running", Config: map[string]any{"name": "someone-else", "cores": float64(1), "memory": "512"}})
}

// C1: a refused clone with an explicit VMID never adopts the VM holding it.
func TestCreateCloneRefusedNeverAdoptsExistingVMID(t *testing.T) {
	f := newFakeAPI(t).withVDC().withTemplate()
	someoneElses(f, 9200)
	f.hook(http.MethodPost, "/api/v1/connections/conn1/guests/qemu/pve1/9000/clone", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
		reply(w, 409, map[string]any{"error": "Quota exceeded", "violations": []string{"VMs: 7/7"}})
		return true
	})
	hcl := strings.Replace(cloneVM, `  name          = "app-01"`, "  name          = \"app-01\"\n  vmid          = 9200", 1)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: f.config(hcl), ExpectError: regexp.MustCompile(`Quota exceeded`)},
			f.afterError(f.stillThere(9200, "someone-else")),
		},
	})
}

func failedDeployment(f *fakeAPI, step string, createVM bool) {
	f.hook(http.MethodPost, "/api/v1/templates/deploy", func(w http.ResponseWriter, _ *http.Request, body map[string]any) bool {
		if createVM {
			return false
		}
		vmid := int(body["vmid"].(float64))
		f.mu.Lock()
		f.deployments[fmt.Sprintf("dep-%d", vmid)] = map[string]any{"id": fmt.Sprintf("dep-%d", vmid)}
		f.mu.Unlock()
		reply(w, 200, map[string]any{"data": map[string]any{"deploymentId": fmt.Sprintf("dep-%d", vmid), "status": "pending", "vmid": vmid}})
		return true
	})
	f.hook(http.MethodGet, "/api/v1/templates/deployments/dep-9105", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
		reply(w, 200, map[string]any{"data": map[string]any{"id": "dep-9105", "status": "failed", "currentStep": step,
			"error": "Subnet 10.15.15.0/24 is full", "connectionId": "conn1", "node": "pve2", "vmid": 9105}})
		return true
	})
}

// C1: a deployment that failed before creating its VM adopts nothing, even
// when its VMID got taken meanwhile.
func TestCreateDeploymentFailedBeforeCreatingNeverAdopts(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	failedDeployment(f, "downloading", false)
	f.hook(http.MethodGet, "/api/v1/connections/conn1/cluster/nextid", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
		someoneElses(f, 9105) // taken by another deployment during the download
		reply(w, 200, map[string]any{"data": 9105})
		return true
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: stoppedCatalogue(f), ExpectError: regexp.MustCompile(`is\s+full`)},
			f.afterError(f.stillThere(9105, "someone-else")),
		},
	})
}

// C1: after creating, a VM is adopted only when it bears the requested name.
func TestCreateDeploymentFailedNameMismatchNotAdopted(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	failedDeployment(f, "creating", false)
	f.hook(http.MethodGet, "/api/v1/connections/conn1/cluster/nextid", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
		someoneElses(f, 9105)
		reply(w, 200, map[string]any{"data": 9105})
		return true
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: stoppedCatalogue(f), ExpectError: regexp.MustCompile(`is\s+full`)},
			f.afterError(f.stillThere(9105, "someone-else")),
		},
	})
}

// C1: a create that times out keeps the VM it started, so destroy removes it.
func TestCreateCloneTimeoutKeepsVM(t *testing.T) {
	f := newFakeAPI(t).withVDC().withTemplate()
	f.hook(http.MethodGet, "/api/v1/tasks/conn1/pve1/UPID:pve1:9000", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
		reply(w, 200, map[string]any{"status": "running"})
		return true
	})
	hcl := strings.Replace(cloneVM, `  power_state = "stopped"`, "  power_state = \"stopped\"\n  timeouts {\n    create = \"2s\"\n  }", 1)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy:             f.noVMsBut(9000),
		Steps: []resource.TestStep{
			{Config: f.config(hcl), ExpectError: regexp.MustCompile(`(?i)deadline|context`)},
			{
				// The VM was kept (tainted): the next apply replaces it.
				PreConfig: func() { f.hook(http.MethodGet, "/api/v1/tasks/conn1/pve1/UPID:pve1:9000", nil) },
				Config:    f.config(hcl),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("proxcenter_vm.app", plancheck.ResourceActionDestroyBeforeCreate)}},
			},
		},
	})
}

func TestCreateDeploymentTimeoutKeepsCreatedVM(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	f.hook(http.MethodGet, "/api/v1/templates/deployments/dep-9105", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
		reply(w, 200, map[string]any{"data": map[string]any{"id": "dep-9105", "status": "configuring", "connectionId": "conn1", "node": "pve2", "vmid": 9105}})
		return true
	})
	hcl := strings.Replace(fmt.Sprintf(catalogueVM, "stopped"), `  power_state = "stopped"`, "  power_state = \"stopped\"\n  timeouts {\n    create = \"1s\"\n  }", 1)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy:             f.noVMsBut(),
		Steps: []resource.TestStep{
			{Config: f.config(hcl), ExpectError: regexp.MustCompile(`dep-9105`)},
			{
				PreConfig: func() { f.hook(http.MethodGet, "/api/v1/templates/deployments/dep-9105", nil) },
				Config:    f.config(hcl),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("proxcenter_vm.web", plancheck.ResourceActionDestroyBeforeCreate)}},
			},
		},
	})
}

// C1: a clash text inside another failure after creation is not a clash.
func TestCreateFailureMentioningInUseAfterCreateKeepsVM(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	f.hook(http.MethodGet, "/api/v1/templates/deployments/dep-9105", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
		reply(w, 200, map[string]any{"data": map[string]any{"id": "dep-9105", "status": "failed", "currentStep": "configuring",
			"error": "IPAM: address 10.15.15.20 already in use", "connectionId": "conn1", "node": "pve2", "vmid": 9105}})
		return true
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: stoppedCatalogue(f), ExpectError: regexp.MustCompile(`already\s+in\s+use`)},
			f.afterError(resource.ComposeTestCheckFunc(f.noVMsBut(), check(func() error {
				if f.nextIDCalls != 1 {
					return fmt.Errorf("nextid called %d times", f.nextIDCalls)
				}
				return nil
			}))),
		},
	})
}

// C2: a key read from a file keeps its trailing newline in the config.
func TestCreateSSHKeyWithTrailingNewline(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	hcl := variant("stopped", `ssh_keys = ["ssh-ed25519 AAAA+x ops@host"]`, `ssh_keys = ["ssh-ed25519 AAAA+x ops@host\n", "ssh-rsa B1 a\nssh-rsa B2 b\n"]`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy:             f.noVMsBut(),
		Steps:                    []resource.TestStep{{Config: f.config(hcl)}, {Config: f.config(hcl), PlanOnly: true}},
	})
}

// C2: a MAC written in lower case is the same MAC.
func TestUpdateLowercaseMAC(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	second := variant("stopped", "    vlan   = 120", "    vlan   = 120\n    mac_address = \"bc:24:11:00:23:91\"")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy:             f.noVMsBut(),
		Steps:                    []resource.TestStep{{Config: f.config(variant("stopped"))}, {Config: f.config(second)}, {Config: f.config(second), PlanOnly: true}},
	})
}

// I1: an address reported by the guest agent during an update.
func TestUpdateIPAddressAppearsDuringApply(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	f.hook(http.MethodGet, "/api/v1/vms", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
		if f.count("PUT "+vmPath+"/config") < 2 {
			return false
		}
		reply(w, 200, map[string]any{"data": map[string]any{"vms": []map[string]any{{"connId": "conn1", "node": "pve2", "vmid": "9105", "name": "web-01", "type": "qemu", "status": "stopped", "ips": []string{"10.0.0.7"}}}}})
		return true
	})
	updateCase(t, f, variant("stopped"), variant("stopped", "cores     = 4", "cores     = 8"), nil)
}

// I1: an IPAM VNet rewrites the address when the first NIC changes bridge.
func TestUpdateBridgeChangeMarksAddressUnknown(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	f.hook(http.MethodPut, vmPath+"/config", func(w http.ResponseWriter, _ *http.Request, body map[string]any) bool {
		if net0, ok := body["net0"].(string); ok && strings.Contains(net0, "v6bc065f") {
			f.mu.Lock()
			f.vms[9105].Config["net0"], f.vms[9105].Config["ipconfig0"] = net0, "ip=10.15.15.40/24,gw=10.15.15.1"
			f.mu.Unlock()
			reply(w, 200, map[string]any{"data": nil})
			return true
		}
		return false
	})
	updateCase(t, f, variant("stopped"), variant("stopped", `bridge = "vnet-prod"`, `bridge = "v6bc065f"`),
		resource.TestCheckResourceAttr("proxcenter_vm.web", "cloud_init.ip_address", "10.15.15.40"))
}

// I1: "dhcp" on an IPAM VNet comes back as the allocated address.
func TestCreateDHCPOnIPAMVNet(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	f.hook(http.MethodGet, "/api/v1/templates/deployments/dep-9105", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
		f.mu.Lock()
		f.vms[9105].Config["ipconfig0"] = "ip=10.15.15.50/24,gw=10.15.15.1"
		f.mu.Unlock()
		return false
	})
	hcl := variant("stopped", `    user     = "ops"`, "    user     = \"ops\"\n    ip       = \"dhcp\"")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy:             f.noVMsBut(),
		Steps: []resource.TestStep{
			{Config: f.config(hcl), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("proxcenter_vm.web", "cloud_init.ip", "dhcp"),
				resource.TestCheckResourceAttr("proxcenter_vm.web", "cloud_init.ip_address", "10.15.15.50"))},
			{Config: f.config(hcl), PlanOnly: true},
		},
	})
}

const multiNICImport = `
resource "proxcenter_vm" "win" {
  connection_id = "conn1"
  name          = "win-01"
  cores         = 4
  memory_mb     = 8192

  disk {
    slot    = "sata0"
    storage = "ceph-gold"
    size_gb = 80
  }

  network {
    slot   = "net0"
    bridge = "vmbr0"
  }
%s}`

func seedWindowsVM(f *fakeAPI) {
	f.addVM(9300, &fakeVM{Node: "pve1", Status: "stopped", Config: map[string]any{
		"name": "win-01", "cores": float64(4), "sockets": float64(2), "memory": "8192,max=16384",
		"sata0": "ceph-gold:vm-9300-disk-0,size=80G", "net0": "e1000=BC:24:11:93:00:00,bridge=vmbr0", "net1": "e1000=BC:24:11:93:00:01,bridge=vmbr1",
	}})
}

// I2 + I3: importing a two-socket, two-NIC e1000 VM with a ballooning memory
// property, described fully, plans nothing.
func TestImportMultiNICKeepsUndescribedDefaults(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	seedWindowsVM(f)
	both := fmt.Sprintf(multiNICImport, "\n  network {\n    slot   = \"net1\"\n    bridge = \"vmbr1\"\n  }\n")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: f.config(both), ResourceName: "proxcenter_vm.win", ImportState: true, ImportStateId: "conn1/9300", ImportStatePersist: true},
			{Config: f.config(both), PlanOnly: true},
		},
	})
}

// I2: a NIC left out of the config is refused, never deleted.
func TestPlanUndescribedNICIsError(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	seedWindowsVM(f)
	one := fmt.Sprintf(multiNICImport, "")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: f.config(one), ResourceName: "proxcenter_vm.win", ImportState: true, ImportStateId: "conn1/9300", ImportStatePersist: true},
			{Config: f.config(one), ExpectError: regexp.MustCompile(`(?s)net1.*no\s+longer\s+described`)},
		},
	})
}

// I2: several NICs need explicit slots: list positions are not identities.
func TestPlanSeveralNICsNeedSlots(t *testing.T) {
	hcl := variant("stopped", "  cloud_init {", "  network {\n    bridge = \"vnet2\"\n  }\n\n  cloud_init {")
	planOnly(t, newFakeAPI(t).withVDC(), hcl, regexp.MustCompile(`(?s)slot.*several\s+network`))
}
