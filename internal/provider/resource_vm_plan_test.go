package provider

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// vmHCL builds a proxcenter_vm with the given source and target_node lines.
func vmHCL(source, targetNode string) string {
	return `
resource "proxcenter_vm" "web" {
  connection_id = "conn1"
  name          = "web-01"
` + targetNode + `
` + source + `
  cores     = 2
  memory_mb = 2048

  disk {
    slot    = "scsi0"
    storage = "ceph-gold"
    size_gb = 20
  }

  network {
    bridge = "vnet-prod"
  }

  power_state = "stopped"
}`
}

func planOnly(t *testing.T, f *fakeAPI, hcl string, expectErr *regexp.Regexp) {
	t.Helper()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: f.providerFactories(),
		Steps: []resource.TestStep{{
			Config:             f.config(hcl),
			PlanOnly:           true,
			ExpectNonEmptyPlan: expectErr == nil,
			ExpectError:        expectErr,
		}},
	})
}

func TestPlanCreateNeedsSource(t *testing.T) {
	planOnly(t, newFakeAPI(t).withVDC(), vmHCL("", `  target_node = "pve1"`), regexp.MustCompile(`(?s)source.*required to create`))
}

func TestPlanCatalogueNeedsTargetNode(t *testing.T) {
	planOnly(t, newFakeAPI(t).withVDC(), vmHCL(`  source { image = "ubuntu-24.04" }`, ""), regexp.MustCompile(`(?s)target_node.*nodes`))
}

func TestPlanBlueprintNeedsTargetNode(t *testing.T) {
	planOnly(t, newFakeAPI(t).withVDC(), vmHCL(`  source { blueprint = "bp1" }`, ""), regexp.MustCompile(`target_node`))
}

func TestPlanCloneWithoutTargetNodeOK(t *testing.T) {
	planOnly(t, newFakeAPI(t).withVDC(), vmHCL(`  source { clone_of = 9000 }`, ""), nil)
}

func TestPlanCatalogueWithTargetNodeOK(t *testing.T) {
	planOnly(t, newFakeAPI(t).withVDC(), vmHCL(`  source { image = "ubuntu-24.04" }`, `  target_node = "pve1"`), nil)
}

func TestPlanSourceExactlyOne(t *testing.T) {
	f := newFakeAPI(t).withVDC()
	planOnly(t, f, vmHCL(`  source {
    image    = "ubuntu-24.04"
    clone_of = 9000
  }`, `  target_node = "pve1"`), regexp.MustCompile(`exactly one of image, blueprint or clone_of`))
	planOnly(t, f, vmHCL(`  source {}`, `  target_node = "pve1"`), regexp.MustCompile(`exactly one of image, blueprint or clone_of`))
}

func TestPlanPowerStateValues(t *testing.T) {
	hcl := regexp.MustCompile(`power_state = "stopped"`).ReplaceAllString(vmHCL(`  source { clone_of = 9000 }`, ""), `power_state = "paused"`)
	planOnly(t, newFakeAPI(t).withVDC(), hcl, regexp.MustCompile(`power_state`))
}

func TestPlanNeedsADisk(t *testing.T) {
	hcl := regexp.MustCompile(`(?s)  disk \{.*?\n  \}\n`).ReplaceAllString(vmHCL(`  source { clone_of = 9000 }`, ""), "")
	planOnly(t, newFakeAPI(t).withVDC(), hcl, regexp.MustCompile(`(?i)disk`))
}
