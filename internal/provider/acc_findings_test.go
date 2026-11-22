package provider

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// laggingList hides the given VMID from /api/v1/vms for the first n reads,
// as ProxCenter's 30 s list cache does right after a creation (lab finding).
func laggingList(f *fakeAPI, vmid string, n int) {
	reads := 0
	f.hook(http.MethodGet, "/api/v1/vms", func(w http.ResponseWriter, r *http.Request, _ map[string]any) bool {
		reads++
		if reads > n || !strings.Contains(r.URL.RawQuery, "vmids="+vmid) {
			return false
		}
		reply(w, 200, map[string]any{"data": map[string]any{"vms": []any{}}})
		return true
	})
}

func TestImportWaitsForTheListToCatchUp(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	seedUIVM(f)
	laggingList(f, "9200", 2)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: f.config(importedVM), ResourceName: "proxcenter_vm.legacy", ImportState: true, ImportStateId: "conn1/9200", ImportStatePersist: true},
			{Config: f.config(importedVM), PlanOnly: true},
		},
	})
}

func TestCloneSourceWaitsForTheListToCatchUp(t *testing.T) {
	f := newFakeAPI(t).withVDC().withTemplate()
	laggingList(f, "9000", 2)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy:             f.noVMsBut(9000),
		Steps:                    []resource.TestStep{{Config: f.config(cloneVM)}},
	})
}

func failReboot(f *fakeAPI) {
	f.hook(http.MethodPost, vmPath+"/reboot", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
		errorReply(w, 500, "VM quit/powerdown failed - got timeout")
		return true
	})
}

// A guest that ignores ACPI (still booting): reboot_on_change falls back to
// a hard stop and a start when force_stop allows it (lab finding).
func TestUpdateRebootFallsBackToStopStartWithForceStop(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	f.pendingKeys["cores"] = true
	failReboot(f)
	cfg := func(cores string) string {
		return variant("running", "cores     = 4", "cores     = "+cores, `power_state = "running"`, "power_state = \"running\"\n  reboot_on_change = true\n  force_stop       = true")
	}
	updateCase(t, f, cfg("4"), cfg("8"), check(func() error {
		if f.count("POST "+vmPath+"/stop") != 1 || f.count("POST "+vmPath+"/start") != 2 {
			return fmt.Errorf("stop=%d start=%d, want a hard stop then a start", f.count("POST "+vmPath+"/stop"), f.count("POST "+vmPath+"/start"))
		}
		if f.vm(9105).Status != "running" || len(f.vm(9105).Pending) != 0 {
			return fmt.Errorf("the VM should run with its change applied")
		}
		return nil
	}))
}

func TestUpdateRebootFailureWithoutForceStopSaysSo(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	f.pendingKeys["cores"] = true
	cfg := func(cores string) string {
		return variant("running", "cores     = 4", "cores     = "+cores, `power_state = "running"`, "power_state = \"running\"\n  reboot_on_change = true")
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{
			{Config: f.config(cfg("4"))},
			{PreConfig: func() { failReboot(f) }, Config: f.config(cfg("8")), ExpectError: regexp.MustCompile(`(?s)got\s+timeout.*force_stop`)},
			{PreConfig: func() { f.hook(http.MethodPost, vmPath+"/reboot", nil) }, Config: f.config(cfg("8"))},
		},
	})
}

// laggingPower makes /api/v1/vms report the VM stopped whatever it is, as the
// ProxCenter list does for up to 30 s after a start (lab finding).
func laggingPower(f *fakeAPI) {
	f.hook(http.MethodGet, "/api/v1/vms", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []map[string]any{}
		for id, vm := range f.vms {
			out = append(out, map[string]any{"connId": "conn1", "node": vm.Node, "vmid": fmt.Sprint(id), "name": vm.Config["name"], "type": "qemu", "status": "stopped"})
		}
		reply(w, 200, map[string]any{"data": map[string]any{"vms": out}})
		return true
	})
}

func TestReadPowerFromLiveStatusNotTheLaggingList(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	laggingPower(f)
	running := f.config(fmt.Sprintf(catalogueVM, "running"))
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps:                    []resource.TestStep{{Config: running}, {Config: running, PlanOnly: true}},
	})
}

func TestDeleteStopsAVMTheListStillCallsStopped(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	laggingPower(f)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy: resource.ComposeTestCheckFunc(f.noVMsBut(), check(func() error {
			if f.count("POST "+vmPath+"/shutdown") != 1 {
				return fmt.Errorf("no shutdown before the delete")
			}
			return nil
		})),
		Steps: []resource.TestStep{{Config: f.config(fmt.Sprintf(catalogueVM, "running"))}},
	})
}

// A ProxCenter without the status route still works from the list.
func TestReadPowerFallsBackToTheListWithoutStatusRoute(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	f.hook(http.MethodGet, vmPath+"/status", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
		errorReply(w, 403, "Route not available to API tokens")
		return true
	})
	stopped := f.config(fmt.Sprintf(catalogueVM, "stopped"))
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps:                    []resource.TestStep{{Config: stopped}, {Config: stopped, PlanOnly: true}},
	})
}

// Without the status route and with the lagging list, the delete meets
// ProxCenter's own refusal: it stops the VM and deletes again (lab finding).
func TestDeleteRetriesAfterMustBeStoppedRefusal(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	laggingPower(f)
	f.hook(http.MethodGet, vmPath+"/status", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
		errorReply(w, 403, "Route not available to API tokens")
		return true
	})
	f.hook(http.MethodDelete, vmPath, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
		if vm := f.vm(9105); vm != nil && vm.Status == "running" {
			reply(w, 400, map[string]any{"error": "VM must be stopped before being deleted", "code": "vm_running"})
			return true
		}
		return false
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		CheckDestroy:             f.noVMsBut(),
		// Without the status route the lagging list shows a power drift:
		// the limit an older ProxCenter keeps; the delete is what is checked.
		Steps: []resource.TestStep{{Config: f.config(fmt.Sprintf(catalogueVM, "running")), ExpectNonEmptyPlan: true}},
	})
}
