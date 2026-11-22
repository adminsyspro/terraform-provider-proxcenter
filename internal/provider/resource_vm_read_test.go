package provider

import (
	"fmt"
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// stoppedCatalogue is catalogueVM stopped, created as VMID 9105 on pve2.
func stoppedCatalogue(f *fakeAPI) string { return f.config(fmt.Sprintf(catalogueVM, "stopped")) }

func TestReadDriftOnDescribedFieldOnly(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: stoppedCatalogue(f)},
			{
				PreConfig: func() {
					vm := f.vm(9105)
					f.mu.Lock()
					vm.Config["tags"], vm.Config["description"], vm.Config["nameserver"] = "web", "set in the UI", "9.9.9.9"
					f.mu.Unlock()
				},
				Config:   stoppedCatalogue(f),
				PlanOnly: true,
			},
			{
				PreConfig: func() {
					f.mu.Lock()
					f.vms[9105].Config["cores"] = float64(8)
					f.mu.Unlock()
				},
				Config:             stoppedCatalogue(f),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestReadUndescribedDiskIgnored(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: stoppedCatalogue(f)},
			{
				PreConfig: func() {
					f.mu.Lock()
					f.vms[9105].Config["scsi5"] = "ceph-gold:vm-9105-disk-5,size=100G"
					f.vms[9105].Config["net3"] = "virtio=BC:24:11:00:00:03,bridge=vmbr1"
					f.mu.Unlock()
				},
				Config:   stoppedCatalogue(f),
				PlanOnly: true,
			},
		},
	})
}

func TestReadMovedByDRSNoDiff(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: stoppedCatalogue(f)},
			{
				PreConfig: func() {
					f.mu.Lock()
					f.vms[9105].Node = "pve1"
					f.mu.Unlock()
				},
				Config:   stoppedCatalogue(f),
				PlanOnly: true,
			},
			{
				Config: stoppedCatalogue(f),
				Check:  resource.TestCheckResourceAttr("proxcenter_vm.web", "node", "pve1"),
			},
		},
	})
}

func TestReadNotFoundRemoves(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: stoppedCatalogue(f)},
			{
				PreConfig: func() {
					f.mu.Lock()
					delete(f.vms, 9105)
					f.mu.Unlock()
				},
				Config:             stoppedCatalogue(f),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestReadMissingFromListKeepsPowerState(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: f.config(fmt.Sprintf(catalogueVM, "running"))},
			{
				// A cluster unreachable from ProxCenter: the list comes back partial.
				PreConfig: func() {
					f.hook(http.MethodGet, "/api/v1/vms", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
						reply(w, 200, map[string]any{"data": map[string]any{"vms": []any{}}})
						return true
					})
				},
				Config:   f.config(fmt.Sprintf(catalogueVM, "running")),
				PlanOnly: true,
			},
		},
	})
}

const importedVM = `
resource "proxcenter_vm" "legacy" {
  connection_id = "conn1"
  name          = "legacy-01"
  cores         = 2
  memory_mb     = 2048
  power_state   = "running"

  disk {
    slot    = "virtio0"
    storage = "ceph-gold"
    size_gb = 40
  }

  network {
    bridge = "vmbr0"
    vlan   = 42
  }

  cloud_init {
    user = "admin"
  }
}`

func seedUIVM(f *fakeAPI) {
	f.addVM(9200, &fakeVM{Node: "pve2", Status: "running", Config: map[string]any{
		"name": "legacy-01", "cores": float64(2), "memory": "2048", "cpu": "host", "tags": "legacy",
		"virtio0": "ceph-gold:vm-9200-disk-0,size=40G", "net0": "virtio=BC:24:11:92:00:00,bridge=vmbr0,tag=42",
		"ide2": "ceph-gold:vm-9200-cloudinit,media=cdrom", "ciuser": "admin", "sshkeys": "ssh-ed25519%20AAAA%20admin", "ipconfig0": "ip=10.1.1.5/24,gw=10.1.1.1",
	}})
}

func TestImportThenEmptyPlan(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	seedUIVM(f)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{
				Config:             f.config(importedVM),
				ResourceName:       "proxcenter_vm.legacy",
				ImportState:        true,
				ImportStateId:      "conn1/9200",
				ImportStatePersist: true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					a := states[0].Attributes
					for k, want := range map[string]string{"node": "pve2", "vmid": "9200", "disk.0.slot": "virtio0", "network.0.vlan": "42",
						"cloud_init.ip": "10.1.1.5/24", "cloud_init.ip_address": "10.1.1.5", "cloud_init.ssh_keys.0": "ssh-ed25519 AAAA admin", "power_state": "running"} {
						if a[k] != want {
							return fmt.Errorf("%s = %q, want %q", k, a[k], want)
						}
					}
					if _, has := a["source.image"]; has && a["source.image"] != "" {
						return fmt.Errorf("an imported VM has no source")
					}
					return nil
				},
			},
			{Config: f.config(importedVM), PlanOnly: true},
		},
	})
}

func TestImportBadID(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{{
			Config: f.config(importedVM), ResourceName: "proxcenter_vm.legacy", ImportState: true, ImportStateId: "9200",
			ExpectError: regexp.MustCompile(`<connection_id>/<vmid>`),
		}},
	})
}

func TestImportUnknownVM(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{{
			Config: f.config(importedVM), ResourceName: "proxcenter_vm.legacy", ImportState: true, ImportStateId: "conn1/4242",
			ExpectError: regexp.MustCompile(`VM\s+4242\s+is\s+not\s+found`),
		}},
	})
}

func TestDeleteShutdownThenDestroy(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy: resource.ComposeTestCheckFunc(f.noVMsBut(), check(func() error {
			if f.count("POST /api/v1/connections/conn1/guests/qemu/pve2/9105/shutdown") != 1 || f.count("POST /api/v1/connections/conn1/guests/qemu/pve2/9105/stop") != 0 {
				return fmt.Errorf("want one ACPI shutdown and no hard stop")
			}
			return nil
		})),
		Steps: []resource.TestStep{{Config: f.config(fmt.Sprintf(catalogueVM, "running"))}},
	})
}

func failShutdown(f *fakeAPI) {
	f.hook(http.MethodPost, "/api/v1/connections/conn1/guests/qemu/pve2/9105/shutdown", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
		errorReply(w, 500, "VM quit/powerdown failed - got timeout")
		return true
	})
}

func TestDeleteRunningWithoutForceStopErrors(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	running := f.config(fmt.Sprintf(catalogueVM, "running"))
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: running},
			{PreConfig: func() { failShutdown(f) }, Config: running, Destroy: true, ExpectError: regexp.MustCompile(`(?s)got\s+timeout.*force_stop`)},
			{PreConfig: func() { f.hook(http.MethodPost, "/api/v1/connections/conn1/guests/qemu/pve2/9105/shutdown", nil) }, Config: running},
		},
	})
}

func TestDeleteForceStop(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	failShutdown(f)
	hcl := regexp.MustCompile(`power_state = "running"`).ReplaceAllString(fmt.Sprintf(catalogueVM, "running"), "power_state = \"running\"\n  force_stop  = true")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy: resource.ComposeTestCheckFunc(f.noVMsBut(), check(func() error {
			if f.count("POST /api/v1/connections/conn1/guests/qemu/pve2/9105/stop") != 1 {
				return fmt.Errorf("no hard stop after the failed shutdown")
			}
			return nil
		})),
		Steps: []resource.TestStep{{Config: f.config(hcl)}},
	})
}

func TestDeleteAlreadyGone(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	f.hook(http.MethodDelete, "/api/v1/connections/conn1/guests/qemu/pve2/9105", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
		f.mu.Lock()
		delete(f.vms, 9105)
		f.mu.Unlock()
		errorReply(w, 404, "VM 9105 not found")
		return true
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy:             f.noVMsBut(),
		Steps:                    []resource.TestStep{{Config: stoppedCatalogue(f)}},
	})
}
