package provider

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

const vmPath = "/api/v1/connections/conn1/guests/qemu/pve2/9105"

// variant is catalogueVM with textual replacements, old1, new1, old2, new2...
func variant(power string, repl ...string) string {
	return strings.NewReplacer(repl...).Replace(fmt.Sprintf(catalogueVM, power))
}

func updateCase(t *testing.T, f *fakeAPI, first, second string, check resource.TestCheckFunc, planChecks ...plancheck.PlanCheck) {
	t.Helper()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy:             f.noVMsBut(),
		Steps: []resource.TestStep{
			{Config: f.config(first)},
			{Config: f.config(second), Check: check, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: planChecks}},
		},
	})
}

func TestUpdateCoresOnlySendsCores(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	updateCase(t, f, variant("stopped"), variant("stopped", "cores     = 4", "cores     = 8"), check(func() error {
		bodies := f.allBodies("PUT " + vmPath + "/config")
		last := bodies[len(bodies)-1]
		if len(last) != 1 || last["cores"] != float64(8) {
			return fmt.Errorf("PUT = %v, want only cores", last)
		}
		return nil
	}), plancheck.ExpectResourceAction("proxcenter_vm.web", plancheck.ResourceActionUpdate))
}

func TestUpdateGrowDiskAbsolute(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	updateCase(t, f, variant("stopped"), variant("stopped", "size_gb = 64", "size_gb = 80"), check(func() error {
		if b := f.lastBody("POST " + vmPath + "/disk/resize"); b["disk"] != "scsi0" || b["size"] != "80G" {
			return fmt.Errorf("resize = %v", b)
		}
		return nil
	}))
}

func TestUpdateMoveDisk(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	updateCase(t, f, variant("stopped"), variant("stopped", `storage = "ceph-silver"`, `storage = "ceph-gold"`), check(func() error {
		if b := f.lastBody("POST " + vmPath + "/disk/move"); b["disk"] != "scsi1" || b["storage"] != "ceph-gold" {
			return fmt.Errorf("move = %v", b)
		}
		return nil
	}), plancheck.ExpectResourceAction("proxcenter_vm.web", plancheck.ResourceActionUpdate))
}

func TestUpdateAddNIC(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	// Several network blocks name their slots.
	second := variant("stopped", "    bridge = \"vnet-prod\"", "    slot   = \"net0\"\n    bridge = \"vnet-prod\"", "  cloud_init {", "  network {\n    slot   = \"net1\"\n    bridge = \"vnet2\"\n  }\n\n  cloud_init {")
	updateCase(t, f, variant("stopped"), second, resource.ComposeAggregateTestCheckFunc(
		resource.TestCheckResourceAttr("proxcenter_vm.web", "network.1.slot", "net1"),
		check(func() error {
			if b := f.lastBody("PUT " + vmPath + "/config"); b["net1"] != "virtio,bridge=vnet2" {
				return fmt.Errorf("PUT = %v", b)
			}
			return nil
		})))
}

func TestUpdateVLANChange(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	updateCase(t, f, variant("stopped"), variant("stopped", "vlan   = 120", "vlan   = 130"), check(func() error {
		b := f.lastBody("PUT " + vmPath + "/config")
		if len(b) != 1 || b["net0"] != "virtio=BC:24:11:00:23:91,bridge=vnet-prod,tag=130,firewall=1" {
			return fmt.Errorf("PUT = %v", b)
		}
		return nil
	}))
}

func TestUpdatePendingWarnsWithoutReboot(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	f.pendingKeys["cores"] = true
	updateCase(t, f, variant("running"), variant("running", "cores     = 4", "cores     = 8"), resource.ComposeAggregateTestCheckFunc(
		resource.TestCheckResourceAttr("proxcenter_vm.web", "cores", "8"),
		check(func() error {
			if f.count("POST "+vmPath+"/reboot") != 0 {
				return fmt.Errorf("rebooted without reboot_on_change")
			}
			if f.vm(9105).Pending["cores"] == nil {
				return fmt.Errorf("the change should still wait for a reboot")
			}
			return nil
		})))
}

func TestUpdatePendingRebootsWhenAsked(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	f.pendingKeys["cores"] = true
	withReboot := func(cores string) string {
		return variant("running", "cores     = 4", "cores     = "+cores, `power_state = "running"`, "power_state = \"running\"\n  reboot_on_change = true")
	}
	updateCase(t, f, withReboot("4"), withReboot("8"), check(func() error {
		if f.count("POST "+vmPath+"/reboot") != 1 || f.vm(9105).Config["cores"] != int64(8) && f.vm(9105).Config["cores"] != float64(8) {
			return fmt.Errorf("reboots=%d cores=%v", f.count("POST "+vmPath+"/reboot"), f.vm(9105).Config["cores"])
		}
		return nil
	}))
}

func withPassword(power, password, version string) string {
	return variant(power, `    user     = "ops"`, `    user     = "ops"
    password_wo         = "`+password+`"
    password_wo_version = `+version)
}

func TestUpdatePasswordVersionSendsPassword(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	updateCase(t, f, withPassword("stopped", "first-secret", "1"), withPassword("stopped", "second-secret", "2"), resource.ComposeAggregateTestCheckFunc(
		resource.TestCheckNoResourceAttr("proxcenter_vm.web", "cloud_init.password_wo"),
		resource.TestCheckResourceAttr("proxcenter_vm.web", "cloud_init.password_wo_version", "2"),
		check(func() error {
			if ci := f.lastBody("POST /api/v1/templates/deploy")["cloudInit"].(map[string]any); ci["cipassword"] != "first-secret" {
				return fmt.Errorf("deploy cloudInit = %v", ci)
			}
			if b := f.lastBody("PUT " + vmPath + "/config"); b["cipassword"] != "second-secret" {
				return fmt.Errorf("PUT = %v", b)
			}
			return nil
		})))
}

func TestUpdatePowerStop(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	updateCase(t, f, variant("running"), variant("stopped"), check(func() error {
		if f.count("POST "+vmPath+"/shutdown") != 1 || f.vm(9105).Status != "stopped" {
			return fmt.Errorf("not shut down")
		}
		return nil
	}))
}

func TestUpdate202Waited(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	f.hook(http.MethodPut, vmPath+"/config", func(w http.ResponseWriter, _ *http.Request, body map[string]any) bool {
		if body["cores"] == nil {
			return false
		}
		f.mu.Lock()
		f.vms[9105].Config["cores"] = body["cores"]
		f.mu.Unlock()
		reply(w, 202, map[string]any{"data": "UPID:pve2:late", "pending": true, "upid": "UPID:pve2:late", "node": "pve2"})
		return true
	})
	updateCase(t, f, variant("stopped"), variant("stopped", "cores     = 4", "cores     = 8"), check(func() error {
		if f.count("GET /api/v1/tasks/conn1/pve2/UPID:pve2:late") != 1 {
			return fmt.Errorf("the 202 task was not followed")
		}
		return nil
	}))
}

func TestPlanDiskShrinkIsError(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: f.config(variant("stopped"))},
			{Config: f.config(variant("stopped", "size_gb = 64", "size_gb = 32")), ExpectError: regexp.MustCompile(`(?s)64\s+GB\s+to\s+32\s+GB.*cannot\s+shrink`)},
		},
	})
}

func TestPlanDiskRemovedIsError(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	second := regexp.MustCompile(`(?s)  disk \{\n    slot    = "scsi1".*?\n  \}\n`).ReplaceAllString(variant("stopped"), "")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: f.config(variant("stopped"))},
			{Config: f.config(second), ExpectError: regexp.MustCompile(`(?s)scsi1.*does\s+not\s+detach\s+disks`)},
		},
	})
}

func TestPlanSourceChangeReplaces(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	updateCase(t, f, variant("stopped"), variant("stopped", "ubuntu-24.04", "debian-12"), resource.TestCheckResourceAttr("proxcenter_vm.web", "vmid", "9106"),
		plancheck.ExpectResourceAction("proxcenter_vm.web", plancheck.ResourceActionDestroyBeforeCreate))
}

func TestPlanImportedVMGainsSourceInPlace(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	seedUIVM(f)
	withSource := strings.Replace(importedVM, `  power_state   = "running"`, "  power_state   = \"running\"\n\n  source { clone_of = 9000 }", 1)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: f.config(importedVM), ResourceName: "proxcenter_vm.legacy", ImportState: true, ImportStateId: "conn1/9200", ImportStatePersist: true},
			{Config: f.config(withSource), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("proxcenter_vm.legacy", plancheck.ResourceActionUpdate)}}},
		},
	})
}
