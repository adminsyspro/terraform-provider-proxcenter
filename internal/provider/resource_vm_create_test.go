package provider

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const catalogueVM = `
resource "proxcenter_vm" "web" {
  connection_id = "conn1"
  name          = "web-01"
  target_node   = "pve2"

  source { image = "ubuntu-24.04" }

  cores     = 4
  memory_mb = 8192

  disk {
    slot    = "scsi0"
    storage = "ceph-gold"
    size_gb = 64
  }
  disk {
    slot    = "scsi1"
    storage = "ceph-silver"
    size_gb = 20
  }

  network {
    bridge = "vnet-prod"
    vlan   = 120
  }

  cloud_init {
    user     = "ops"
    ssh_keys = ["ssh-ed25519 AAAA+x ops@host"]
  }

  power_state = "%s"
}`

func check(fn func() error) resource.TestCheckFunc {
	return func(*terraform.State) error { return fn() }
}

func TestCreateFromImage(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy:             f.noVMsBut(),
		Steps: []resource.TestStep{{
			Config: f.config(fmt.Sprintf(catalogueVM, "running")),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("proxcenter_vm.web", "id", "conn1/9105"),
				resource.TestCheckResourceAttr("proxcenter_vm.web", "vmid", "9105"),
				resource.TestCheckResourceAttr("proxcenter_vm.web", "node", "pve2"),
				resource.TestCheckResourceAttr("proxcenter_vm.web", "network.0.slot", "net0"),
				resource.TestCheckResourceAttr("proxcenter_vm.web", "network.0.mac_address", "BC:24:11:00:23:91"),
				resource.TestCheckResourceAttr("proxcenter_vm.web", "cloud_init.ip", "dhcp"),
				resource.TestCheckResourceAttr("proxcenter_vm.web", "power_state", "running"),
				check(func() error {
					body := f.lastBody("POST /api/v1/templates/deploy")
					hw := body["hardware"].(map[string]any)
					switch {
					case body["start"] != false:
						return fmt.Errorf("deploy start = %v, want false", body["start"])
					case hw["diskSize"] != "64G" || hw["diskBus"] != "scsi" || hw["vlanTag"] != float64(120) || hw["networkBridge"] != "vnet-prod":
						return fmt.Errorf("hardware = %v", hw)
					case body["storage"] != "ceph-gold" || body["node"] != "pve2" || body["imageSlug"] != "ubuntu-24.04":
						return fmt.Errorf("deploy = %v", body)
					}
					ci := body["cloudInit"].(map[string]any)
					if _, has := ci["cipassword"]; has || ci["sshKeys"] != "ssh-ed25519 AAAA+x ops@host" || ci["ciuser"] != "ops" {
						return fmt.Errorf("cloudInit = %v", ci)
					}
					if _, has := ci["ipconfig0"]; has {
						return fmt.Errorf("an unset ip must leave ipconfig0 to ProxCenter: %v", ci)
					}
					put := f.lastBody("PUT /api/v1/connections/conn1/guests/qemu/pve2/9105/config")
					if put["scsi1"] != "ceph-silver:20" || len(put) != 1 {
						return fmt.Errorf("config PUT = %v, want only the extra disk", put)
					}
					if f.count("POST /api/v1/connections/conn1/guests/qemu/pve2/9105/start") != 1 {
						return fmt.Errorf("VM not started")
					}
					return nil
				}),
			),
		}},
	})
}

func TestCreateStoppedNeverCallsPower(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	forbid := func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
		errorReply(w, 403, "Token scope vms:power required")
		return true
	}
	for _, a := range []string{"start", "stop", "shutdown", "reboot"} {
		f.hook(http.MethodPost, "/api/v1/connections/conn1/guests/qemu/pve2/9105/"+a, forbid)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy:             f.noVMsBut(),
		Steps: []resource.TestStep{{
			Config: f.config(fmt.Sprintf(catalogueVM, "stopped")),
			Check:  resource.TestCheckResourceAttr("proxcenter_vm.web", "power_state", "stopped"),
		}},
	})
}

func TestCreateFromBlueprintSendsItsImage(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	hcl := strings.Replace(fmt.Sprintf(catalogueVM, "stopped"), `source { image = "ubuntu-24.04" }`, `source { blueprint = "bp1" }`, 1)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy:             f.noVMsBut(),
		Steps: []resource.TestStep{{
			Config: f.config(hcl),
			Check: check(func() error {
				body := f.lastBody("POST /api/v1/templates/deploy")
				if body["blueprintId"] != "bp1" || body["imageSlug"] != "debian-12" {
					return fmt.Errorf("deploy = %v", body)
				}
				return nil
			}),
		}},
	})
}

const cloneVM = `
resource "proxcenter_vm" "app" {
  connection_id = "conn1"
  name          = "app-01"

  source { clone_of = 9000 }

  cores     = 2
  memory_mb = 4096

  disk {
    slot    = "scsi0"
    storage = "ceph-gold"
    size_gb = 32
  }

  network {
    bridge = "vnet-prod"
  }

  cloud_init {
    user = "ops"
  }

  power_state = "stopped"
}`

func TestCreateFromCloneWaitsProvisioning(t *testing.T) {
	f := newFakeAPI(t).withVDC().withTemplate()
	f.cloneProvisioning = []string{"running", "running"}
	putBeforeReady := false
	f.hook(http.MethodPut, "/api/v1/connections/conn1/guests/qemu/pve1/9105/config", func(http.ResponseWriter, *http.Request, map[string]any) bool {
		if vm := f.vm(9105); vm != nil && len(vm.Provisioning) > 0 {
			putBeforeReady = true
		}
		return false
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy:             f.noVMsBut(9000),
		Steps: []resource.TestStep{{
			Config: f.config(cloneVM),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("proxcenter_vm.app", "node", "pve1"),
				resource.TestCheckResourceAttr("proxcenter_vm.app", "target_node", "pve1"),
				resource.TestCheckResourceAttr("proxcenter_vm.app", "disk.0.size_gb", "32"),
				resource.TestCheckResourceAttr("proxcenter_vm.app", "cpu", "host"),
				check(func() error {
					if putBeforeReady {
						return fmt.Errorf("config written while the clone was still provisioning")
					}
					clone := f.lastBody("POST /api/v1/connections/conn1/guests/qemu/pve1/9000/clone")
					if clone["newid"] != float64(9105) || clone["full"] != true || clone["name"] != "app-01" {
						return fmt.Errorf("clone = %v", clone)
					}
					put := f.lastBody("PUT /api/v1/connections/conn1/guests/qemu/pve1/9105/config")
					if put["cores"] != float64(2) || put["memory"] != float64(4096) || put["ciuser"] != "ops" || !strings.HasPrefix(fmt.Sprint(put["net0"]), "virtio=BC:24:11:00:90:00,bridge=vnet-prod") {
						return fmt.Errorf("config PUT = %v", put)
					}
					if f.count("POST /api/v1/connections/conn1/guests/qemu/pve1/9105/disk/resize") != 1 {
						return fmt.Errorf("template disk not grown to 32 GB")
					}
					return nil
				}),
			),
		}},
	})
}

func TestCreateCloneProvisioningFailed(t *testing.T) {
	f := newFakeAPI(t).withVDC().withTemplate()
	f.cloneProvisioning = []string{"failed"}
	f.cloneFailures = []string{"ipam: no free address in 10.15.15.0/24"}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: f.config(cloneVM),
				ExpectError: regexp.MustCompile(`no free address in\s+10\.15\.15\.0/24`)},
			f.afterError(resource.TestCheckFunc(f.noVMsBut(9000))),
		},
	})
}

func TestCreateDeploymentFailedKeepsVMInState(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	f.hook(http.MethodGet, "/api/v1/templates/deployments/dep-9105", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
		reply(w, 200, map[string]any{"data": map[string]any{"id": "dep-9105", "status": "failed", "currentStep": "configuring",
			"error": "qm set failed: storage ceph-gold is full", "connectionId": "conn1", "node": "pve2", "vmid": 9105}})
		return true
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: f.config(fmt.Sprintf(catalogueVM, "stopped")),
				ExpectError: regexp.MustCompile(`storage\s+ceph-gold\s+is\s+full`)},
			f.afterError(resource.ComposeTestCheckFunc(f.noVMsBut(), check(func() error {
				if f.nextIDCalls != 1 {
					return fmt.Errorf("nextid called %d times, a failed deployment must not allocate another VMID", f.nextIDCalls)
				}
				return nil
			}))),
		},
	})
}

func TestCreateRetriesVmidClash(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	f.addVM(9105, &fakeVM{Node: "pve1", Status: "running", Config: map[string]any{"name": "someone-else"}})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy:             f.noVMsBut(9105),
		Steps: []resource.TestStep{{
			Config: f.config(fmt.Sprintf(catalogueVM, "stopped")),
			Check:  resource.TestCheckResourceAttr("proxcenter_vm.web", "vmid", "9106"),
		}},
	})
}

func TestCreateRetriesCloneVmidClash(t *testing.T) {
	f := newFakeAPI(t).withVDC().withTemplate()
	f.addVM(9105, &fakeVM{Node: "pve1", Status: "running", Config: map[string]any{"name": "someone-else"}})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy:             f.noVMsBut(9000, 9105),
		Steps: []resource.TestStep{{
			Config: f.config(cloneVM),
			Check:  resource.TestCheckResourceAttr("proxcenter_vm.app", "vmid", "9106"),
		}},
	})
}

func TestCreateStopsAfterFiveClashes(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	keep := []int{}
	for id := 9105; id <= 9109; id++ {
		f.addVM(id, &fakeVM{Node: "pve1", Status: "running", Config: map[string]any{"name": "taken"}})
		keep = append(keep, id)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: f.config(fmt.Sprintf(catalogueVM, "stopped")),
				ExpectError: regexp.MustCompile(`(?s)VMID\s+9109\s+is\s+already\s+in\s+use.*5\s+attempts`)},
			f.afterError(resource.TestCheckFunc(f.noVMsBut(keep...))),
		},
	})
}

func TestCreateNextIDUnavailable(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	f.nextIDs = nil
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: f.config(fmt.Sprintf(catalogueVM, "stopped")),
				ExpectError: regexp.MustCompile(`no free VMID`)},
			f.afterError(check(func() error {
				if f.nextIDCalls != 1 {
					return fmt.Errorf("nextid called %d times, a 503 is not a VMID clash", f.nextIDCalls)
				}
				return nil
			})),
		},
	})
}

func TestCreateQuotaRefusalShownVerbatim(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	f.hook(http.MethodPost, "/api/v1/templates/deploy", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
		reply(w, 409, map[string]any{"error": "Quota exceeded", "violations": []string{"RAM: 60/64 GB, +8 GB exceeds quota", "VMs: 7/7"}})
		return true
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: f.config(fmt.Sprintf(catalogueVM, "stopped")),
				ExpectError: regexp.MustCompile(`(?s)Quota exceeded.*RAM:\s+60/64\s+GB,\s+\+8\s+GB\s+exceeds\s+quota.*VMs:\s+7/7`)},
			f.afterError(resource.TestCheckFunc(f.noVMsBut())),
		},
	})
}
