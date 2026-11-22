package provider

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// I1: swap 0 is a value, not "unset".
func TestLXCUpdateSwapToZero(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	second := strings.Replace(ctConfig("stopped", ""), "memory_mb = 1024", "memory_mb = 1024\n  swap_mb   = 0", 1)
	ctUpdateCase(t, f, ctConfig("stopped", ""), second, check(func() error {
		if b := f.lastBody("PUT " + ctPath + "/config"); b["swap"] != float64(0) {
			return fmt.Errorf("PUT = %v", b)
		}
		return nil
	}))
}

// I2: the path of a mount point is set at creation only.
func TestLXCPlanMountPointPathChangeIsError(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: f.config(ctConfig("stopped", ""))},
			{Config: f.config(strings.Replace(ctConfig("stopped", ""), `path    = "/data"`, `path    = "/srv"`, 1)),
				ExpectError: regexp.MustCompile(`(?s)path\s+of\s+mp0.*creation\s+only`)},
		},
	})
}

// I3: a non-boolean feature survives a feature change.
func TestLXCUpdateFeaturesKeepNonBooleanKeys(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	second := strings.Replace(ctConfig("stopped", ""), "features = { nesting = true }", "features = { nesting = true, keyctl = true }", 1)
	ctUpdateCase(t, f, ctConfig("stopped", ""), second, check(func() error {
		got := fmt.Sprint(f.lastBody("PUT " + ctPath + "/config")["features"])
		if !strings.Contains(got, "mount=nfs;cifs") || !strings.Contains(got, "keyctl=1") || !strings.Contains(got, "nesting=1") {
			return fmt.Errorf("features = %q", got)
		}
		return nil
	}), func() {
		f.mu.Lock()
		f.vms[9105].Config["features"] = "mount=nfs;cifs,nesting=1"
		f.mu.Unlock()
	})
}

// I4: what a clone cannot receive is refused at plan, and its privilege and
// swap come from the source.
func TestLXCPlanCloneRefusesCreationOnlyKeys(t *testing.T) {
	for _, line := range []string{`  password_wo = "x"`, `  nameserver = "9.9.9.9"`, `  ssh_keys = ["ssh-ed25519 A"]`, `  unprivileged = true`} {
		hcl := ctHCL(`  source { clone_of = 9001 }`, "", line+"\n")
		planOnly(t, newFakeAPI(t).withVDC().withCTTemplate(), hcl, regexp.MustCompile(`(?s)clone.*source`))
	}
}

func TestLXCCloneKeepsSourcePrivilegeAndSwap(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	f.addVM(9002, &fakeVM{Type: "lxc", Node: "pve1", Status: "stopped", Config: map[string]any{
		"hostname": "tpl-priv", "template": "1", "cores": float64(1), "memory": float64(256), "swap": float64(256), "unprivileged": float64(0),
		"rootfs": "ceph-gold:base-9002-disk-0,size=2G", "net0": "name=eth0,bridge=vmbr0,hwaddr=BC:24:11:90:02:00,ip=dhcp,type=veth",
	}})
	cfg := ctHCL(`  source { clone_of = 9002 }`, "", "")
	cfg = strings.Replace(cfg, "size_gb = 8", "size_gb = 2", 1)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy:             f.noVMsBut(9002),
		Steps: []resource.TestStep{
			{Config: f.config(cfg), Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("proxcenter_lxc.ct", "unprivileged", "false"),
				resource.TestCheckResourceAttr("proxcenter_lxc.ct", "swap_mb", "256"),
				check(func() error {
					if b := f.lastBody("PUT " + ctPath + "/config"); b != nil && b["swap"] != nil {
						return fmt.Errorf("swap overwritten: %v", b)
					}
					return nil
				}))},
			{Config: f.config(cfg), PlanOnly: true},
		},
	})
}

// I5: ProxCenter refuses a privileged container to a vDC tenant.
func TestLXCTenantPrivilegedRefusedVerbatim(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	f.tenantCreates = true
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: f.config(ctConfig("stopped", "  unprivileged = false")), ExpectError: regexp.MustCompile(`Tenants\s+may\s+only\s+create\s+unprivileged\s+containers`)},
			f.afterError(resource.TestCheckFunc(f.noVMsBut())),
		},
	})
}

// M1: an accepted create is ours even when its task cannot be followed.
func TestLXCCreateTaskReadFailureKeepsContainer(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	f.hook(http.MethodGet, "/api/v1/tasks/conn1/pve1/UPID:pve1:vzcreate:9105", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
		errorReply(w, 500, "task read failed")
		return true
	})
	cfg := f.config(ctConfig("stopped", ""))
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: cfg, ExpectError: regexp.MustCompile(`task\s+read\s+failed`)},
			{PreConfig: func() { f.hook(http.MethodGet, "/api/v1/tasks/conn1/pve1/UPID:pve1:vzcreate:9105", nil) }, Config: cfg,
				Check: resource.TestCheckResourceAttr("proxcenter_lxc.ct", "vmid", "9106")},
			f.afterError(resource.TestCheckFunc(f.noVMsBut())),
		},
	})
}

// M3: a mount point created by Terraform is backed up.
func TestLXCMountPointsAreBackedUp(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy:             f.noVMsBut(),
		Steps: []resource.TestStep{{Config: f.config(ctConfig("stopped", "")), Check: check(func() error {
			if mp := f.lastBody("POST /api/v1/connections/conn1/guests/lxc/pve1")["mp0"]; mp != "ceph-gold:10,mp=/data,backup=1" {
				return fmt.Errorf("mp0 = %v", mp)
			}
			return nil
		})}},
	})
}
