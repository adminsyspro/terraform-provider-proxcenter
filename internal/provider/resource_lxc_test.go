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

const ctPath = "/api/v1/connections/conn1/guests/lxc/pve1/9105"

// ctConfig is a container from the alpine template on pve1.
func ctConfig(power, extra string) string {
	return fmt.Sprintf(`
resource "proxcenter_lxc" "ct" {
  connection_id = "conn1"
  target_node   = "pve1"
  hostname      = "ct-web"

  source { ostemplate = "local:vztmpl/alpine-3.24-default_20261105_amd64.tar.xz" }

  cores     = 2
  memory_mb = 1024

  features = { nesting = true }

  rootfs {
    storage = "ceph-gold"
    size_gb = 8
  }

  mount_point {
    slot    = "mp0"
    storage = "ceph-gold"
    size_gb = 10
    path    = "/data"
  }

  network {
    bridge = "vmbr0"
    ip     = "dhcp"
    vlan   = 150
  }

  ssh_keys = ["ssh-ed25519 AAAA ops@host\n"]
%s
  power_state = %q
}`, extra, power)
}

func TestLXCCreateFromTemplate(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	cfg := ctConfig("running", "  password_wo = \"s3cret\"")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy:             f.noVMsBut(),
		Steps: []resource.TestStep{
			{
				Config: f.config(cfg),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("proxcenter_lxc.ct", "id", "conn1/9105"),
					resource.TestCheckResourceAttr("proxcenter_lxc.ct", "node", "pve1"),
					resource.TestCheckResourceAttr("proxcenter_lxc.ct", "network.0.slot", "net0"),
					resource.TestCheckResourceAttr("proxcenter_lxc.ct", "network.0.name", "eth0"),
					resource.TestCheckResourceAttrSet("proxcenter_lxc.ct", "network.0.mac_address"),
					resource.TestCheckResourceAttr("proxcenter_lxc.ct", "swap_mb", "512"),
					resource.TestCheckResourceAttr("proxcenter_lxc.ct", "unprivileged", "true"),
					resource.TestCheckNoResourceAttr("proxcenter_lxc.ct", "password_wo"),
					check(func() error {
						b := f.lastBody("POST /api/v1/connections/conn1/guests/lxc/pve1")
						want := map[string]any{"hostname": "ct-web", "ostemplate": "local:vztmpl/alpine-3.24-default_20261105_amd64.tar.xz",
							"rootfs": "ceph-gold:8", "mp0": "ceph-gold:10,mp=/data", "net0": "name=eth0,bridge=vmbr0,ip=dhcp,tag=150",
							"cores": float64(2), "memory": float64(1024), "swap": float64(512), "unprivileged": float64(1), "features": "nesting=1",
							"ssh-public-keys": "ssh-ed25519 AAAA ops@host", "password": "s3cret", "start": float64(0)}
						for k, v := range want {
							if b[k] != v {
								return fmt.Errorf("create %s = %v, want %v (body %v)", k, b[k], v, b)
							}
						}
						if f.count("POST "+ctPath+"/start") != 1 {
							return fmt.Errorf("not started")
						}
						return nil
					}),
				),
			},
			{Config: f.config(cfg), PlanOnly: true},
		},
	})
}

func TestLXCCreateStoppedNeverCallsPower(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	for _, a := range []string{"start", "stop", "shutdown"} {
		f.hook(http.MethodPost, ctPath+"/"+a, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
			errorReply(w, 403, "Token scope vms:power required")
			return true
		})
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy:             f.noVMsBut(),
		Steps:                    []resource.TestStep{{Config: f.config(ctConfig("stopped", ""))}},
	})
}

func TestLXCClone(t *testing.T) {
	f := newFakeAPI(t).withVDC().withCTTemplate()
	cfg := `
resource "proxcenter_lxc" "ct" {
  connection_id = "conn1"
  hostname      = "ct-clone"

  source { clone_of = 9001 }

  cores     = 2
  memory_mb = 512

  rootfs {
    storage = "ceph-gold"
    size_gb = 4
  }

  network {
    bridge = "vmbr1"
  }

  power_state = "stopped"
}`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy:             f.noVMsBut(9001),
		Steps: []resource.TestStep{
			{
				Config: f.config(cfg),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("proxcenter_lxc.ct", "node", "pve1"),
					resource.TestCheckResourceAttr("proxcenter_lxc.ct", "rootfs.size_gb", "4"),
					check(func() error {
						clone := f.lastBody("POST /api/v1/connections/conn1/guests/lxc/pve1/9001/clone")
						if clone["hostname"] != "ct-clone" || clone["newid"] != float64(9105) {
							return fmt.Errorf("clone = %v", clone)
						}
						put := f.lastBody("PUT " + ctPath + "/config")
						if put["cores"] != float64(2) || put["memory"] != float64(512) || !strings.Contains(fmt.Sprint(put["net0"]), "bridge=vmbr1") {
							return fmt.Errorf("config PUT = %v", put)
						}
						if r := f.lastBody("POST " + ctPath + "/disk/resize"); r["disk"] != "rootfs" || r["size"] != "4G" {
							return fmt.Errorf("resize = %v", r)
						}
						return nil
					}),
				),
			},
			{Config: f.config(cfg), PlanOnly: true},
		},
	})
}

// Review Focus 1: the API forces a tenant's container unprivileged.
func TestLXCTenantForcedUnprivilegedNoDiff(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	f.forceUnprivileged = true
	cfg := ctConfig("stopped", "  unprivileged = false")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy:             f.noVMsBut(),
		Steps: []resource.TestStep{
			{Config: f.config(cfg)},
			{Config: f.config(cfg), PlanOnly: true},
			// An apply with nothing to change refreshes: the state records true.
			{Config: f.config(cfg), Check: resource.TestCheckResourceAttr("proxcenter_lxc.ct", "unprivileged", "true")},
		},
	})
}

// Review Focus 2: a refused template leaves nothing in state.
func TestLXCTemplateRefusedVerbatim(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	cfg := strings.Replace(ctConfig("stopped", ""), "alpine-3.24-default_20261105_amd64.tar.xz", "missing.tar.xz", 1)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: f.config(cfg), ExpectError: regexp.MustCompile(`volume\s+'local:vztmpl/missing.tar.xz'\s+does\s+not\s+exist`)},
			f.afterError(resource.TestCheckFunc(f.noVMsBut())),
		},
	})
}

const importedCT = `
resource "proxcenter_lxc" "legacy" {
  connection_id = "conn1"
  hostname      = "legacy-ct"
  cores         = 1
  memory_mb     = 512

  rootfs {
    storage = "ceph-gold"
    size_gb = 4
  }

  mount_point {
    slot    = "mp0"
    storage = "ceph-gold"
    size_gb = 10
    path    = "/data"
  }
  mount_point {
    slot    = "mp1"
    storage = "ceph-silver"
    size_gb = 2
    path    = "/logs"
  }

  network {
    slot   = "net0"
    bridge = "vmbr0"
    vlan   = 150
  }
  network {
    slot   = "net1"
    bridge = "vnet-prod"
  }
}`

// Review Focus 4.
func TestLXCImportTwoMountPointsTwoNICs(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	f.addVM(9300, &fakeVM{Type: "lxc", Node: "pve2", Status: "running", Config: map[string]any{
		"hostname": "legacy-ct", "cores": float64(1), "memory": float64(512), "swap": float64(256), "unprivileged": float64(1),
		"features": "nesting=1", "nameserver": "1.1.1.1",
		"rootfs": "ceph-gold:vm-9300-disk-0,size=4G", "mp0": "ceph-gold:vm-9300-disk-1,mp=/data,size=10G", "mp1": "ceph-silver:vm-9300-disk-2,mp=/logs,size=2G",
		"net0": "name=eth0,bridge=vmbr0,hwaddr=BC:24:11:93:00:00,ip=dhcp,tag=150,type=veth",
		"net1": "name=eth1,bridge=vnet-prod,hwaddr=BC:24:11:93:00:01,ip=10.15.15.30/24,gw=10.15.15.1,firewall=1,type=veth",
	}})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{
				Config: f.config(importedCT), ResourceName: "proxcenter_lxc.legacy", ImportState: true, ImportStateId: "conn1/9300", ImportStatePersist: true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					a := states[0].Attributes
					for k, want := range map[string]string{"node": "pve2", "mount_point.1.path": "/logs", "network.1.ip": "10.15.15.30/24",
						"network.1.gateway": "10.15.15.1", "features.nesting": "true", "nameserver": "1.1.1.1", "power_state": "running", "swap_mb": "256"} {
						if a[k] != want {
							return fmt.Errorf("%s = %q, want %q", k, a[k], want)
						}
					}
					return nil
				},
			},
			{Config: f.config(importedCT), PlanOnly: true},
		},
	})
}

func TestLXCDeleteShutdownThenDestroy(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy: resource.ComposeTestCheckFunc(f.noVMsBut(), check(func() error {
			if f.count("POST "+ctPath+"/shutdown") != 1 {
				return fmt.Errorf("no shutdown before the delete")
			}
			return nil
		})),
		Steps: []resource.TestStep{{Config: f.config(ctConfig("running", ""))}},
	})
}

func TestLXCReadMovedByDRSNoDiff(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	cfg := f.config(ctConfig("stopped", ""))
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: cfg},
			{PreConfig: func() { f.mu.Lock(); f.vms[9105].Node = "pve3"; f.mu.Unlock() }, Config: cfg, PlanOnly: true},
			{Config: cfg, Check: resource.TestCheckResourceAttr("proxcenter_lxc.ct", "node", "pve3")},
		},
	})
}
