package provider

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

func ctVariant(power string, repl ...string) string {
	return strings.NewReplacer(repl...).Replace(ctConfig(power, ""))
}

func ctUpdateCase(t *testing.T, f *fakeAPI, first, second string, check resource.TestCheckFunc, pre ...func()) {
	t.Helper()
	step2 := resource.TestStep{Config: f.config(second), Check: check}
	if len(pre) > 0 {
		step2.PreConfig = pre[0]
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy:             f.noVMsBut(),
		Steps:                    []resource.TestStep{{Config: f.config(first)}, step2, {Config: f.config(second), PlanOnly: true}},
	})
}

func TestLXCUpdateCoresMemoryOnly(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	ctUpdateCase(t, f, ctVariant("stopped"), ctVariant("stopped", "cores     = 2", "cores     = 4", "memory_mb = 1024", "memory_mb = 2048"), check(func() error {
		b := f.lastBody("PUT " + ctPath + "/config")
		if len(b) != 2 || b["cores"] != float64(4) || b["memory"] != float64(2048) {
			return fmt.Errorf("PUT = %v", b)
		}
		return nil
	}))
}

func TestLXCUpdateGrowAndMove(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	second := ctVariant("stopped", "    size_gb = 8", "    size_gb = 12", "    size_gb = 10", "    size_gb = 15", `  rootfs {
    storage = "ceph-gold"`, `  rootfs {
    storage = "ceph-silver"`)
	ctUpdateCase(t, f, ctVariant("stopped"), second, check(func() error {
		sizes := map[string]string{}
		for _, b := range f.allBodies("POST " + ctPath + "/disk/resize") {
			sizes[b["disk"].(string)] = b["size"].(string)
		}
		if sizes["rootfs"] != "12G" || sizes["mp0"] != "15G" {
			return fmt.Errorf("resizes = %v", sizes)
		}
		if m := f.lastBody("POST " + ctPath + "/disk/move"); m["disk"] != "rootfs" || m["storage"] != "ceph-silver" {
			return fmt.Errorf("move = %v", m)
		}
		return nil
	}))
}

// Review Focus 3.
func TestLXCUpdateVLANKeepsMACAndUnknownSubkeys(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	ctUpdateCase(t, f, ctVariant("stopped"), ctVariant("stopped", "vlan   = 150", "vlan   = 160"), check(func() error {
		b := f.lastBody("PUT " + ctPath + "/config")
		net0 := fmt.Sprint(b["net0"])
		if len(b) != 1 || !strings.Contains(net0, "tag=160") || !strings.Contains(net0, "hwaddr=") || !strings.Contains(net0, "ip6=auto") || !strings.Contains(net0, "type=veth") {
			return fmt.Errorf("PUT = %v", b)
		}
		return nil
	}), func() {
		f.mu.Lock()
		f.vms[9105].Config["net0"] = fmt.Sprint(f.vms[9105].Config["net0"]) + ",ip6=auto"
		f.mu.Unlock()
	})
}

func TestLXCUpdateAddedMountPointIsError(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	second := strings.Replace(ctConfig("stopped", "  mount_point {\n    slot    = \"mp1\"\n    storage = \"ceph-gold\"\n    size_gb = 2\n    path    = \"/logs\"\n  }"), "", "", 1)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: f.config(ctConfig("stopped", ""))},
			{Config: f.config(second), ExpectError: regexp.MustCompile(`(?s)mp1.*at\s+creation\s+only`)},
		},
	})
}

func TestLXCUpdateCreationOnlyKeysWarnNoCall(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	second := ctVariant("stopped", `ssh_keys = ["ssh-ed25519 AAAA ops@host\n"]`, `ssh_keys = ["ssh-ed25519 BBBB other@host"]`+"\n  nameserver = \"9.9.9.9\"")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy:             f.noVMsBut(),
		Steps: []resource.TestStep{
			{Config: f.config(ctConfig("stopped", ""))},
			{Config: f.config(second), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("proxcenter_lxc.ct", plancheck.ResourceActionUpdate)}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("proxcenter_lxc.ct", "nameserver", "9.9.9.9"),
					check(func() error {
						if n := f.count("PUT " + ctPath + "/config"); n != 0 {
							return fmt.Errorf("%d config writes for creation-only keys", n)
						}
						return nil
					}))},
			{Config: f.config(second), PlanOnly: true},
		},
	})
}

func TestLXCUpdatePowerStopStart(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	ctUpdateCase(t, f, ctVariant("running"), ctVariant("stopped"), check(func() error {
		if f.count("POST "+ctPath+"/shutdown") != 1 || f.vm(9105).Status != "stopped" {
			return fmt.Errorf("not shut down")
		}
		return nil
	}))
}

// Review Focus 5: live changes reboot nothing unless Proxmox reports pending.
func TestLXCUpdatePendingRebootsWhenAsked(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	f.pendingKeys["memory"] = true
	extra := "  reboot_on_change = true"
	first := ctConfig("running", extra)
	second := strings.Replace(first, "memory_mb = 1024", "memory_mb = 2048", 1)
	ctUpdateCase(t, f, first, second, check(func() error {
		if f.count("POST "+ctPath+"/reboot") != 1 {
			return fmt.Errorf("not rebooted")
		}
		return nil
	}))
}

func TestLXCUpdateLiveChangeNoReboot(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	first := ctConfig("running", "  reboot_on_change = true")
	second := strings.Replace(first, "cores     = 2", "cores     = 3", 1)
	ctUpdateCase(t, f, first, second, check(func() error {
		if f.count("POST "+ctPath+"/reboot") != 0 {
			return fmt.Errorf("rebooted for a live change")
		}
		return nil
	}))
}
